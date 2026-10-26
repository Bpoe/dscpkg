package dscpkg

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	for _, pair := range [][2]string{
		{"1.9.0", "1.10.0"}, {"1.0.0-rc.9", "1.0.0-rc.10"},
		{"1.0.0-rc.10", "1.0.0"}, {"1.0.0-alpha", "1.0.0-alpha.1"},
		{"1.0.0-1", "1.0.0-alpha"}, {"9", "10"},
		{"99999999999999999999", "100000000000000000000"},
	} {
		if compareVersions(pair[0], pair[1]) >= 0 || compareVersions(pair[1], pair[0]) <= 0 {
			t.Fatalf("incorrect precedence: %q < %q", pair[0], pair[1])
		}
	}
	for _, version := range []string{"1", "1.2", "1.10.0"} {
		if !isStableVersion(version) || compareVersions(version, version) != 0 {
			t.Fatalf("invalid stable version handling: %q", version)
		}
	}
	for _, version := range []string{"1.0.0-preview", "latest", "1..0", "../1"} {
		if isStableVersion(version) {
			t.Fatalf("non-stable version accepted: %q", version)
		}
	}
}

func publishResourceFixture(t *testing.T, repository, pkg, packageVersion, resourceVersion, platform string, resources ...string) {
	t.Helper()
	identities := make([]manifestIdentity, 0, len(resources))
	refs := make([]string, 0, len(resources))
	for _, resource := range resources {
		identities = append(identities, manifestIdentity{Type: resource, Version: resourceVersion})
		refs = append(refs, resource+"@"+resourceVersion)
	}
	data, err := json.Marshal(manifestIdentity{Resources: identities})
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "package.zip")
	if err := os.WriteFile(archive, makeZip(t, "shared.dsc.manifests.json", data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Publish(PublishOptions{
		Repository: repository, Package: pkg, PackageVersion: packageVersion,
		Platform: platform, Archive: archive, Resources: refs,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLatestCompatibleResolution(t *testing.T) {
	t.Setenv("DSC_RESOURCE_PATH", "")
	primary, secondary := t.TempDir(), t.TempDir()
	for _, version := range []string{"1.9.0", "1.10.0", "2.0.0-preview", "3.0.0"} {
		platform := "linux_amd64"
		if version == "3.0.0" {
			platform = "windows_amd64"
		}
		publishResourceFixture(t, primary, testPackage, version, version, platform, testResourceOne)
	}
	// The newest package can be incompatible independently of the resource version.
	publishResourceFixture(t, primary, testPackage, "8.0.0", "1.10.0", "windows_amd64", testResourceOne)
	publishResourceFixture(t, secondary, testPackage, "9.0.0", "9.0.0", "linux_amd64", testResourceOne)
	publishResourceFixture(t, secondary, testPackage, "9.0.0", "9.0.0", "darwin_arm64", testResourceOne)
	first := httptest.NewServer(http.FileServer(http.Dir(primary)))
	defer first.Close()
	second := httptest.NewServer(http.FileServer(http.Dir(secondary)))
	defer second.Close()
	for _, test := range []struct{ version, pin, resourceVersion, packageVersion string }{
		{"", "", "1.10.0", "1.10.0"},
		{"", "1.9.0", "1.9.0", "1.9.0"},
		{"1.9.0", "", "1.9.0", "1.9.0"},
		{"2.0.0-preview", "", "2.0.0-preview", "2.0.0-preview"},
	} {
		t.Run(test.version+"/"+test.pin, func(t *testing.T) {
			client := NewClient(t.TempDir(), []string{first.URL, second.URL})
			client.Platform = "linux_amd64"
			result, err := client.Install(testResourceOne, test.version, test.pin)
			if err != nil {
				t.Fatal(err)
			}
			if result.Version != test.resourceVersion || result.PackageVersion != test.packageVersion {
				t.Fatalf("wrong resolved versions: %+v", result)
			}
		})
	}
	client := NewClient(t.TempDir(), []string{first.URL, second.URL})
	client.Platform = "freebsd_amd64"
	if _, err := client.Install(testResourceOne, "", ""); err == nil {
		t.Fatal("unsupported platforms must not install")
	}
	client.Platform = "linux_amd64"
	if _, err := client.Install(testResourceTwo, "", ""); err == nil {
		t.Fatal("missing resource must fail")
	}
	// Once the first repository has only incompatible candidates, use the next one.
	client.Platform = "darwin_arm64"
	result, err := client.Install(testResourceOne, "", "")
	if err != nil || result.Version != "9.0.0" {
		t.Fatalf("repository precedence: %+v %v", result, err)
	}
}

func TestLatestResolutionErrorsDoNotFallBack(t *testing.T) {
	for _, failure := range []string{"catalog", "digest", "signature", "archive signature"} {
		t.Run(failure, func(t *testing.T) {
			repo := newTestRepository(t)
			repo.discovery()
			repo.set("/v1/policies.json", []byte(`{"policies":[]}`), nil)
			descriptor := []byte(`{"packages":{"example.test/shared":{"versions":{"4.0.0":{}}}}}`)
			entry := CatalogEntry{URL: "/descriptor.json", Digest: "sha256:" + digest(descriptor)}
			repo.set("/descriptor.json", descriptor, nil)
			switch failure {
			case "digest":
				entry.Digest = "sha256:" + strings.Repeat("0", 64)
			case "signature", "archive signature":
				key, policy := testSigningPolicy(t)
				policies, _ := json.Marshal(policyDocument{Policies: []Policy{policy}})
				repo.set("/v1/policies.json", policies, nil)
				if failure == "archive signature" {
					signed := signedAsset(t, key, descriptor)
					repo.set("/descriptor.json", signed.body, signed.headers)
					repo.set("/v1/packages/example.test/shared/4.0.0.json", []byte(`{"archives":{}}`), nil)
				}
			}
			catalog, _ := json.Marshal(Catalog{Resources: map[string]map[string]CatalogEntry{
				testResourceOne: {"1.0.0": entry},
			}})
			if failure == "catalog" {
				catalog = []byte("{")
			}
			repo.set("/v1/catalog.json", catalog, nil)
			var fallbackCalls atomic.Int64
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackCalls.Add(1)
				http.NotFound(w, r)
			}))
			defer fallback.Close()
			client := NewClient(t.TempDir(), []string{repo.server.URL, fallback.URL})
			if _, err := client.Install(testResourceOne, "", ""); err == nil || fallbackCalls.Load() != 0 {
				t.Fatalf("failed metadata must not fall back: err=%v fallback requests=%d", err, fallbackCalls.Load())
			}
		})
	}
}

func TestResourceStateAndSharedSet(t *testing.T) {
	t.Setenv("DSC_RESOURCE_PATH", "")
	repository := t.TempDir()
	publishResourceFixture(t, repository, testPackage, "4.0.0", "1.0.0", currentPlatform(), testResourceOne, testResourceTwo)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.FileServer(http.Dir(repository)).ServeHTTP(w, r)
	}))
	defer server.Close()
	client := NewClient(filepath.Join(t.TempDir(), "missing"), nil)
	desired := ResourceState{Resource: testResourceOne, Repositories: []string{server.URL}}
	actual, differences, err := client.TestResource(desired, io.Discard)
	if err != nil || actual.State != "missing" || !reflect.DeepEqual(differences, []string{"state"}) {
		t.Fatalf("missing: %+v %v %v", actual, differences, err)
	}
	if _, err := os.Stat(client.PackagesDir); !os.IsNotExist(err) {
		t.Fatal("Test created the package store")
	}
	actual, err = client.SetResource(desired, io.Discard)
	if err != nil || actual.Version != "1.0.0" || actual.PackageVersion != "4.0.0" {
		t.Fatalf("Set: %+v %v", actual, err)
	}
	initialRequests := requests.Load()
	before := discoverySnapshot(t, client.PackagesDir)
	for _, resource := range []string{testResourceOne, testResourceTwo} {
		for _, version := range []string{"", "1.0.0"} {
			desired.Resource, desired.Version = resource, version
			state, diff, err := client.TestResource(desired, io.Discard)
			if err != nil || len(diff) != 0 || !*state.InDesiredState {
				t.Fatalf("shared resource compliance: %+v %v %v", state, diff, err)
			}
			if _, err := client.SetResource(desired, io.Discard); err != nil {
				t.Fatal(err)
			}
		}
	}
	if requests.Load() != initialRequests {
		t.Fatal("compliant operations accessed the network")
	}
	registry, err := ReadRegistry(client.PackagesDir)
	if err != nil || len(registry.Resources) != 1 {
		t.Fatalf("shared Set must not register a second resource: %+v %v", registry, err)
	}
	server.Close()
	if _, err := client.GetResource(desired, io.Discard); err != nil {
		t.Fatalf("offline Get: %v", err)
	}
	if _, err := client.SetResource(desired, io.Discard); err != nil {
		t.Fatalf("offline compliant Set: %v", err)
	}
	if after := discoverySnapshot(t, client.PackagesDir); !reflect.DeepEqual(before, after) {
		t.Fatal("Get/Test/compliant Set mutated the store")
	}
	desired.Version = "2.0.0"
	actual, diff, err := client.TestResource(desired, io.Discard)
	if err != nil || actual.State != "missing" || !reflect.DeepEqual(diff, []string{"state", "version"}) {
		t.Fatalf("wrong version: %+v %v %v", actual, diff, err)
	}
	desired.State = "missing"
	if _, err := client.SetResource(desired, io.Discard); err == nil {
		t.Fatal("Set must reject removal")
	}
}

