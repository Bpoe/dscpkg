package dscpkg

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type discoveredManifest struct {
	ManifestPath string `json:"manifestPath"`
}

// Discover emits DSC 3.3 discovery JSON Lines using only the local package store.
// It does not acquire the store's write lock, create files, or consult repositories.
func Discover(packagesDir string, stdout, stderr io.Writer) error {
	return discover(packagesDir, currentPlatform(), stdout, stderr)
}

func discover(packagesDir, platform string, stdout, stderr io.Writer) error {
	root, err := filepath.Abs(packagesDir)
	if err != nil {
		return err
	}
	directories, err := packageDirectories(root)
	if err != nil {
		return fmt.Errorf("enumerate installed packages: %w", err)
	}
	var manifests []string
	for _, directory := range directories {
		installed, err := readInstalledPackage(directory)
		if errors.Is(err, os.ErrNotExist) {
			if _, err := fmt.Fprintf(stderr, "dscpkg: skipping %q: platform provenance is missing; move this version directory aside and reinstall its resources with dscpkg to verify the platform\n", directory); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("verify package %q (move it aside and reinstall to repair): %w", directory, err)
		}
		if installed.Platform != platform {
			continue
		}
		if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type().IsRegular() && isResourceManifest(entry.Name()) {
				manifests = append(manifests, path)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("enumerate manifests in %q: %w", directory, err)
		}
	}
	sort.Strings(manifests)
	encoder := json.NewEncoder(stdout)
	for _, path := range uniquePaths(manifests) {
		if err := encoder.Encode(discoveredManifest{ManifestPath: path}); err != nil {
			return fmt.Errorf("write discovered manifest: %w", err)
		}
	}
	return nil
}

func isResourceManifest(name string) bool {
	name = strings.ToLower(name)
	for _, kind := range []string{".dsc.resource", ".dsc.adaptedresource", ".dsc.manifests"} {
		for _, extension := range []string{".json", ".yaml", ".yml"} {
			if strings.HasSuffix(name, kind+extension) {
				return true
			}
		}
	}
	return false
}
