package dscpkg

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type Discovery struct {
	Catalog         string `json:"catalog.v1"`
	Resources       string `json:"resources.v1"`
	Packages        string `json:"packages.v1"`
	SigningPolicies string `json:"signing-policies.v1"`
}

type Catalog struct {
	Resources map[string]map[string]CatalogEntry `json:"resources"`
}

type CatalogEntry struct {
	URL    string `json:"url"`
	Digest string `json:"digest"`
}

type ResourceDescriptor struct {
	Packages map[string]struct {
		Versions map[string]json.RawMessage `json:"versions"`
	} `json:"packages"`
}

type PackageDescriptor struct {
	Archives map[string]Archive `json:"archives"`
}

type Archive struct {
	URL    string   `json:"url"`
	Hashes []string `json:"hashes"`
}

type Registry struct {
	Resources []RegisteredResource `json:"resources"`
}

type RegisteredResource struct {
	Resource string `json:"resource"`
	Version  string `json:"version"`
	Digest   string `json:"digest,omitempty"`
}

type Policy struct {
	Namespace string `json:"namespace"`
	Required  bool   `json:"required"`
	Keys      []JWK  `json:"keys"`
}

type JWK struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x"`
	Y   string `json:"y,omitempty"`
	N   string `json:"n,omitempty"`
	E   string `json:"e,omitempty"`
}

type policyDocument struct {
	Policies []Policy `json:"policies"`
}

type packageResolution struct {
	Resource       string `json:"resource"`
	Version        string `json:"version"`
	Package        string `json:"package"`
	PackageVersion string `json:"packageVersion"`
	Digest         string `json:"digest"`
	Path           string `json:"path"`
}

func normalizeResource(resource string) (string, error) {
	value := strings.ToLower(strings.Trim(resource, "/"))
	parts := strings.Split(value, "/")
	if len(parts) != 2 || !validSegment(parts[0]) || !validSegment(parts[1]) {
		return "", fmt.Errorf("invalid resource type %q: expected Namespace/name", resource)
	}
	return value, nil
}

func normalizePackage(name string) (string, error) {
	value := strings.ToLower(strings.Trim(name, "/"))
	parts := strings.Split(value, "/")
	if len(parts) != 2 || !validSegment(parts[0]) || !validSegment(parts[1]) {
		return "", fmt.Errorf("invalid package name %q: expected namespace/name", name)
	}
	return value, nil
}

func validSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func resolveURL(base, reference string) (string, error) {
	ref, err := url.Parse(reference)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", reference, err)
	}
	if !ref.IsAbs() && base == "" {
		return "", errors.New("relative URL has no base")
	}
	if ref.IsAbs() {
		if ref.Scheme != "http" && ref.Scheme != "https" {
			return "", fmt.Errorf("unsupported URL scheme %q", ref.Scheme)
		}
		return ref.String(), nil
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	return baseURL.ResolveReference(ref).String(), nil
}

func namespace(name string) string {
	return strings.ToLower(strings.SplitN(name, "/", 2)[0])
}
