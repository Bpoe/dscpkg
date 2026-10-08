package dscpkg

import (
	"archive/zip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testResourceOne = "example.test/one"
const testResourceTwo = "example.test/two"
const testPackage = "example.test/shared"

type responseAsset struct {
	body    []byte
	headers http.Header
}

type testRepository struct {
	server          *httptest.Server
	mu              sync.Mutex
	assets          map[string]responseAsset
	catalogGets     int
	conditionalGets int
	seenValidators  []string
}

func newTestRepository(t *testing.T) *testRepository {
	t.Helper()
	repository := &testRepository{assets: make(map[string]responseAsset)}
	repository.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repository.mu.Lock()
		defer repository.mu.Unlock()
		if r.URL.Path == "/v1/catalog.json" {
			repository.catalogGets++
			repository.seenValidators = append(repository.seenValidators, r.Header.Get("If-None-Match"))
		}
		asset, ok := repository.assets[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		for key, values := range asset.headers {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if etag := asset.headers.Get("ETag"); etag != "" && r.Header.Get("If-None-Match") == etag {
			repository.conditionalGets++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(asset.body)
	}))
	t.Cleanup(repository.server.Close)
	return repository
}

func (r *testRepository) set(path string, body []byte, headers http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	normalized := make(http.Header)
	for key, values := range headers {
		for _, value := range values {
			normalized.Add(key, value)
		}
	}
	r.assets[path] = responseAsset{body: append([]byte(nil), body...), headers: normalized}
}

func (r *testRepository) counts() (int, int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.catalogGets, r.conditionalGets, append([]string(nil), r.seenValidators...)
}

func (r *testRepository) discovery() {
	body := []byte(fmt.Sprintf(`{"catalog.v1":%q,"resources.v1":%q,"packages.v1":%q,"signing-policies.v1":%q}`,
		r.server.URL+"/v1/catalog.json", r.server.URL+"/v1/resources",
		r.server.URL+"/v1/packages", r.server.URL+"/v1/policies.json"))
	r.set("/.well-known/dsc.json", body, nil)
}

func TestInstallPrecedenceSigningSharingUpdateAndCleanup(t *testing.T) {
	oldEnv := os.Getenv("DSC_RESOURCE_PATH")
	t.Cleanup(func() { _ = os.Setenv("DSC_RESOURCE_PATH", oldEnv) })
	_ = os.Unsetenv("DSC_RESOURCE_PATH")

	repo := newTestRepository(t)
	repo.discovery()
	privateKey, policy := testSigningPolicy(t)
	policyDoc, _ := json.Marshal(map[string]any{"policies": []Policy{policy}})
	repo.set("/v1/policies.json", policyDoc, nil)

	archiveV1 := makeZip(t, "resource.json", []byte("version one"))
	archiveV2 := makeZip(t, "resource.json", []byte("version two"))
	packageInfoV1 := signedAsset(t, privateKey, []byte(fmt.Sprintf(
		`{"archives":{"linux_amd64":{"url":"/archives/v1.zip","hashes":[%q]}}}`,
		"sha256:"+digest(archiveV1))))
	packageInfoV2 := signedAsset(t, privateKey, []byte(fmt.Sprintf(
		`{"archives":{"linux_amd64":{"url":"/archives/v2.zip","hashes":[%q]}}}`,
		"sha256:"+digest(archiveV2))))
	repo.set("/v1/packages/example.test/shared/1.2.0.json", packageInfoV1.body, packageInfoV1.headers)
	repo.set("/v1/packages/example.test/shared/2.0.0.json", packageInfoV2.body, packageInfoV2.headers)
	repo.set("/archives/v1.zip", archiveV1, nil)
	repo.set("/archives/v2.zip", archiveV2, nil)

	descriptor1 := []byte(`{"packages":{"example.test/shared":{"versions":{"1.0.0":{},"1.2.0":{}}}}}`)
	descriptor2 := []byte(`{"packages":{"example.test/shared":{"versions":{"1.0.0":{},"1.2.0":{}}}}}`)
	setSignedDescriptor(t, repo, privateKey, testResourceOne, descriptor1)
	setSignedDescriptor(t, repo, privateKey, testResourceTwo, descriptor2)
	initialCatalog := catalogBody(map[string][]byte{testResourceOne: descriptor1, testResourceTwo: descriptor2})
	repo.set("/v1/catalog.json", initialCatalog, http.Header{"ETag": []string{`"v1"`}})

	fallback := newTestRepository(t)
	fallback.discovery()
	fallback.set("/v1/catalog.json", catalogBody(map[string][]byte{testResourceOne: descriptor1}), http.Header{"ETag": []string{`"fallback"`}})

	cacheDir := t.TempDir()
	client := NewClient(cacheDir, []string{repo.server.URL, fallback.server.URL})
	client.Platform = "linux_amd64"
	first, err := client.Install(testResourceOne, "2026-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Downloaded || first.PackageVersion != "1.2.0" {
		t.Fatalf("unexpected initial install result: %+v", first)
	}
	second, err := client.Install(testResourceTwo, "2026-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Downloaded || second.Path != first.Path {
		t.Fatalf("expected shared cached package, got %+v", second)
	}
	fallbackCatalogGets, _, _ := fallback.counts()
	if fallbackCatalogGets != 0 {
		t.Fatalf("lower-precedence repository was queried after a catalog match: %d", fallbackCatalogGets)
	}
	registry, err := ReadRegistry(cacheDir)
	if err != nil || len(registry.Resources) != 2 {
		t.Fatalf("resource registration failed: %+v, %v", registry, err)
	}

	updates, err := client.Update()
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 || updates[0].Downloaded || updates[1].Downloaded {
		t.Fatalf("unchanged update should reuse the shared package: %+v", updates)
	}
	gets, conditional, validators := repo.counts()
	if conditional == 0 {
		cache, _ := os.ReadFile(filepath.Join(cacheDir, ".dscpkg", "catalog-cache.json"))
		t.Fatalf("expected an If-None-Match catalog request, got %d catalog requests and %d conditional requests; validators=%q cache=%s", gets, conditional, validators, cache)
	}

	descriptorV2 := []byte(`{"packages":{"example.test/shared":{"versions":{"2.0.0":{}}}}}`)
	setSignedDescriptor(t, repo, privateKey, testResourceOne, descriptorV2)
	setSignedDescriptor(t, repo, privateKey, testResourceTwo, descriptorV2)
	repo.set("/v1/catalog.json", catalogBody(map[string][]byte{testResourceOne: descriptorV2, testResourceTwo: descriptorV2}), http.Header{"ETag": []string{`"v2"`}})
	updates, err = client.Update()
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 || !updates[0].Downloaded || updates[1].Downloaded || updates[0].Path != updates[1].Path {
		t.Fatalf("expected one download for newly shared package: %+v", updates)
	}
	registry, err = ReadRegistry(cacheDir)
	if err != nil || registry.Resources[0].Digest == "" {
		t.Fatalf("updated descriptor digest was not recorded: %+v, %v", registry, err)
	}
	orphan := filepath.Join(cacheDir, "example.test", "unused", "9.9.9")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	removed, err := client.Cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("cleanup should remove old and orphan versions; removed %v", removed)
	}
	if _, err := os.Stat(updates[0].Path); err != nil {
		t.Fatalf("cleanup removed current shared package: %v", err)
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatalf("cleanup retained obsolete package version: %v", err)
	}
}

