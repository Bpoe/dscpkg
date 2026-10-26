package dscpkg

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const maxDocumentSize = 16 << 20

type Client struct {
	PackagesDir  string
	Repositories []string
	Platform     string
	HTTPClient   *http.Client
}

type catalogCache struct {
	ETag string `json:"etag"`
	Body []byte `json:"body"`
}

type repository struct {
	origin     string
	discovery  Discovery
	catalogURL string
	policies   map[string]Policy
}

type document struct {
	body    []byte
	headers http.Header
}

var errNoDiscovery = errors.New("repository has no discovery document")

type InstallResult struct {
	Resource        string `json:"resource"`
	Version         string `json:"version"`
	Package         string `json:"package"`
	PackageVersion  string `json:"packageVersion"`
	Platform        string `json:"platform"`
	Path            string `json:"path"`
	Downloaded      bool   `json:"downloaded"`
	DSCResourcePath string `json:"dscResourcePath"`
}

func NewClient(packagesDir string, repositories []string) *Client {
	return &Client{
		PackagesDir:  packagesDir,
		Repositories: append([]string(nil), repositories...),
		Platform:     runtime.GOOS + "_" + runtime.GOARCH,
		HTTPClient:   &http.Client{Timeout: 2 * time.Minute},
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (c *Client) Install(resource, version, requestedPackageVersion string) (InstallResult, error) {
	var result InstallResult
	err := withPackagesLock(c.PackagesDir, func() error {
		var err error
		result, err = c.install(resource, version, requestedPackageVersion)
		return err
	})
	return result, err
}

func (c *Client) install(resource, version, requestedPackageVersion string) (InstallResult, error) {
	resolved, repo, err := c.resolve(resource, version, requestedPackageVersion)
	if err != nil {
		return InstallResult{}, err
	}
	downloaded, err := c.installPackage(repo, resolved)
	if err != nil {
		return InstallResult{}, err
	}
	if err := registerResourceLocked(c.PackagesDir, resolved.Resource, resolved.Version, resolved.Digest); err != nil {
		return InstallResult{}, fmt.Errorf("register resource: %w", err)
	}
	resourcePath, err := ResourcePath(c.PackagesDir, os.Getenv("DSC_RESOURCE_PATH"))
	if err != nil {
		return InstallResult{}, err
	}
	resourcePath = addPath(resourcePath, resolved.Path)
	if err := os.Setenv("DSC_RESOURCE_PATH", resourcePath); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{
		Resource: resolved.Resource, Version: resolved.Version, Package: resolved.Package,
		PackageVersion: resolved.PackageVersion, Platform: c.Platform, Path: resolved.Path,
		Downloaded: downloaded, DSCResourcePath: resourcePath,
	}, nil
}

func (c *Client) Update() ([]InstallResult, error) {
	var results []InstallResult
	err := withPackagesLock(c.PackagesDir, func() error {
		var err error
		results, err = c.update()
		return err
	})
	return results, err
}

func (c *Client) update() ([]InstallResult, error) {
	registry, err := ReadRegistry(c.PackagesDir)
	if err != nil {
		return nil, err
	}
	results := make([]InstallResult, 0, len(registry.Resources))
	resolutions := make([]packageResolution, 0, len(registry.Resources))
	for _, entry := range registry.Resources {
		resolved, repo, err := c.resolve(entry.Resource, entry.Version, "")
		if err != nil {
			return nil, err
		}
		downloaded, err := c.installPackage(repo, resolved)
		if err != nil {
			return nil, err
		}
		entry.Digest = resolved.Digest
		resolutions = append(resolutions, resolved)
		results = append(results, InstallResult{
			Resource: resolved.Resource, Version: resolved.Version, Package: resolved.Package,
			PackageVersion: resolved.PackageVersion, Platform: c.Platform, Path: resolved.Path,
			Downloaded: downloaded,
		})
	}
	for i := range registry.Resources {
		registry.Resources[i].Digest = resolutions[i].Digest
	}
	if len(registry.Resources) > 0 {
		if err := writeRegistry(c.PackagesDir, registry); err != nil {
			return nil, fmt.Errorf("update resource registry: %w", err)
		}
	}
	envPaths := make([]string, 0, len(resolutions))
	for _, resolved := range resolutions {
		envPaths = append(envPaths, resolved.Path)
	}
	resourcePath, err := ResourcePath(c.PackagesDir, os.Getenv("DSC_RESOURCE_PATH"))
	if err != nil {
		return nil, err
	}
	resourcePath, err = replacePackagePaths(resourcePath, c.PackagesDir, envPaths)
	if err != nil {
		return nil, err
	}
	if err := os.Setenv("DSC_RESOURCE_PATH", resourcePath); err != nil {
		return nil, err
	}
	for i := range results {
		results[i].DSCResourcePath = resourcePath
	}
	return results, nil
}

func (c *Client) Cleanup() ([]string, error) {
	var removed []string
	err := withPackagesLock(c.PackagesDir, func() error {
		var err error
		removed, err = c.cleanup()
		return err
	})
	return removed, err
}

func (c *Client) cleanup() ([]string, error) {
	registry, err := ReadRegistry(c.PackagesDir)
	if err != nil {
		return nil, err
	}
	required := make(map[string]bool)
	for _, entry := range registry.Resources {
		resolved, _, err := c.resolve(entry.Resource, entry.Version, "")
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(resolved.Path); err != nil {
			return nil, fmt.Errorf("required package %q is missing; run dscpkg update first", resolved.Path)
		}
		required[canonicalPath(resolved.Path)] = true
	}
	activePaths := splitPaths(os.Getenv("DSC_RESOURCE_PATH"))
	activeSet := make(map[string]bool)
	for _, item := range activePaths {
		if item != "" {
			activeSet[canonicalPath(item)] = true
		}
	}

	removed := make([]string, 0)
	root := c.PackagesDir
	namespaces, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return removed, nil
	}
	if err != nil {
		return nil, err
	}
	for _, ns := range namespaces {
		if !ns.IsDir() || strings.HasPrefix(ns.Name(), ".") {
			continue
		}
		packages, err := os.ReadDir(filepath.Join(root, ns.Name()))
		if err != nil {
			return nil, err
		}
		for _, pkg := range packages {
			if !pkg.IsDir() {
				continue
			}
			pkgPath := filepath.Join(root, ns.Name(), pkg.Name())
			versions, err := os.ReadDir(pkgPath)
			if err != nil {
				return nil, err
			}
			for _, item := range versions {
				if !item.IsDir() {
					continue
				}
				versionPath := filepath.Join(pkgPath, item.Name())
				key := canonicalPath(versionPath)
				if required[key] || activeSet[key] {
					continue
				}
				if err := os.RemoveAll(versionPath); err != nil {
					return nil, err
				}
				if err := os.Remove(installedMetadataPath(versionPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("remove package provenance: %w", err)
				}
				removed = append(removed, versionPath)
			}
		}
	}
	sort.Strings(removed)
	return removed, nil
}

func (c *Client) resolve(resource, version, requestedPackageVersion string) (packageResolution, *repository, error) {
	resource, err := normalizeResource(resource)
	if err != nil {
		return packageResolution{}, nil, err
	}
	version = strings.ToLower(strings.TrimSpace(version))
	if version != "" && !validSegment(version) {
		return packageResolution{}, nil, fmt.Errorf("invalid resource version %q", version)
	}
	if len(c.Repositories) == 0 {
		return packageResolution{}, nil, errors.New("at least one repository origin is required")
	}
	for _, origin := range c.Repositories {
		repo, err := c.discover(origin)
		if err != nil {
			if errors.Is(err, errNoDiscovery) {
				continue
			}
			return packageResolution{}, nil, err
		}
		catalog, err := c.getCatalog(repo.catalogURL)
		if err != nil {
			return packageResolution{}, nil, err
		}
		versions := []string{version}
		if version == "" {
			versions = nil
			for candidate := range catalog.Resources[resource] {
				if isStableVersion(candidate) {
					versions = append(versions, candidate)
				}
			}
			sort.Slice(versions, func(i, j int) bool { return orderVersions(versions[i], versions[j]) > 0 })
		}
		for _, candidate := range versions {
			entry, found := catalog.Resources[resource][candidate]
			if !found {
				continue
			}
			resolved, err := c.resolveEntry(repo, resource, candidate, requestedPackageVersion, entry, version == "")
			if errors.Is(err, errNoCompatiblePackage) && version == "" {
				continue
			}
			if err != nil {
				return packageResolution{}, nil, err
			}
			return resolved, repo, nil
		}
	}
	return packageResolution{}, nil, fmt.Errorf("resource %s@%s was not found with a compatible package in any configured repository", resource, version)
}

var errNoCompatiblePackage = errors.New("no compatible package version")

func (c *Client) resolveEntry(repo *repository, resource, version, requestedPackageVersion string, entry CatalogEntry, requirePlatform bool) (packageResolution, error) {
	if strings.TrimSpace(entry.URL) == "" {
		return packageResolution{}, fmt.Errorf("catalog entry for %s@%s has no descriptor URL", resource, version)
	}
	if _, _, err := parseDigest(entry.Digest); err != nil {
		return packageResolution{}, fmt.Errorf("catalog entry for %s@%s has invalid digest: %w", resource, version, err)
	}
	descriptorURL, err := resolveURL(repo.catalogURL, entry.URL)
	if err != nil {
		return packageResolution{}, err
	}
	response, err := c.getDocument(descriptorURL)
	if err != nil {
		return packageResolution{}, err
	}
	if err := verifyDigest(response.body, entry.Digest); err != nil {
		return packageResolution{}, fmt.Errorf("resource descriptor %s integrity check: %w", descriptorURL, err)
	}
	if err := c.verifyDocument(repo, namespace(resource), descriptorURL, response); err != nil {
		return packageResolution{}, fmt.Errorf("resource descriptor %s signature check: %w", descriptorURL, err)
	}
	var descriptor ResourceDescriptor
	if err := json.Unmarshal(response.body, &descriptor); err != nil {
		return packageResolution{}, fmt.Errorf("parse resource descriptor %s: %w", descriptorURL, err)
	}
	if len(descriptor.Packages) != 1 {
		return packageResolution{}, fmt.Errorf("resource descriptor for %s@%s must identify exactly one package; got %d", resource, version, len(descriptor.Packages))
	}
	var packageName string
	var packageInfo struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	for name, info := range descriptor.Packages {
		packageName, err = normalizePackage(name)
		if err != nil {
			return packageResolution{}, err
		}
		packageInfo = info
	}
	versions := make([]string, 0, len(packageInfo.Versions))
	for candidate := range packageInfo.Versions {
		if validSegment(candidate) {
			versions = append(versions, strings.ToLower(candidate))
		}
	}
	if len(versions) == 0 {
		return packageResolution{}, fmt.Errorf("package %s has no valid published versions", packageName)
	}
	sort.Slice(versions, func(i, j int) bool { return orderVersions(versions[i], versions[j]) < 0 })
	selected := versions[len(versions)-1]
	if requestedPackageVersion != "" {
		selected = strings.ToLower(requestedPackageVersion)
		found := false
		for _, candidate := range versions {
			if candidate == selected {
				found = true
			}
		}
		if !found {
			if requirePlatform {
				return packageResolution{}, errNoCompatiblePackage
			}
			return packageResolution{}, fmt.Errorf("package version %q is not available for %s", requestedPackageVersion, packageName)
		}
		versions = []string{selected}
	}
	resolved := packageResolution{
		Resource: resource, Version: version, Package: packageName, PackageVersion: selected,
		Digest: entry.Digest, Path: filepath.Join(c.PackagesDir, filepath.FromSlash(packageName), selected),
	}
	if requirePlatform {
		for i := len(versions) - 1; i >= 0; i-- {
			archive, archiveURL, err := c.packageArchive(repo, packageName, versions[i])
			if errors.Is(err, errNoCompatiblePackage) {
				continue
			}
			if err != nil {
				return packageResolution{}, err
			}
			resolved.PackageVersion = versions[i]
			resolved.Path = filepath.Join(c.PackagesDir, filepath.FromSlash(packageName), versions[i])
			resolved.archive, resolved.archiveURL = &archive, archiveURL
			return resolved, nil
		}
		return packageResolution{}, errNoCompatiblePackage
	}
	return resolved, nil
}

func (c *Client) discover(origin string) (*repository, error) {
	base, err := normalizeOrigin(origin)
	if err != nil {
		return nil, err
	}
	discoveryURL := strings.TrimRight(base, "/") + "/.well-known/dsc.json"
	response, status, err := c.request(discoveryURL, nil)
	if err != nil {
		if status == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", errNoDiscovery, origin)
		}
		return nil, err
	}
	var discovery Discovery
	if err := json.Unmarshal(response.body, &discovery); err != nil {
		return nil, fmt.Errorf("parse discovery document %s: %w", discoveryURL, err)
	}
	if discovery.Catalog == "" || discovery.Resources == "" || discovery.Packages == "" {
		return nil, fmt.Errorf("discovery document %s must contain catalog.v1, resources.v1, and packages.v1", discoveryURL)
	}
	catalogURL, err := resolveURL(discoveryURL, discovery.Catalog)
	if err != nil {
		return nil, err
	}
	discovery.Resources, err = resolveURL(discoveryURL, discovery.Resources)
	if err != nil {
		return nil, err
	}
	discovery.Packages, err = resolveURL(discoveryURL, discovery.Packages)
	if err != nil {
		return nil, err
	}
	discovery.Catalog = catalogURL
	repo := &repository{origin: base, discovery: discovery, catalogURL: catalogURL, policies: map[string]Policy{}}
	if discovery.SigningPolicies != "" {
		policyURL, err := resolveURL(discoveryURL, discovery.SigningPolicies)
		if err != nil {
			return nil, err
		}
		policyResponse, err := c.getDocument(policyURL)
		if err != nil {
			return nil, fmt.Errorf("fetch signing policies: %w", err)
		}
		var doc policyDocument
		if err := json.Unmarshal(policyResponse.body, &doc); err != nil {
			var policies []Policy
			if arrayErr := json.Unmarshal(policyResponse.body, &policies); arrayErr != nil {
				return nil, fmt.Errorf("parse signing policies: %w", err)
			}
			doc.Policies = policies
		}
		if doc.Policies == nil {
			return nil, errors.New("signing policy document must contain a policies array")
		}
		for _, policy := range doc.Policies {
			ns := strings.ToLower(strings.TrimSpace(policy.Namespace))
			if !validSegment(ns) {
				return nil, fmt.Errorf("invalid signing policy namespace %q", policy.Namespace)
			}
			policy.Namespace = ns
			repo.policies[ns] = policy
		}
	}
	for ns, policy := range bundledPolicies {
		repo.policies[ns] = policy
	}
	return repo, nil
}

func (c *Client) getCatalog(catalogURL string) (Catalog, error) {
	cache, err := c.readCatalogCache()
	if err != nil {
		return Catalog{}, fmt.Errorf("read catalog cache: %w", err)
	}
	entry, cached := cache[catalogURL]
	headers := make(http.Header)
	if cached && entry.ETag != "" {
		headers.Set("If-None-Match", entry.ETag)
	}
	request, err := http.NewRequest(http.MethodGet, catalogURL, nil)
	if err != nil {
		return Catalog{}, err
	}
	request.Header = headers
	response, err := c.httpClient().Do(request)
	if err != nil {
		return Catalog{}, fmt.Errorf("fetch catalog %s: %w", catalogURL, err)
	}
	defer response.Body.Close()
	var body []byte
	switch response.StatusCode {
	case http.StatusNotModified:
		if !cached {
			return Catalog{}, fmt.Errorf("catalog %s returned 304 without a cached body", catalogURL)
		}
		body = entry.Body
	case http.StatusOK:
		body, err = io.ReadAll(io.LimitReader(response.Body, maxDocumentSize+1))
		if err != nil {
			return Catalog{}, err
		}
		if len(body) > maxDocumentSize {
			return Catalog{}, fmt.Errorf("catalog %s exceeds size limit", catalogURL)
		}
		cache[catalogURL] = catalogCache{ETag: response.Header.Get("ETag"), Body: body}
		if err := c.writeCatalogCache(cache); err != nil {
			return Catalog{}, fmt.Errorf("cache catalog: %w", err)
		}
	default:
		return Catalog{}, fmt.Errorf("fetch catalog %s: HTTP %s", catalogURL, response.Status)
	}
	var catalog Catalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return Catalog{}, fmt.Errorf("parse catalog %s: %w", catalogURL, err)
	}
	if catalog.Resources == nil {
		return Catalog{}, fmt.Errorf("catalog %s does not contain a resources object", catalogURL)
	}
	return catalog, nil
}