func TestResourceExplicitSetAndNoAutomaticUpgrade(t *testing.T) {
	t.Setenv("DSC_RESOURCE_PATH", "")
	repository := t.TempDir()
	for _, version := range []string{"1.0.0", "2.0.0"} {
		publishResourceFixture(t, repository, testPackage, version, version, currentPlatform(), testResourceOne)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(repository)))
	defer server.Close()
	client := NewClient(t.TempDir(), []string{server.URL})
	desired := ResourceState{Resource: testResourceOne, Version: "1.0.0"}
	if _, err := client.SetResource(desired, io.Discard); err != nil {
		t.Fatal(err)
	}
	desired.Version = ""
	actual, err := client.SetResource(desired, io.Discard)
	if err != nil || actual.Version != "1.0.0" {
		t.Fatalf("unversioned Set upgraded installed resource: %+v %v", actual, err)
	}
	desired.Version = "2.0.0"
	if _, err := client.SetResource(desired, io.Discard); err != nil {
		t.Fatal(err)
	}
	actual, err = client.GetResource(ResourceState{Resource: testResourceOne}, io.Discard)
	if err != nil || actual.Version != "2.0.0" {
		t.Fatalf("newest installed version: %+v %v", actual, err)
	}
	states, err := client.InstalledResources(io.Discard)
	if err != nil || len(states) != 2 {
		t.Fatalf("Set deleted older versions: %+v %v", states, err)
	}
}

