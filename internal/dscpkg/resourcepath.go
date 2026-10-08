package dscpkg

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func ResourcePath(packagesDir, existing string) (string, error) {
	packagePaths, err := packageDirectories(packagesDir)
	if err != nil {
		return "", err
	}
	existingPaths := splitPaths(existing)
	kept := make([]string, 0, len(existingPaths))
	for _, item := range existingPaths {
		if !pathUnder(item, packagesDir) {
			kept = append(kept, item)
		}
	}
	return strings.Join(uniquePaths(append(kept, packagePaths...)), string(os.PathListSeparator)), nil
}

func packageDirectories(packagesDir string) ([]string, error) {
	var result []string
	namespaces, err := os.ReadDir(packagesDir)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, namespace := range namespaces {
		if !namespace.IsDir() || strings.HasPrefix(namespace.Name(), ".") {
			continue
		}
		packages, err := os.ReadDir(filepath.Join(packagesDir, namespace.Name()))
		if err != nil {
			return nil, err
		}
		for _, pkg := range packages {
			if !pkg.IsDir() {
				continue
			}
			versions, err := os.ReadDir(filepath.Join(packagesDir, namespace.Name(), pkg.Name()))
			if err != nil {
				return nil, err
			}
			for _, version := range versions {
				if version.IsDir() {
					result = append(result, filepath.Join(packagesDir, namespace.Name(), pkg.Name(), version.Name()))
				}
			}
		}
	}
	sort.Strings(result)
	return result, nil
}

func replacePackagePaths(existing, packagesDir string, desired []string) (string, error) {
	var kept []string
	for _, item := range splitPaths(existing) {
		if !pathUnder(item, packagesDir) {
			kept = append(kept, item)
		}
	}
	return strings.Join(uniquePaths(append(kept, desired...)), string(os.PathListSeparator)), nil
}

func addPath(existing, value string) string {
	return strings.Join(uniquePaths(append(splitPaths(existing), value)), string(os.PathListSeparator))
}

func splitPaths(value string) []string {
	var result []string
	for _, item := range strings.Split(value, string(os.PathListSeparator)) {
		if strings.TrimSpace(item) != "" {
			result = append(result, item)
		}
	}
	return result
}

func uniquePaths(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, item := range values {
		key := canonicalPath(item)
		if !seen[key] {
			seen[key] = true
			result = append(result, item)
		}
	}
	return result
}

func canonicalPath(value string) string {
	full, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		full = filepath.Clean(value)
	}
	if runtime.GOOS == "windows" {
		full = strings.ToLower(full)
	}
	return full
}

func pathUnder(value, root string) bool {
	child, base := canonicalPath(value), canonicalPath(root)
	relative, err := filepath.Rel(base, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
