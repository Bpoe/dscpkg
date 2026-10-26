package dscpkg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type installedPackage struct {
	SchemaVersion int    `json:"schemaVersion"`
	Platform      string `json:"platform"`
}

// Keep provenance beside the version directory, outside the verified archive contents.
func installedMetadataPath(packagePath string) string {
	return filepath.Join(filepath.Dir(packagePath), "."+filepath.Base(packagePath)+".dscpkg.json")
}

func readInstalledPackage(packagePath string) (installedPackage, error) {
	path := installedMetadataPath(packagePath)
	info, err := os.Lstat(path)
	if err != nil {
		return installedPackage{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxDocumentSize {
		return installedPackage{}, fmt.Errorf("invalid installed package metadata %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return installedPackage{}, err
	}
	var installed installedPackage
	if err := json.Unmarshal(data, &installed); err != nil {
		return installedPackage{}, fmt.Errorf("parse installed package metadata %q: %w", path, err)
	}
	platform, err := normalizePlatform(installed.Platform)
	if err != nil || installed.SchemaVersion != 1 || platform != installed.Platform {
		return installedPackage{}, fmt.Errorf("invalid installed package metadata %q: expected schemaVersion 1 and an os_arch platform", path)
	}
	return installed, nil
}

func writeInstalledPackage(packagePath, platform string) error {
	if normalized, err := normalizePlatform(platform); err != nil || normalized != platform {
		return fmt.Errorf("invalid installation platform %q", platform)
	}
	data, err := json.Marshal(installedPackage{SchemaVersion: 1, Platform: platform})
	if err != nil {
		return err
	}
	return atomicWrite(installedMetadataPath(packagePath), append(data, '\n'), 0o644)
}
