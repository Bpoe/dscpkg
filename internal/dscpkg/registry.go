package dscpkg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func ReadRegistry(packagesDir string) (Registry, error) {
	path := filepath.Join(packagesDir, "resources.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Registry{Resources: []RegisteredResource{}}, nil
	}
	if err != nil {
		return Registry{}, fmt.Errorf("read resource registry: %w", err)
	}
	var registry Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		return Registry{}, fmt.Errorf("parse resource registry: %w", err)
	}
	if registry.Resources == nil {
		return Registry{}, errors.New("resource registry must contain a resources array")
	}
	for i := range registry.Resources {
		resource, err := normalizeResource(registry.Resources[i].Resource)
		if err != nil || strings.TrimSpace(registry.Resources[i].Version) == "" {
			return Registry{}, fmt.Errorf("invalid resource registry entry at index %d", i)
		}
		registry.Resources[i].Resource = resource
		registry.Resources[i].Version = strings.ToLower(strings.TrimSpace(registry.Resources[i].Version))
	}
	return registry, nil
}

func writeRegistry(packagesDir string, registry Registry) error {
	if err := os.MkdirAll(packagesDir, 0o755); err != nil {
		return err
	}
	byKey := make(map[string]RegisteredResource, len(registry.Resources))
	for _, resource := range registry.Resources {
		name, err := normalizeResource(resource.Resource)
		if err != nil || strings.TrimSpace(resource.Version) == "" {
			return fmt.Errorf("invalid registered resource %q", resource.Resource)
		}
		resource.Resource = name
		resource.Version = strings.ToLower(strings.TrimSpace(resource.Version))
		byKey[name+"\x00"+resource.Version] = resource
	}
	registry.Resources = registry.Resources[:0]
	for _, resource := range byKey {
		registry.Resources = append(registry.Resources, resource)
	}
	sort.Slice(registry.Resources, func(i, j int) bool {
		a, b := registry.Resources[i], registry.Resources[j]
		if a.Resource == b.Resource {
			return a.Version < b.Version
		}
		return a.Resource < b.Resource
	})
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(packagesDir, "resources.json"), append(data, '\n'), 0o600)
}

func registerResource(packagesDir, resource, version, digest string) error {
	return withPackagesLock(packagesDir, func() error {
		return registerResourceLocked(packagesDir, resource, version, digest)
	})
}

func registerResourceLocked(packagesDir, resource, version, digest string) error {
	registry, err := ReadRegistry(packagesDir)
	if err != nil {
		return err
	}
	resource, err = normalizeResource(resource)
	if err != nil {
		return err
	}
	version = strings.ToLower(strings.TrimSpace(version))
	for i := range registry.Resources {
		if registry.Resources[i].Resource == resource && registry.Resources[i].Version == version {
			registry.Resources[i].Digest = digest
			return writeRegistry(packagesDir, registry)
		}
	}
	registry.Resources = append(registry.Resources, RegisteredResource{Resource: resource, Version: version, Digest: digest})
	return writeRegistry(packagesDir, registry)
}

func RemoveResource(packagesDir, resource, version string) error {
	return withPackagesLock(packagesDir, func() error {
		return removeResourceLocked(packagesDir, resource, version)
	})
}

func removeResourceLocked(packagesDir, resource, version string) error {
	resource, err := normalizeResource(resource)
	if err != nil {
		return err
	}
	registry, err := ReadRegistry(packagesDir)
	if err != nil {
		return err
	}
	version = strings.ToLower(strings.TrimSpace(version))
	kept := registry.Resources[:0]
	for _, entry := range registry.Resources {
		if entry.Resource == resource && (version == "" || entry.Version == version) {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == len(registry.Resources) {
		return fmt.Errorf("resource %q is not registered", resource)
	}
	registry.Resources = kept
	return writeRegistry(packagesDir, registry)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".dscpkg-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
