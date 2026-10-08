package dscpkg

import (
	"archive/zip"
	"crypto/sha256"
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
	"strings"
	"time"
)

type PublishOptions struct {
	Repository     string
	Package        string
	PackageVersion string
	Platform       string
	Archive        string
	ArchiveURL     string
	Resources      []string
}

type publishResourceData struct {
	name    string
	version string
}

type publishArchive struct {
	path   string
	hash   string
	url    string
	local  bool
	remove func()
}

// Publish writes repository metadata and, for a local archive, the archive itself.
func Publish(options PublishOptions) error {
	packageName, err := normalizePackage(options.Package)
	if err != nil {
		return err
	}
	packageVersion, err := normalizePublishVersion("package", options.PackageVersion)
	if err != nil {
		return err
	}
	platform, err := normalizePlatform(options.Platform)
	if err != nil {
		return err
	}
	if (options.Archive == "") == (options.ArchiveURL == "") {
		return errors.New("exactly one of --archive or --archive-url is required")
	}
	if len(options.Resources) == 0 {
		return errors.New("at least one --resource is required")
	}
	resources := make([]publishResourceData, 0, len(options.Resources))
	seenResources := make(map[string]bool)
	for _, identity := range options.Resources {
		name, version, found := strings.Cut(identity, "@")
		if !found || strings.Contains(version, "@") {
			return fmt.Errorf("invalid resource %q: expected Namespace/name@version", identity)
		}
		normalizedName, err := normalizeResource(name)
		if err != nil {
			return err
		}
		normalizedVersion, err := normalizePublishVersion("resource", version)
		if err != nil {
			return err
		}
		key := normalizedName + "\x00" + normalizedVersion
		if !seenResources[key] {
			resources = append(resources, publishResourceData{name: normalizedName, version: normalizedVersion})
			seenResources[key] = true
		}
	}

	archive, err := preparePublishArchive(options)
	if err != nil {
		return err
	}
	if archive.remove != nil {
		defer archive.remove()
	}

	root, err := filepath.Abs(options.Repository)
	if err != nil {
		return fmt.Errorf("resolve repository directory: %w", err)
	}
	if err := checkRepositoryRoot(root); err != nil {
		return err
	}

	discovery, catalog, err := loadPublishMetadata(root)
	if err != nil {
		return err
	}
	policies, err := publishPolicies(root, discovery)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if policy, ok := policies[namespace(resource.name)]; ok && policy.Required {
			return fmt.Errorf("cannot publish %s: namespace %s requires signed metadata; dscpkg publish does not support signing", resource.name, namespace(resource.name))
		}
	}
	if policy, ok := policies[namespace(packageName)]; ok && policy.Required {
		return fmt.Errorf("cannot publish package %s: namespace %s requires signed metadata; dscpkg publish does not support signing", packageName, namespace(packageName))
	}

	packagePath := path.Join("v1/packages", packageName, packageVersion+".json")
	packageBytes, packageChanged, err := updatedPackageDescriptor(root, packagePath, packageVersion, platform, archive)
	if err != nil {
		return err
	}
	if packageChanged {
		if err := rejectExistingSignature(root, packagePath); err != nil {
			return err
		}
	}

	resourceWrites := make([]struct {
		path string
		data []byte
	}, 0, len(resources))
	for _, resource := range resources {
		data, err := updatedResourceDescriptor(root, catalog, resource, packageName, packageVersion)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		digestHex := hex.EncodeToString(digest[:])
		descriptorPath := path.Join("v1/resources", resource.name, resource.version+"."+digestHex+".json")
		catalogEntry := CatalogEntry{URL: strings.TrimPrefix(descriptorPath, "v1/"), Digest: "sha256:" + digestHex}
		if catalog.Resources == nil {
			catalog.Resources = make(map[string]map[string]CatalogEntry)
		}
		if catalog.Resources[resource.name] == nil {
			catalog.Resources[resource.name] = make(map[string]CatalogEntry)
		}
		catalog.Resources[resource.name][resource.version] = catalogEntry
		resourceWrites = append(resourceWrites, struct {
			path string
			data []byte
		}{path: descriptorPath, data: data})
	}
	catalogBytes, err := marshalPublishJSON(catalog)
	if err != nil {
		return fmt.Errorf("serialize catalog: %w", err)
	}
	if !bytesEqualFile(root, "v1/catalog.json", catalogBytes) {
		if err := rejectExistingSignature(root, "v1/catalog.json"); err != nil {
			return err
		}
	}
	discoveryBytes, err := marshalPublishJSON(discovery)
	if err != nil {
		return fmt.Errorf("serialize discovery document: %w", err)
	}
	writeDiscovery := false
	if _, err := readRepositoryFile(root, ".well-known/dsc.json"); errors.Is(err, os.ErrNotExist) {
		writeDiscovery = true
	} else if err != nil {
		return fmt.Errorf("inspect discovery document: %w", err)
	}

	// Validate every destination before the first published file is written.
	if err := prepareRepositoryDirectories(root, packagePath, resourceWrites); err != nil {
		return err
	}
	if err := ensureLocalArchive(root, packageName, packageVersion, platform, archive); err != nil {
		return err
	}
	if packageChanged {
		if err := writePublishFile(root, packagePath, packageBytes); err != nil {
			return fmt.Errorf("write package descriptor: %w", err)
		}
	}
	for _, item := range resourceWrites {
		if err := writePublishFile(root, item.path, item.data); err != nil {
			return fmt.Errorf("write resource descriptor %s: %w", item.path, err)
		}
	}
	if writeDiscovery {
		if err := writePublishFile(root, ".well-known/dsc.json", discoveryBytes); err != nil {
			return fmt.Errorf("write discovery document: %w", err)
		}
	}
	if !bytesEqualFile(root, "v1/catalog.json", catalogBytes) {
		if err := writePublishFile(root, "v1/catalog.json", catalogBytes); err != nil {
			return fmt.Errorf("publish catalog: %w", err)
		}
	}
	return nil
}