func (c *Client) readCatalogCache() (map[string]catalogCache, error) {
	result := make(map[string]catalogCache)
	data, err := os.ReadFile(filepath.Join(c.PackagesDir, ".dscpkg", "catalog-cache.json"))
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return make(map[string]catalogCache), nil
	}
	return result, nil
}

func (c *Client) writeCatalogCache(cache map[string]catalogCache) error {
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(c.PackagesDir, ".dscpkg", "catalog-cache.json"), data, 0o600)
}

func (c *Client) getDocument(uri string) (document, error) {
	response, status, err := c.request(uri, nil)
	if err != nil {
		return document{}, err
	}
	if status != http.StatusOK {
		return document{}, fmt.Errorf("GET %s: HTTP %d", uri, status)
	}
	return response, nil
}

func (c *Client) request(uri string, headers http.Header) (document, int, error) {
	request, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return document{}, 0, err
	}
	request.Header.Set("Accept", "application/json")
	if headers != nil {
		request.Header = headers.Clone()
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return document{}, 0, fmt.Errorf("GET %s: %w", uri, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDocumentSize+1))
	if err != nil {
		return document{}, response.StatusCode, err
	}
	if len(body) > maxDocumentSize {
		return document{}, response.StatusCode, fmt.Errorf("response from %s exceeds size limit", uri)
	}
	if response.StatusCode != http.StatusOK {
		return document{}, response.StatusCode, fmt.Errorf("GET %s: HTTP %s", uri, response.Status)
	}
	return document{body: body, headers: response.Header.Clone()}, response.StatusCode, nil
}

