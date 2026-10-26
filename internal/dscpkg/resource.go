package dscpkg

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type ResourceState struct {
	Resource       string   `json:"resource"`
	Version        string   `json:"version,omitempty"`
	State          string   `json:"state"`
	Repositories   []string `json:"repositories,omitempty"`
	Package        string   `json:"package,omitempty"`
	PackageVersion string   `json:"packageVersion,omitempty"`
	Path           string   `json:"path,omitempty"`
	InDesiredState *bool    `json:"_inDesiredState,omitempty"`
}

type manifestIdentity struct {
	Type           string             `json:"type" yaml:"type"`
	Version        string             `json:"version" yaml:"version"`
	RequireAdapter string             `json:"requireAdapter" yaml:"requireAdapter"`
	Resources      []manifestIdentity `json:"resources" yaml:"resources"`
}

func validateResourceState(desired ResourceState) error {
	if _, err := normalizeResource(desired.Resource); err != nil {
		return err
	}
	if desired.Version != "" && !validSegment(strings.ToLower(desired.Version)) {
		return fmt.Errorf("invalid resource version %q", desired.Version)
	}
	if desired.State != "" && desired.State != "installed" && desired.State != "missing" {
		return fmt.Errorf("invalid resource state %q: expected installed or missing", desired.State)
	}
	return nil
}

// InstalledResources reads the same compatible manifests advertised by discovery.
// Adapter-backed entries need adapter discovery, so are deliberately not asserted here.
func (c *Client) InstalledResources(stderr io.Writer) ([]ResourceState, error) {
	manifests, err := EnumerateInstalledManifests(c.PackagesDir, c.Platform, stderr)
	if err != nil {
		return nil, err
	}
	result := make([]ResourceState, 0)
	for _, manifest := range manifests {
		file, err := os.Open(manifest.ManifestPath)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxDocumentSize+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(data) > maxDocumentSize {
			return nil, fmt.Errorf("manifest %q exceeds size limit", manifest.ManifestPath)
		}
		var identity manifestIdentity
		if strings.HasSuffix(strings.ToLower(manifest.ManifestPath), ".json") {
			err = json.Unmarshal(data, &identity)
		} else {
			err = yaml.Unmarshal(data, &identity)
		}
		if err != nil {
			return nil, fmt.Errorf("parse manifest %q: %w", manifest.ManifestPath, err)
		}
		identities := []manifestIdentity{identity}
		name := strings.ToLower(manifest.ManifestPath)
		if strings.Contains(name, ".dsc.manifests.") {
			if identity.Resources == nil {
				return nil, fmt.Errorf("manifest list %q has no resources array", manifest.ManifestPath)
			}
			identities = identity.Resources
		}
		for _, identity := range identities {
			if identity.RequireAdapter != "" || strings.Contains(name, ".dsc.adaptedresource.") {
				if _, err := fmt.Fprintf(stderr, "dscpkg: skipping adapter-backed resource in %q: offline inventory cannot verify adapter availability\n", manifest.ManifestPath); err != nil {
					return nil, err
				}
				continue
			}
			if _, err := normalizeResource(identity.Type); err != nil {
				return nil, fmt.Errorf("manifest %q: %w", manifest.ManifestPath, err)
			}
			if !validSegment(strings.ToLower(identity.Version)) {
				return nil, fmt.Errorf("manifest %q has invalid resource version %q", manifest.ManifestPath, identity.Version)
			}
			result = append(result, ResourceState{
				Resource: identity.Type, Version: identity.Version, State: "installed",
				Package: manifest.Package, PackageVersion: manifest.PackageVersion, Path: manifest.Path,
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if strings.ToLower(a.Resource) != strings.ToLower(b.Resource) {
			return strings.ToLower(a.Resource) < strings.ToLower(b.Resource)
		}
		if compared := compareVersions(a.Version, b.Version); compared != 0 {
			return compared > 0
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		if compared := compareVersions(a.PackageVersion, b.PackageVersion); compared != 0 {
			return compared > 0
		}
		return a.Path < b.Path
	})
	seen := make(map[string]bool)
	unique := make([]ResourceState, 0, len(result))
	for _, state := range result {
		key := strings.ToLower(state.Resource + "@" + state.Version)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, state)
		}
	}
	return unique, nil
}

func (c *Client) GetResource(desired ResourceState, stderr io.Writer) (ResourceState, error) {
	if err := validateResourceState(desired); err != nil {
		return ResourceState{}, err
	}
	resource, err := normalizeResource(desired.Resource)
	if err != nil {
		return ResourceState{}, err
	}
	installed, err := c.InstalledResources(stderr)
	if err != nil {
		return ResourceState{}, err
	}
	for _, state := range installed {
		if strings.EqualFold(state.Resource, resource) &&
			(desired.Version == "" || strings.EqualFold(state.Version, desired.Version)) {
			state.Repositories = desired.Repositories
			return state, nil
		}
	}
	return ResourceState{Resource: desired.Resource, State: "missing", Repositories: desired.Repositories}, nil
}

func (c *Client) TestResource(desired ResourceState, stderr io.Writer) (ResourceState, []string, error) {
	actual, err := c.GetResource(desired, stderr)
	if err != nil {
		return ResourceState{}, nil, err
	}
	state := desired.State
	if state == "" {
		state = "installed"
	}
	differences := make([]string, 0)
	if actual.State != state {
		differences = append(differences, "state")
	}
	if desired.Version != "" && !strings.EqualFold(desired.Version, actual.Version) {
		differences = append(differences, "version")
	}
	compliant := len(differences) == 0
	actual.InDesiredState = &compliant
	return actual, differences, nil
}

func (c *Client) SetResource(desired ResourceState, stderr io.Writer) (ResourceState, error) {
	if desired.State == "missing" {
		return ResourceState{}, errors.New("state missing is observation-only; resource removal is not supported")
	}
	actual, differences, err := c.TestResource(desired, stderr)
	if err != nil {
		return ResourceState{}, err
	}
	actual.InDesiredState = nil
	if len(differences) == 0 {
		return actual, nil
	}
	installer := *c
	if len(desired.Repositories) > 0 {
		installer.Repositories = desired.Repositories
	}
	if _, err := installer.Install(desired.Resource, desired.Version, ""); err != nil {
		return ResourceState{}, err
	}
	actual, differences, err = c.TestResource(desired, stderr)
	if err != nil {
		return ResourceState{}, err
	}
	if len(differences) != 0 {
		return ResourceState{}, fmt.Errorf("installed package does not expose the requested discoverable resource %s@%s; differing properties: %s", desired.Resource, desired.Version, strings.Join(differences, ", "))
	}
	actual.InDesiredState = nil
	return actual, nil
}