func normalizePublishVersion(kind, value string) (string, error) {
	version := strings.ToLower(strings.TrimSpace(value))
	if !validSegment(version) {
		return "", fmt.Errorf("invalid %s version %q", kind, value)
	}
	return version, nil
}

func normalizePlatform(value string) (string, error) {
	platform := strings.ToLower(strings.TrimSpace(value))
	osName, architecture, ok := strings.Cut(platform, "_")
	if !ok || strings.Contains(architecture, "_") || !validSegment(osName) || !validSegment(architecture) {
		return "", fmt.Errorf("invalid platform %q: expected os_arch", value)
	}
	return platform, nil
}

func preparePublishArchive(options PublishOptions) (publishArchive, error) {
	if options.Archive != "" {
		info, err := os.Lstat(options.Archive)
		if err != nil {
			return publishArchive{}, fmt.Errorf("open archive %q: %w", options.Archive, err)
		}
		if !info.Mode().IsRegular() {
			return publishArchive{}, fmt.Errorf("archive %q must be a regular file", options.Archive)
		}
		archive, err := inspectPublishArchive(options.Archive)
		if err != nil {
			return publishArchive{}, fmt.Errorf("validate archive %q: %w", options.Archive, err)
		}
		archive.local = true
		return archive, nil
	}

	if strings.TrimSpace(options.ArchiveURL) != options.ArchiveURL {
		return publishArchive{}, fmt.Errorf("invalid archive URL %q: surrounding whitespace is not allowed", options.ArchiveURL)
	}
	parsed, err := url.Parse(options.ArchiveURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return publishArchive{}, fmt.Errorf("invalid archive URL %q: expected an HTTP(S) URL without credentials", options.ArchiveURL)
	}
	temp, err := os.CreateTemp("", "dscpkg-publish-*.zip")
	if err != nil {
		return publishArchive{}, fmt.Errorf("create temporary archive: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	client := &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if (request.URL.Scheme != "http" && request.URL.Scheme != "https") || request.URL.User != nil {
				return fmt.Errorf("redirect to unsupported URL scheme %q", request.URL.Scheme)
			}
			if len(via) >= 10 {
				return errors.New("too many archive redirects")
			}
			return nil
		},
	}
	request, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		_ = temp.Close()
		cleanup()
		return publishArchive{}, fmt.Errorf("download archive %q: %w", options.ArchiveURL, err)
	}
	response, err := client.Do(request)
	if err != nil {
		_ = temp.Close()
		cleanup()
		return publishArchive{}, fmt.Errorf("download archive %q: %w", options.ArchiveURL, err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		_ = temp.Close()
		cleanup()
		return publishArchive{}, fmt.Errorf("download archive %q: HTTP %s", options.ArchiveURL, response.Status)
	}
	written, copyErr := io.Copy(temp, io.LimitReader(response.Body, maxArchiveSize+1))
	bodyCloseErr := response.Body.Close()
	if copyErr == nil && bodyCloseErr != nil {
		copyErr = bodyCloseErr
	}
	if copyErr == nil && written > maxArchiveSize {
		copyErr = fmt.Errorf("archive exceeds %d-byte size limit", maxArchiveSize)
	}
	if copyErr == nil {
		copyErr = temp.Sync()
	}
	if closeErr := temp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		cleanup()
		return publishArchive{}, fmt.Errorf("download archive %q: %w", options.ArchiveURL, copyErr)
	}
	archive, err := inspectPublishArchive(tempPath)
	if err != nil {
		cleanup()
		return publishArchive{}, fmt.Errorf("validate downloaded archive %q: %w", options.ArchiveURL, err)
	}
	archive.url = options.ArchiveURL
	archive.remove = cleanup
	return archive, nil
}