func TestResourceInventoryFormsAndPlatform(t *testing.T) {
	root := t.TempDir()
	for _, pkg := range []struct{ name, platform string }{
		{"example/a/1", currentPlatform()}, {"example/z/2", currentPlatform()},
		{"example/foreign/1", "other_arch"}, {"example/legacy/1", ""},
	} {
		directory := discoveryPackage(t, root, pkg.name, pkg.platform)
		discoveryFile(t, filepath.Join(directory, "one.dsc.resource.json"), `{"type":"Example/One","version":"1.0.0"}`)
		if pkg.name != "example/a/1" {
			continue
		}
		discoveryFile(t, filepath.Join(directory, "two.dsc.resource.yaml"), "type: Example/Two\nversion: 1.0.0\nschema: !embedded {}\n")
		discoveryFile(t, filepath.Join(directory, "three.dsc.resource.yml"), "type: Example/Three\nversion: 1.0.0\n")
		discoveryFile(t, filepath.Join(directory, "list.dsc.manifests.json"), `{"resources":[{"type":"Example/Four","version":"2.0.0"},{"type":"Example/One","version":"1.0.0"},{"type":"Example/Adapted","version":"1.0.0","requireAdapter":"Example/Adapter"}]}`)
		discoveryFile(t, filepath.Join(directory, "list.dsc.manifests.yaml"), "resources:\n  - type: Example/Five\n    version: 1.0.0\n")
		discoveryFile(t, filepath.Join(directory, "adapted.dsc.adaptedresource.json"), `{"type":"Example/Adapted","version":"1.0.0","requireAdapter":"Example/Adapter"}`)
	}
	client := NewClient(root, nil)
	before := discoverySnapshot(t, root)
	var diagnostics bytes.Buffer
	states, err := client.InstalledResources(&diagnostics)
	if err != nil || len(states) != 5 {
		t.Fatalf("inventory: %+v %v", states, err)
	}
	if !strings.Contains(diagnostics.String(), "adapter") || !strings.Contains(diagnostics.String(), "reinstall") {
		t.Fatalf("missing limitations diagnostics: %s", &diagnostics)
	}
	for i := range 3 {
		repeated, err := client.InstalledResources(io.Discard)
		if err != nil || !reflect.DeepEqual(repeated, states) {
			t.Fatalf("nondeterministic inventory %d: %+v %v", i, repeated, err)
		}
	}
	if after := discoverySnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("inventory modified filesystem state")
	}
	if states[2].Resource != "Example/One" || states[2].Package != "example/z" {
		t.Fatalf("duplicate selection should prefer newer package: %+v", states)
	}
	discoveryFile(t, filepath.Join(root, "example", "a", ".1.dscpkg.json"), "{")
	if _, err := client.InstalledResources(io.Discard); err == nil {
		t.Fatal("corrupt provenance was hidden")
	}
}