func (c *Client) verifyDocument(repo *repository, ns, uri string, response document) error {
	policy, hasPolicy := repo.policies[ns]
	if !hasPolicy {
		return nil
	}
	signature := response.headers.Get("content-signature")
	if signature == "" {
		signature = response.headers.Get("x-ms-meta-content-signature")
	}
	if signature == "" {
		signature = response.headers.Get("x-azm-meta-content-signature")
	}
	if signature == "" {
		sidecar, _, err := c.request(uri+".jws", nil)
		if err == nil {
			signature = strings.TrimSpace(string(sidecar.body))
		} else if !strings.Contains(err.Error(), "HTTP 404") {
			return fmt.Errorf("fetch detached signature sidecar: %w", err)
		}
	}
	if signature == "" && policy.Required {
		return fmt.Errorf("namespace %s requires a detached JWS (checked content-signature, x-ms-meta-content-signature, x-azm-meta-content-signature, and %s.jws)", ns, uri)
	}
	if signature == "" {
		return nil
	}
	if err := verifyDetachedJWS(signature, response.body, policy.Keys); err != nil {
		return fmt.Errorf("namespace %s: %w", ns, err)
	}
	return nil
}

func normalizeOrigin(origin string) (string, error) {
	origin = strings.TrimSpace(origin)
	if !strings.Contains(origin, "://") {
		origin = "https://" + strings.TrimRight(origin, "/")
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid repository origin %q", origin)
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func verifyDigest(data []byte, digest string) error {
	algorithm, expected, err := parseDigest(digest)
	if err != nil {
		return err
	}
	var actual []byte
	switch algorithm {
	case "sha1":
		sum := sha1.Sum(data)
		actual = sum[:]
	case "sha256":
		sum := sha256.Sum256(data)
		actual = sum[:]
	case "sha384":
		sum := sha512.Sum384(data)
		actual = sum[:]
	case "sha512":
		sum := sha512.Sum512(data)
		actual = sum[:]
	default:
		return fmt.Errorf("unsupported digest algorithm %q", algorithm)
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("%s digest mismatch", algorithm)
	}
	return nil
}

func parseDigest(value string) (string, []byte, error) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(value)), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", nil, fmt.Errorf("invalid digest %q; expected algorithm:hex", value)
	}
	digest, err := hex.DecodeString(parts[1])
	if err != nil {
		return "", nil, fmt.Errorf("invalid digest hex: %w", err)
	}
	expectedLength := map[string]int{"sha1": 20, "sha256": 32, "sha384": 48, "sha512": 64}
	if length, ok := expectedLength[parts[0]]; !ok || len(digest) != length {
		return "", nil, fmt.Errorf("unsupported or malformed digest algorithm %q", parts[0])
	}
	return parts[0], digest, nil
}