func TestDigestFailureDoesNotFallBack(t *testing.T) {
	primary := newTestRepository(t)
	primary.discovery()
	primary.set("/v1/policies.json", []byte(`{"policies":[]}`), nil)
	descriptor := []byte(`{"packages":{"example.test/shared":{"versions":{"1.0.0":{}}}}}`)
	primary.set("/v1/catalog.json", []byte(fmt.Sprintf(
		`{"resources":{"%s":{"2026-01-01":{"url":"/descriptor.json","digest":"sha256:%s"}}}}`,
		testResourceOne, strings.Repeat("0", 64))), http.Header{"ETag": []string{`"bad"`}})
	primary.set("/descriptor.json", descriptor, nil)
	fallback := newTestRepository(t)
	fallback.discovery()
	fallback.set("/v1/policies.json", []byte(`{"policies":[]}`), nil)
	fallback.set("/v1/catalog.json", catalogBody(map[string][]byte{testResourceOne: descriptor}), nil)
	client := NewClient(t.TempDir(), []string{primary.server.URL, fallback.server.URL})
	client.Platform = "linux_amd64"
	if _, err := client.Install(testResourceOne, "2026-01-01", ""); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected catalog digest failure, got %v", err)
	}
	fallbackGets, _, _ := fallback.counts()
	if fallbackGets != 0 {
		t.Fatalf("integrity failure incorrectly fell back to later repository: %d", fallbackGets)
	}
}