func inspectPublishArchive(filePath string) (publishArchive, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return publishArchive{}, err
	}
	if info.Size() > maxArchiveSize {
		return publishArchive{}, fmt.Errorf("archive exceeds %d-byte size limit", maxArchiveSize)
	}
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return publishArchive{}, fmt.Errorf("invalid ZIP archive: %w", err)
	}
	seen := make(map[string]bool, len(reader.File))
	var expanded uint64
	for _, entry := range reader.File {
		name := entry.Name
		localName := filepath.FromSlash(name)
		if strings.Contains(name, `\`) || !filepath.IsLocal(localName) {
			_ = reader.Close()
			return publishArchive{}, fmt.Errorf("unsafe archive path %q", name)
		}
		clean := filepath.Clean(localName)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			_ = reader.Close()
			return publishArchive{}, fmt.Errorf("unsafe archive path %q", name)
		}
		key := strings.ToLower(clean)
		if seen[key] {
			_ = reader.Close()
			return publishArchive{}, fmt.Errorf("duplicate archive path %q", name)
		}
		seen[key] = true
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			_ = reader.Close()
			return publishArchive{}, fmt.Errorf("unsupported file type in archive entry %q", name)
		}
		if entry.UncompressedSize64 > maxExpandedSize-expanded {
			_ = reader.Close()
			return publishArchive{}, errors.New("archive exceeds expanded size limit")
		}
		expanded += entry.UncompressedSize64
		if mode.IsDir() || strings.HasSuffix(name, "/") {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			_ = reader.Close()
			return publishArchive{}, fmt.Errorf("open ZIP entry %q: %w", name, err)
		}
		_, copyErr := io.Copy(io.Discard, file)
		closeErr := file.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			_ = reader.Close()
			return publishArchive{}, fmt.Errorf("validate ZIP entry %q: %w", name, copyErr)
		}
	}
	if err := reader.Close(); err != nil {
		return publishArchive{}, err
	}
	file, err := os.Open(filePath)
	if err != nil {
		return publishArchive{}, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return publishArchive{}, copyErr
	}
	return publishArchive{path: filePath, hash: hex.EncodeToString(hash.Sum(nil))}, nil
}

func checkRepositoryRoot(root string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect repository directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("repository path %q must be a real directory", root)
	}
	return nil
}

func loadPublishMetadata(root string) (Discovery, Catalog, error) {
	discovery := Discovery{
		Catalog:   "../v1/catalog.json",
		Resources: "../v1/resources",
		Packages:  "../v1/packages",
	}
	catalog := Catalog{Resources: make(map[string]map[string]CatalogEntry)}
	discoveryData, err := readRepositoryFile(root, ".well-known/dsc.json")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Discovery{}, Catalog{}, fmt.Errorf("read discovery document: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(discoveryData, &discovery); err != nil {
			return Discovery{}, Catalog{}, fmt.Errorf("parse discovery document: %w", err)
		}
		if discovery.Catalog == "" || discovery.Resources == "" || discovery.Packages == "" {
			return Discovery{}, Catalog{}, errors.New("discovery document must contain catalog.v1, resources.v1, and packages.v1")
		}
	}
	catalogData, err := readRepositoryFile(root, "v1/catalog.json")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Discovery{}, Catalog{}, fmt.Errorf("read catalog: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(catalogData, &catalog); err != nil {
			return Discovery{}, Catalog{}, fmt.Errorf("parse catalog: %w", err)
		}
		if catalog.Resources == nil {
			return Discovery{}, Catalog{}, errors.New("catalog must contain a resources object")
		}
	}
	return discovery, catalog, nil
}

func publishPolicies(root string, discovery Discovery) (map[string]Policy, error) {
	result := make(map[string]Policy)
	if discovery.SigningPolicies == "" {
		return result, nil
	}
	parsed, err := url.Parse(discovery.SigningPolicies)
	if err != nil || parsed.IsAbs() || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("cannot inspect configured remote signing policies while publishing; use a local relative signing-policy URL")
	}
	policyPath := path.Clean(path.Join(".well-known", parsed.Path))
	if policyPath == ".." || strings.HasPrefix(policyPath, "../") {
		return nil, errors.New("signing policy URL escapes repository root")
	}
	data, err := readRepositoryFile(root, policyPath)
	if err != nil {
		return nil, fmt.Errorf("read configured signing policies: %w", err)
	}
	var doc policyDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		var policies []Policy
		if arrayErr := json.Unmarshal(data, &policies); arrayErr != nil {
			return nil, fmt.Errorf("parse signing policies: %w", err)
		}
		doc.Policies = policies
	}
	for _, policy := range doc.Policies {
		ns := strings.ToLower(strings.TrimSpace(policy.Namespace))
		if !validSegment(ns) {
			return nil, fmt.Errorf("invalid signing policy namespace %q", policy.Namespace)
		}
		policy.Namespace = ns
		result[ns] = policy
	}
	return result, nil
}

func updatedPackageDescriptor(root, relative, packageVersion, platform string, archive publishArchive) ([]byte, bool, error) {
	descriptor := PackageDescriptor{Archives: make(map[string]Archive)}
	data, err := readRepositoryFile(root, relative)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("read package descriptor: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &descriptor); err != nil {
			return nil, false, fmt.Errorf("parse package descriptor: %w", err)
		}
		if descriptor.Archives == nil {
			descriptor.Archives = make(map[string]Archive)
		}
	}
	archiveURL := archive.url
	if archive.local {
		archiveURL = path.Base(path.Dir(relative)) + "_" + packageVersion + "_" + platform + ".zip"
	}
	newArchive := Archive{URL: archiveURL, Hashes: []string{"sha256:" + archive.hash}}
	if previous, exists := descriptor.Archives[platform]; exists {
		oldDigest, oldOK := sha256ArchiveHash(previous.Hashes)
		if previous.URL != archiveURL || !oldOK || oldDigest != archive.hash {
			return nil, false, fmt.Errorf("conflicting immutable package artifact for platform %s", platform)
		}
		return data, false, nil
	}
	descriptor.Archives[platform] = newArchive
	encoded, err := marshalPublishJSON(descriptor)
	if err != nil {
		return nil, false, err
	}
	return encoded, true, nil
}

func sha256ArchiveHash(hashes []string) (string, bool) {
	for _, value := range hashes {
		algorithm, digest, err := splitArchiveHash(value)
		if err == nil && algorithm == "sha256" {
			return digest, true
		}
	}
	return "", false
}

func updatedResourceDescriptor(root string, catalog Catalog, resource publishResourceData, packageName, packageVersion string) ([]byte, error) {
	document := make(map[string]json.RawMessage)
	if entries := catalog.Resources[resource.name]; entries != nil {
		if entry, exists := entries[resource.version]; exists {
			relative, err := localCatalogPath(entry.URL)
			if err != nil {
				return nil, fmt.Errorf("cannot update %s@%s: %w", resource.name, resource.version, err)
			}
			old, err := readRepositoryFile(root, relative)
			if err != nil {
				return nil, fmt.Errorf("read resource descriptor for %s@%s: %w", resource.name, resource.version, err)
			}
			if err := verifyDigest(old, entry.Digest); err != nil {
				return nil, fmt.Errorf("existing resource descriptor for %s@%s: %w", resource.name, resource.version, err)
			}
			if err := json.Unmarshal(old, &document); err != nil {
				return nil, fmt.Errorf("parse resource descriptor for %s@%s: %w", resource.name, resource.version, err)
			}
		}
	}
	packages := make(map[string]json.RawMessage)
	if raw, exists := document["packages"]; exists {
		if err := json.Unmarshal(raw, &packages); err != nil {
			return nil, fmt.Errorf("parse packages in resource descriptor %s@%s: %w", resource.name, resource.version, err)
		}
	}
	provider := ""
	providerKey := ""
	for name := range packages {
		normalized, err := normalizePackage(name)
		if err != nil {
			return nil, fmt.Errorf("invalid package provider %q in resource descriptor", name)
		}
		if provider != "" && provider != normalized {
			return nil, fmt.Errorf("resource descriptor for %s@%s identifies multiple package providers", resource.name, resource.version)
		}
		provider = normalized
		providerKey = name
	}
	if provider != "" && provider != packageName {
		return nil, fmt.Errorf("resource %s@%s is already provided by %s; cannot change provider to %s", resource.name, resource.version, provider, packageName)
	}
	providerData := make(map[string]json.RawMessage)
	if provider != "" {
		if err := json.Unmarshal(packages[providerKey], &providerData); err != nil {
			return nil, fmt.Errorf("parse package provider descriptor: %w", err)
		}
		delete(packages, providerKey)
	}
	versions := make(map[string]json.RawMessage)
	if raw, exists := providerData["versions"]; exists {
		if err := json.Unmarshal(raw, &versions); err != nil {
			return nil, fmt.Errorf("parse resource versions: %w", err)
		}
	}
	if versions == nil {
		versions = make(map[string]json.RawMessage)
	}
	versions[packageVersion] = json.RawMessage("{}")
	versionBytes, err := json.Marshal(versions)
	if err != nil {
		return nil, err
	}
	providerData["versions"] = versionBytes
	providerBytes, err := json.Marshal(providerData)
	if err != nil {
		return nil, err
	}
	packages[packageName] = providerBytes
	packagesBytes, err := json.Marshal(packages)
	if err != nil {
		return nil, err
	}
	document["packages"] = packagesBytes
	return marshalPublishJSON(document)
}

func localCatalogPath(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(value, `\`) {
		return "", errors.New("resource descriptor URL is not a local relative path")
	}
	clean := path.Clean(path.Join("v1", parsed.Path))
	if clean == "v1" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || !strings.HasPrefix(clean, "v1/resources/") {
		return "", errors.New("resource descriptor URL escapes the local resources directory")
	}
	return clean, nil
}

func marshalPublishJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func readRepositoryFile(root, relative string) ([]byte, error) {
	target, err := checkedRepositoryPath(root, relative, false)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(target)
}

func checkedRepositoryPath(root, relative string, createDirs bool) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if !filepath.IsLocal(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe repository path %q", relative)
	}
	if err := checkRepositoryRoot(root); err != nil {
		return "", err
	}
	current := root
	parts := strings.Split(clean, string(filepath.Separator))
	for _, segment := range parts[:len(parts)-1] {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && createDirs {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("unsafe repository directory %q", current)
		}
	}
	target := filepath.Join(root, clean)
	info, err := os.Lstat(target)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", fmt.Errorf("unsafe repository file %q", target)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return target, nil
}

func prepareRepositoryDirectories(root, packagePath string, resources []struct {
	path string
	data []byte
}) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create repository directory: %w", err)
	}
	if err := checkRepositoryRoot(root); err != nil {
		return err
	}
	for _, relative := range []string{".well-known/dsc.json", packagePath, "v1/catalog.json"} {
		if _, err := checkedRepositoryPath(root, relative, true); err != nil {
			return err
		}
	}
	for _, item := range resources {
		if _, err := checkedRepositoryPath(root, item.path, true); err != nil {
			return err
		}
	}
	return nil
}

func ensureLocalArchive(root, packageName, version, platform string, archive publishArchive) error {
	if !archive.local {
		return nil
	}
	filename := path.Join("v1/packages", packageName, path.Base(packageName)+"_"+version+"_"+platform+".zip")
	destination, err := checkedRepositoryPath(root, filename, true)
	if err != nil {
		return err
	}
	if existing, err := os.Open(destination); err == nil {
		info, statErr := existing.Stat()
		if statErr != nil {
			_ = existing.Close()
			return statErr
		}
		if !info.Mode().IsRegular() {
			_ = existing.Close()
			return fmt.Errorf("unsafe existing archive %q", destination)
		}
		hash := sha256.New()
		_, readErr := io.Copy(hash, existing)
		closeErr := existing.Close()
		if readErr == nil {
			readErr = closeErr
		}
		if readErr != nil {
			return readErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != archive.hash {
			return fmt.Errorf("conflicting archive already exists at %s", filename)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	source, err := os.Open(archive.path)
	if err != nil {
		return err
	}
	defer source.Close()
	temp, err := os.CreateTemp(filepath.Dir(destination), ".dscpkg-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), source)
	if copyErr == nil && written > maxArchiveSize {
		copyErr = fmt.Errorf("archive exceeds %d-byte size limit", maxArchiveSize)
	}
	if copyErr == nil && hex.EncodeToString(hash.Sum(nil)) != archive.hash {
		copyErr = errors.New("local archive changed after validation")
	}
	if copyErr == nil {
		copyErr = temp.Sync()
	}
	if closeErr := temp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return copyErr
	}
	if err := os.Rename(tempName, destination); err != nil {
		return fmt.Errorf("publish archive: %w", err)
	}
	return nil
}

func writePublishFile(root, relative string, data []byte) error {
	target, err := checkedRepositoryPath(root, relative, true)
	if err != nil {
		return err
	}
	if bytesEqualFile(root, relative, data) {
		return nil
	}
	return atomicWrite(target, data, 0o644)
}

func bytesEqualFile(root, relative string, expected []byte) bool {
	actual, err := readRepositoryFile(root, relative)
	return err == nil && string(actual) == string(expected)
}

func rejectExistingSignature(root, relative string) error {
	signaturePath := relative + ".jws"
	if _, err := readRepositoryFile(root, signaturePath); err == nil {
		return fmt.Errorf("cannot update %s because detached signature %s would be invalidated; signing is not supported", relative, signaturePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect signature for %s: %w", relative, err)
	}
	return nil
}