func compareVersions(a, b string) int {
	prefixA, preA, _ := strings.Cut(a, "-")
	prefixB, preB, _ := strings.Cut(b, "-")
	partsA, partsB := strings.Split(prefixA, "."), strings.Split(prefixB, ".")
	count := len(partsA)
	if len(partsB) > count {
		count = len(partsB)
	}
	for i := 0; i < count; i++ {
		av, bv := "0", "0"
		if i < len(partsA) {
			av = partsA[i]
		}
		if i < len(partsB) {
			bv = partsB[i]
		}
		if compared := compareVersionPart(av, bv); compared != 0 {
			return compared
		}
	}
	if preA == "" && preB != "" {
		return 1
	}
	if preA != "" && preB == "" {
		return -1
	}
	aParts, bParts := strings.Split(preA, "."), strings.Split(preB, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if compared := compareVersionPart(aParts[i], bParts[i]); compared != 0 {
			return compared
		}
	}
	if len(aParts) < len(bParts) {
		return -1
	}
	if len(aParts) > len(bParts) {
		return 1
	}
	return 0
}

func orderVersions(a, b string) int {
	if compared := compareVersions(a, b); compared != 0 {
		return compared
	}
	return strings.Compare(a, b)
}

func numericVersionPart(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func compareVersionPart(a, b string) int {
	an, bn := numericVersionPart(a), numericVersionPart(b)
	if an && bn {
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(a) < len(b) {
			return -1
		}
		if len(a) > len(b) {
			return 1
		}
	} else if an != bn {
		if an {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func isStableVersion(value string) bool {
	if !validSegment(value) {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if !numericVersionPart(part) {
			return false
		}
	}
	return true
}

func packageDocumentURL(endpoint, packageName, version string) (string, error) {
	name, err := normalizePackage(packageName)
	if err != nil {
		return "", err
	}
	version = strings.ToLower(version)
	if !validSegment(version) {
		return "", fmt.Errorf("invalid package version %q", version)
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	base.Path = path.Join(base.Path, name, version+".json")
	return base.String(), nil
}

func currentPlatform() string {
	arch := runtime.GOARCH
	return runtime.GOOS + "_" + arch
}