func TestRequiredSignatureRejectsTampering(t *testing.T) {
	repo := newTestRepository(t)
	repo.discovery()
	privateKey, policy := testSigningPolicy(t)
	policyDoc, _ := json.Marshal(map[string]any{"policies": []Policy{policy}})
	repo.set("/v1/policies.json", policyDoc, nil)
	descriptor := []byte(`{"packages":{"example.test/shared":{"versions":{"1.0.0":{}}}}}`)
	signed := signedAsset(t, privateKey, descriptor)
	signed.headers.Set("Content-Signature", "invalid")
	repo.set("/v1/resources/example.test/one/2026-01-01.json", descriptor, signed.headers)
	repo.set("/v1/catalog.json", catalogBody(map[string][]byte{testResourceOne: descriptor}), nil)
	client := NewClient(t.TempDir(), []string{repo.server.URL})
	client.Platform = "linux_amd64"
	if _, err := client.Install(testResourceOne, "2026-01-01", ""); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected signature verification failure, got %v", err)
	}
}

func TestRelativeDiscoveryEndpointsAndSigningNamespace(t *testing.T) {
	repo := newTestRepository(t)
	repo.set("/.well-known/dsc.json", []byte(`{
		"catalog.v1":"../v1/catalog.json",
		"resources.v1":"../v1/resources",
		"packages.v1":"../v1/packages",
		"signing-policies.v1":"../v1/policies.json"
	}`), nil)
	repo.set("/v1/policies.json", []byte(`{"policies":[{"namespace":" example.test ","required":true}]}`), nil)

	client := NewClient(t.TempDir(), []string{repo.server.URL})
	discovered, err := client.discover(repo.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if discovered.discovery.Resources != repo.server.URL+"/v1/resources" ||
		discovered.discovery.Packages != repo.server.URL+"/v1/packages" ||
		discovered.catalogURL != repo.server.URL+"/v1/catalog.json" {
		t.Fatalf("discovery endpoints were not resolved: %+v", discovered.discovery)
	}
	if policy, ok := discovered.policies["example.test"]; !ok || !policy.Required {
		t.Fatalf("trimmed signing namespace was not applied: %+v", discovered.policies)
	}

	repo.set("/v1/policies.json", []byte(`{"policies":[{"namespace":"example.test/other","required":true}]}`), nil)
	if _, err := client.discover(repo.server.URL); err == nil {
		t.Fatal("expected a multi-segment signing namespace to be rejected")
	}
}

func TestCachedPackageDoesNotFetchPackageDescriptor(t *testing.T) {
	repo := newTestRepository(t)
	repo.discovery()
	repo.set("/v1/policies.json", []byte(`{"policies":[]}`), nil)
	descriptor := []byte(`{"packages":{"example.test/shared":{"versions":{"1.0.0":{}}}}}`)
	repo.set("/v1/resources/example.test/one/2026-01-01.json", descriptor, nil)
	repo.set("/v1/resources/example.test/two/2026-01-01.json", descriptor, nil)
	repo.set("/v1/catalog.json", catalogBody(map[string][]byte{
		testResourceOne: descriptor,
		testResourceTwo: descriptor,
	}), nil)
	archive := makeZip(t, "resource.json", []byte("content"))
	repo.set("/v1/packages/example.test/shared/1.0.0.json",
		[]byte(fmt.Sprintf(`{"archives":{"linux_amd64":{"url":"/archive.zip","hashes":[%q]}}}`, "sha256:"+digest(archive))), nil)
	repo.set("/archive.zip", archive, nil)

	packagesDir := t.TempDir()
	client := NewClient(packagesDir, []string{repo.server.URL})
	client.Platform = "linux_amd64"
	if _, err := client.Install(testResourceOne, "2026-01-01", ""); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	delete(repo.assets, "/v1/packages/example.test/shared/1.0.0.json")
	repo.mu.Unlock()

	result, err := client.Install(testResourceTwo, "2026-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloaded {
		t.Fatalf("expected the cached package to be reused: %+v", result)
	}
}

func TestArchiveHashMismatchPreventsInstallation(t *testing.T) {
	repo := newTestRepository(t)
	repo.discovery()
	repo.set("/v1/policies.json", []byte(`{"policies":[]}`), nil)
	descriptor := []byte(`{"packages":{"example.test/shared":{"versions":{"1.0.0":{}}}}}`)
	repo.set("/v1/resources/example.test/one/2026-01-01.json", descriptor, nil)
	repo.set("/v1/catalog.json", catalogBody(map[string][]byte{testResourceOne: descriptor}), nil)
	repo.set("/v1/packages/example.test/shared/1.0.0.json",
		[]byte(fmt.Sprintf(`{"archives":{"linux_amd64":{"url":"/archive.zip","hashes":[%q]}}}`, "sha256:"+strings.Repeat("0", 64))), nil)
	repo.set("/archive.zip", makeZip(t, "resource.json", []byte("content")), nil)
	packagesDir := t.TempDir()
	client := NewClient(packagesDir, []string{repo.server.URL})
	client.Platform = "linux_amd64"
	if _, err := client.Install(testResourceOne, "2026-01-01", ""); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("expected archive hash verification failure, got %v", err)
	}
	installed := filepath.Join(packagesDir, "example.test", "shared", "1.0.0")
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("hash-mismatched package was installed: %v", err)
	}
	if _, err := ReadRegistry(packagesDir); err != nil {
		t.Fatalf("registry should remain readable: %v", err)
	}
}