func TestResourceMalformedManifestsAndFinalState(t *testing.T) {
	for _, fixture := range []struct{ name, content string }{
		{"bad.dsc.resource.json", "{"}, {"bad.dsc.resource.yaml", ":"},
		{"bad.dsc.resource.json", "{}"}, {"bad.dsc.manifests.json", `{"type":"Example/NotAnInstance","version":"1.0.0"}`},
	} {
		t.Run(fixture.name+fixture.content, func(t *testing.T) {
			root := t.TempDir()
			dir := discoveryPackage(t, root, "example/shared/1", currentPlatform())
			discoveryFile(t, filepath.Join(dir, fixture.name), fixture.content)
			if _, err := NewClient(root, nil).InstalledResources(io.Discard); err == nil {
				t.Fatal("malformed manifest accepted")
			}
		})
	}
	t.Setenv("DSC_RESOURCE_PATH", "")
	repository := t.TempDir()
	// The catalog advertises One, but the archive only contains Two.
	publishResourceFixture(t, repository, testPackage, "1.0.0", "1.0.0", currentPlatform(), testResourceTwo)
	catalogPath := filepath.Join(repository, "v1", "catalog.json")
	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var catalog Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	catalog.Resources[testResourceOne] = catalog.Resources[testResourceTwo]
	data, err = json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	discoveryFile(t, catalogPath, string(data))
	server := httptest.NewServer(http.FileServer(http.Dir(repository)))
	defer server.Close()
	client := NewClient(t.TempDir(), []string{server.URL})
	if _, err := client.SetResource(ResourceState{Resource: testResourceOne}, io.Discard); err == nil || !strings.Contains(err.Error(), "does not expose") {
		t.Fatalf("Set must verify discoverability after installation: %v", err)
	}
	_, err = client.SetResource(ResourceState{Resource: "invalid"}, io.Discard)
	if err == nil {
		t.Fatal("invalid resource identity accepted")
	}
}
