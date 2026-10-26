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

type InstalledManifest struct {
	ManifestPath   string
	Package        string
	PackageVersion string
	Path           string
}

// Discover emits DSC 3.3 discovery JSON Lines using only the local package store.
// It does not acquire the store's write lock, create files, or consult repositories.
func Discover(packagesDir string, stdout, stderr io.Writer) error {
	return discover(packagesDir, currentPlatform(), stdout, stderr)
}

func discover(packagesDir, platform string, stdout, stderr io.Writer) error {
	manifests, err := EnumerateInstalledManifests(packagesDir, platform, stderr)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	for _, manifest := range manifests {
		if err := encoder.Encode(discoveredManifest{ManifestPath: manifest.ManifestPath}); err != nil {
			return fmt.Errorf("write discovered manifest: %w", err)
		}
	}
	return nil
}

// EnumerateInstalledManifests is the read-only platform gate for discovery and inventory.
func EnumerateInstalledManifests(packagesDir, platform string, stderr io.Writer) ([]InstalledManifest, error) {
	root, err := filepath.Abs(packagesDir)
	if err != nil {
		return nil, err
	}
	directories, err := packageDirectories(root)
	if err != nil {
		return nil, fmt.Errorf("enumerate installed packages: %w", err)
	}
	var manifests []InstalledManifest
	seen := make(map[string]bool)
	for _, directory := range directories {
		installed, err := readInstalledPackage(directory)
		if errors.Is(err, os.ErrNotExist) {
			if _, err := fmt.Fprintf(stderr, "dscpkg: skipping %q: platform provenance is missing; move this version directory aside and reinstall its resources with dscpkg to verify the platform\n", directory); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("verify package %q (move it aside and reinstall to repair): %w", directory, err)
		}
		if installed.Platform != platform {
			continue
		}
		if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type().IsRegular() && isResourceManifest(entry.Name()) {
				key := canonicalPath(path)
				if !seen[key] {
					seen[key] = true
					pkg := filepath.Dir(directory)
					manifests = append(manifests, InstalledManifest{
						ManifestPath: path, Path: directory, PackageVersion: filepath.Base(directory),
						Package: filepath.Base(filepath.Dir(pkg)) + "/" + filepath.Base(pkg),
					})
				}
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("enumerate manifests in %q: %w", directory, err)
		}
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ManifestPath < manifests[j].ManifestPath })
	return manifests, nil
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