func TestSafeExtractionRejectsZipSlip(t *testing.T) {
	archive := makeZip(t, "../escape.txt", []byte("unsafe"))
	destination := t.TempDir()
	if err := extractZip(writeTemp(t, archive), destination); err == nil {
		t.Fatal("expected zip-slip path to be rejected")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(destination), "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("archive wrote outside extraction directory: %v", err)
	}
}

func TestRemoveResourceByResourceAndVersion(t *testing.T) {
	root := t.TempDir()
	if err := registerResource(root, testResourceOne, "v1", "sha256:abc"); err != nil {
		t.Fatal(err)
	}
	if err := registerResource(root, testResourceOne, "v2", "sha256:def"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveResource(root, testResourceOne, "v1"); err != nil {
		t.Fatal(err)
	}
	registry, err := ReadRegistry(root)
	if err != nil || len(registry.Resources) != 1 || registry.Resources[0].Version != "v2" {
		t.Fatalf("remove did not preserve the other version: %+v, %v", registry, err)
	}
}

func TestConcurrentResourceRegistrations(t *testing.T) {
	root := t.TempDir()
	const count = 20
	var wg sync.WaitGroup
	errors := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resource := fmt.Sprintf("example.test/resource-%d", i)
			errors <- registerResource(root, resource, "v1", "sha256:abc")
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	registry, err := ReadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Resources) != count {
		t.Fatalf("concurrent registrations lost updates: got %d, want %d", len(registry.Resources), count)
	}
}

func TestResourcePathSkipsStagingDirectories(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "example.test", "shared", "1.0.0")
	staging := filepath.Join(root, "example.test", "shared", ".dscpkg-install-staging")
	for _, dir := range []string{installed, staging} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	result, err := ResourcePath(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if result != installed {
		t.Fatalf("resource path includes staging directory: %q", result)
	}
}

func setSignedDescriptor(t *testing.T, repo *testRepository, key *ecdsa.PrivateKey, resource string, body []byte) {
	t.Helper()
	signature := signedAsset(t, key, body)
	resourcePath := strings.Split(resource, "/")
	repo.set("/v1/resources/"+resourcePath[0]+"/"+resourcePath[1]+"/2026-01-01.json", body, signature.headers)
}

func catalogBody(descriptors map[string][]byte) []byte {
	resources := make(map[string]map[string]CatalogEntry)
	for resource, body := range descriptors {
		resources[resource] = map[string]CatalogEntry{"2026-01-01": {
			URL: "/v1/resources/" + resource + "/2026-01-01.json", Digest: "sha256:" + digest(body),
		}}
	}
	body, _ := json.Marshal(Catalog{Resources: resources})
	return body
}

func testSigningPolicy(t *testing.T) (*ecdsa.PrivateKey, Policy) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{Namespace: "example.test", Required: true, Keys: []JWK{{
		Kid: "test-key", Kty: "EC", Crv: "P-256",
		X: base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))),
		Y: base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32))),
	}}}
	return key, policy
}

func signedAsset(t *testing.T, key *ecdsa.PrivateKey, payload []byte) responseAsset {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","kid":"test-key"}`))
	input := []byte(header + "." + base64.RawURLEncoding.EncodeToString(payload))
	sum := sha256.Sum256(input)
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	rawSignature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	compact := header + ".." + base64.RawURLEncoding.EncodeToString(rawSignature)
	return responseAsset{body: payload, headers: http.Header{"Content-Signature": []string{compact}}}
}

func makeZip(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buffer strings.Builder
	writer := zip.NewWriter(stringWriter{&buffer})
	entry, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return []byte(buffer.String())
}

type stringWriter struct{ builder *strings.Builder }

func (w stringWriter) Write(value []byte) (int, error) {
	return w.builder.Write(value)
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "*.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return file.Name()
}
