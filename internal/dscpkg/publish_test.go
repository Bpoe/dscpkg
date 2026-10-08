package dscpkg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const publishPackage = "example/resources"
const publishResource = "example/users"

func TestPublishInitializesAndIsIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "wwwroot")
	archive := filepath.Join(t.TempDir(), "resources.zip")
	zipBytes := makeZip(t, "users.dsc.resource.json", []byte(`{"type":"example/users"}`))
	if err := os.WriteFile(archive, zipBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	options := PublishOptions{
		Repository: root, Package: publishPackage, PackageVersion: "1.0.0",
		Platform: "linux_amd64", Archive: archive,
		Resources: []string{"example/users@1.0.0", "example/groups@2.0.0"},
	}
	if err := Publish(options); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(root, ".well-known", "dsc.json"), `{
  "catalog.v1": "../v1/catalog.json",
  "resources.v1": "../v1/resources",
  "packages.v1": "../v1/packages"
}
`)
	var packageDescriptor PackageDescriptor
	readJSONFile(t, filepath.Join(root, "v1/packages/example/resources/1.0.0.json"), &packageDescriptor)
	sum := sha256.Sum256(zipBytes)
	archiveEntry := packageDescriptor.Archives["linux_amd64"]
	if archiveEntry.URL != "resources_1.0.0_linux_amd64.zip" || len(archiveEntry.Hashes) != 1 || archiveEntry.Hashes[0] != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected package archive descriptor: %+v", archiveEntry)
	}
	catalog := readCatalogFile(t, root)
	if len(catalog.Resources) != 2 {
		t.Fatalf("catalog resources = %v, want two published resources", catalog.Resources)
	}
	for resource, versions := range catalog.Resources {
		for version, entry := range versions {
			resourcePath, err := localCatalogPath(entry.URL)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(resourcePath)))
			if err != nil {
				t.Fatal(err)
			}
			if entry.Digest != "sha256:"+digest(body) {
				t.Fatalf("catalog digest mismatch for %s@%s", resource, version)
			}
			if !strings.Contains(filepath.Base(resourcePath), "."+digest(body)+".json") {
				t.Fatalf("descriptor path %q does not contain full digest %s", resourcePath, digest(body))
			}
		}
	}
	states := repositoryFileStates(t, root)
	if err := Publish(options); err != nil {
		t.Fatalf("identical publication failed: %v", err)
	}
	assertRepositoryFileStates(t, root, states)
	if _, err := os.Stat(filepath.Join(root, "v1/packages/example/resources/resources_1.0.0_linux_amd64.zip")); err != nil {
		t.Fatalf("published archive missing: %v", err)
	}
}

func TestPublishMergesVersionsPlatformsAndPreservesExistingRepository(t *testing.T) {
	root := t.TempDir()
	discovery := []byte(`{"catalog.v1":"../v1/catalog.json","resources.v1":"../v1/resources","packages.v1":"../v1/packages","signing-policies.v1":"../v1/policies.json","custom":"preserve"}
`)
	if err := os.MkdirAll(filepath.Join(root, ".well-known"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".well-known/dsc.json"), discovery, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	policies, _ := json.Marshal(policyDocument{Policies: []Policy{{Namespace: "unrelated", Required: false}}})
	if err := os.WriteFile(filepath.Join(root, "v1/policies.json"), policies, 0o644); err != nil {
		t.Fatal(err)
	}
	initial := Catalog{Resources: map[string]map[string]CatalogEntry{
		"other/resource": {"9.0": {URL: "resources/other/resource/9.0.old.json", Digest: "sha256:" + strings.Repeat("a", 64)}},
	}}
	initialBytes, _ := marshalPublishJSON(initial)
	if err := os.WriteFile(filepath.Join(root, "v1/catalog.json"), initialBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	archive1 := writePublishTestArchive(t, makeZip(t, "one", []byte("one")))
	archive2 := writePublishTestArchive(t, makeZip(t, "two", []byte("two")))
	for _, options := range []PublishOptions{
		{Repository: root, Package: publishPackage, PackageVersion: "1.0.0", Platform: "linux_amd64", Archive: archive1, Resources: []string{"example/users@1.0.0"}},
		{Repository: root, Package: publishPackage, PackageVersion: "1.0.0", Platform: "windows_amd64", Archive: archive2, Resources: []string{"example/users@1.0.0", "example/groups@2.0.0"}},
		{Repository: root, Package: publishPackage, PackageVersion: "2.0.0", Platform: "linux_amd64", Archive: archive2, Resources: []string{"example/users@1.0.0"}},
	} {
		if err := Publish(options); err != nil {
			t.Fatal(err)
		}
	}
	updatedDiscovery, err := os.ReadFile(filepath.Join(root, ".well-known/dsc.json"))
	if err != nil || string(updatedDiscovery) != string(discovery) {
		t.Fatalf("existing discovery configuration changed: %s, %v", updatedDiscovery, err)
	}
	updated := readCatalogFile(t, root)
	if _, ok := updated.Resources["other/resource"]["9.0"]; !ok {
		t.Fatal("unrelated catalog resource was lost")
	}
	if _, ok := updated.Resources[publishResource]["1.0.0"]; !ok {
		t.Fatal("resource's first version was lost")
	}
	var descriptor PackageDescriptor
	readJSONFile(t, filepath.Join(root, "v1/packages/example/resources/1.0.0.json"), &descriptor)
	if len(descriptor.Archives) != 2 {
		t.Fatalf("platforms were not merged: %+v", descriptor.Archives)
	}
	var resourceDescriptor ResourceDescriptor
	entry := updated.Resources[publishResource]["1.0.0"]
	resourcePath, _ := localCatalogPath(entry.URL)
	readJSONFile(t, filepath.Join(root, filepath.FromSlash(resourcePath)), &resourceDescriptor)
	versions := resourceDescriptor.Packages[publishPackage].Versions
	if len(versions) != 2 {
		t.Fatalf("resource package versions were not preserved: %+v", versions)
	}
}

func TestPublishRejectsImmutableAndProviderConflicts(t *testing.T) {
	root := t.TempDir()
	first := writePublishTestArchive(t, makeZip(t, "entry", []byte("first")))
	second := writePublishTestArchive(t, makeZip(t, "entry", []byte("second")))
	options := PublishOptions{
		Repository: root, Package: publishPackage, PackageVersion: "1.0.0",
		Platform: "linux_amd64", Archive: first, Resources: []string{"example/users@1.0.0"},
	}
	if err := Publish(options); err != nil {
		t.Fatal(err)
	}
	conflict := options
	conflict.Archive = second
	if err := Publish(conflict); err == nil || !strings.Contains(err.Error(), "conflicting immutable") {
		t.Fatalf("expected immutable artifact conflict, got %v", err)
	}
	providerConflict := options
	providerConflict.Package = "elsewhere/resources"
	if err := Publish(providerConflict); err == nil || !strings.Contains(err.Error(), "already provided") {
		t.Fatalf("expected provider conflict, got %v", err)
	}
}

func TestPublishRemoteRedirectAndInstallUnderURLPrefix(t *testing.T) {
	archiveBytes := makeZip(t, "users.dsc.resource.json", []byte(`{"type":"example/users"}`))
	archiveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/asset.zip", http.StatusFound)
			return
		}
		_, _ = w.Write(archiveBytes)
	}))
	defer archiveServer.Close()
	root := t.TempDir()
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	t.Setenv("TMP", tempDir)
	t.Setenv("TEMP", tempDir)
	originalURL := archiveServer.URL + "/redirect"
	options := PublishOptions{
		Repository: root, Package: publishPackage, PackageVersion: "1.0.0",
		Platform: "linux_amd64", ArchiveURL: originalURL, Resources: []string{"example/users@1.0.0"},
	}
	if err := Publish(options); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(tempDir); err != nil || len(entries) != 0 {
		t.Fatalf("temporary download was not removed: entries=%v err=%v", entries, err)
	}
	var packageDescriptor PackageDescriptor
	readJSONFile(t, filepath.Join(root, "v1/packages/example/resources/1.0.0.json"), &packageDescriptor)
	remoteEntry := packageDescriptor.Archives["linux_amd64"]
	if remoteEntry.URL != originalURL || remoteEntry.Hashes[0] != "sha256:"+digest(archiveBytes) {
		t.Fatalf("unexpected remote archive metadata: %+v", remoteEntry)
	}
	if _, err := os.Stat(filepath.Join(root, "v1/packages/example/resources/resources_1.0.0_linux_amd64.zip")); !os.IsNotExist(err) {
		t.Fatalf("remote ZIP unexpectedly copied into repository: %v", err)
	}

	const prefix = "/dsc-resources/"
	server := httptest.NewServer(http.StripPrefix(prefix, http.FileServer(http.Dir(root))))
	defer server.Close()
	client := NewClient(t.TempDir(), []string{server.URL + strings.TrimSuffix(prefix, "/")})
	client.Platform = "linux_amd64"
	result, err := client.Install(publishResource, "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Package != publishPackage || !result.Downloaded {
		t.Fatalf("unexpected install result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "users.dsc.resource.json")); err != nil {
		t.Fatalf("resource archive was not extracted: %v", err)
	}
	registry, err := ReadRegistry(client.PackagesDir)
	if err != nil || len(registry.Resources) != 1 || registry.Resources[0].Resource != publishResource {
		t.Fatalf("resource was not registered: %+v, %v", registry, err)
	}
}

func TestPublishRemoteFailuresLeaveRepositoryUntouched(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{name: "http failure", handler: func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}, want: "HTTP 503"},
		{name: "invalid ZIP", handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not a ZIP")) }, want: "invalid ZIP"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tempDir := t.TempDir()
			t.Setenv("TMPDIR", tempDir)
			t.Setenv("TMP", tempDir)
			t.Setenv("TEMP", tempDir)
			server := httptest.NewServer(test.handler)
			defer server.Close()
			root := filepath.Join(t.TempDir(), "missing-repository")
			err := Publish(PublishOptions{
				Repository: root, Package: publishPackage, PackageVersion: "1.0.0",
				Platform: "linux_amd64", ArchiveURL: server.URL,
				Resources: []string{"example/users@1.0.0"},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want text %q", err, test.want)
			}
			if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
				t.Fatalf("failed archive validation modified repository: %v", statErr)
			}
			if entries, readErr := os.ReadDir(tempDir); readErr != nil || len(entries) != 0 {
				t.Fatalf("failed download left temporary files: entries=%v err=%v", entries, readErr)
			}
		})
	}
}

func TestPublishValidationAndSigningLimitations(t *testing.T) {
	archive := writePublishTestArchive(t, makeZip(t, "entry", []byte("content")))
	root := t.TempDir()
	base := PublishOptions{
		Repository: root, Package: publishPackage, PackageVersion: "1.0.0",
		Platform: "linux_amd64", Archive: archive, Resources: []string{"example/users@1.0.0"},
	}
	for _, change := range []func(*PublishOptions){
		func(o *PublishOptions) { o.Package = "../resources" },
		func(o *PublishOptions) { o.Platform = "linux/../../etc" },
		func(o *PublishOptions) { o.Resources = []string{"example/../users@1.0.0"} },
		func(o *PublishOptions) { o.ArchiveURL = "file:///etc/passwd" },
	} {
		invalid := base
		change(&invalid)
		if err := Publish(invalid); err == nil {
			t.Fatal("invalid publish input unexpectedly succeeded")
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".well-known/dsc.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid inputs modified repository: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".well-known"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".well-known/dsc.json"), []byte(`{"catalog.v1":"../v1/catalog.json","resources.v1":"../v1/resources","packages.v1":"../v1/packages","signing-policies.v1":"../v1/policies.json"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	policies := policyDocument{Policies: []Policy{{Namespace: "example", Required: true}}}
	policyBytes, _ := json.Marshal(policies)
	if err := os.WriteFile(filepath.Join(root, "v1/policies.json"), policyBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	err := Publish(base)
	if err == nil || !strings.Contains(err.Error(), "does not support signing") {
		t.Fatalf("expected actionable signing limitation, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "v1/catalog.json")); !os.IsNotExist(err) {
		t.Fatalf("signing rejection modified catalog: %v", err)
	}
}

func TestPublishPreservesOldContentAddressedDescriptor(t *testing.T) {
	root := t.TempDir()
	one := writePublishTestArchive(t, makeZip(t, "entry", []byte("one")))
	two := writePublishTestArchive(t, makeZip(t, "entry", []byte("two")))
	options := PublishOptions{
		Repository: root, Package: publishPackage, PackageVersion: "1.0.0",
		Platform: "linux_amd64", Archive: one, Resources: []string{"example/users@1.0.0"},
	}
	if err := Publish(options); err != nil {
		t.Fatal(err)
	}
	oldEntry := readCatalogFile(t, root).Resources[publishResource]["1.0.0"]
	oldPath, _ := localCatalogPath(oldEntry.URL)
	oldBody, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(oldPath)))
	if err != nil {
		t.Fatal(err)
	}
	options.PackageVersion = "2.0.0"
	options.Archive = two
	if err := Publish(options); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(oldPath)))
	if err != nil || string(after) != string(oldBody) {
		t.Fatalf("old descriptor was modified: %v", err)
	}
}

func writePublishTestArchive(t *testing.T, data []byte) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func readCatalogFile(t *testing.T, root string) Catalog {
	t.Helper()
	var result Catalog
	readJSONFile(t, filepath.Join(root, "v1/catalog.json"), &result)
	return result
}

func readJSONFile(t *testing.T, file string, target any) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, file, expected string) {
	t.Helper()
	actual, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != expected {
		t.Fatalf("%s = %s, want %s", file, actual, expected)
	}
}

type publishFileState struct {
	data    string
	modTime time.Time
}

func repositoryFileStates(t *testing.T, root string) map[string]publishFileState {
	t.Helper()
	result := make(map[string]publishFileState)
	err := filepath.Walk(root, func(file string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		result[relative] = publishFileState{data: string(data), modTime: info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertRepositoryFileStates(t *testing.T, root string, expected map[string]publishFileState) {
	t.Helper()
	actual := repositoryFileStates(t, root)
	if len(actual) != len(expected) {
		t.Fatalf("repository file count changed: got %d, want %d", len(actual), len(expected))
	}
	for name, state := range expected {
		if actual[name] != state {
			t.Fatalf("repository file %s changed during idempotent publish", name)
		}
	}
}

func TestPublishRejectsMalformedZipAndUnsafeZipPaths(t *testing.T) {
	for _, item := range []struct {
		name string
		data []byte
	}{
		{name: "malformed", data: []byte("not a zip archive")},
		{name: "traversal", data: makeZip(t, "../escape", []byte("bad"))},
	} {
		t.Run(item.name, func(t *testing.T) {
			archive := writePublishTestArchive(t, item.data)
			root := filepath.Join(t.TempDir(), "repository")
			err := Publish(PublishOptions{
				Repository: root, Package: publishPackage, PackageVersion: "1.0.0",
				Platform: "linux_amd64", Archive: archive, Resources: []string{"example/users@1.0.0"},
			})
			if err == nil {
				t.Fatal("invalid ZIP unexpectedly published")
			}
			if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
				t.Fatalf("invalid ZIP modified repository: %v", statErr)
			}
		})
	}
}

func TestPublishRemoteArchiveCanBeUsedByConsumer(t *testing.T) {
	payload := makeZip(t, "resource.dsc.resource.json", []byte(fmt.Sprintf(`{"name":%q}`, publishResource)))
	archiveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer archiveServer.Close()
	root := t.TempDir()
	if err := Publish(PublishOptions{
		Repository: root, Package: publishPackage, PackageVersion: "1.0.0",
		Platform: "linux_amd64", ArchiveURL: archiveServer.URL,
		Resources: []string{"example/users@1.0.0"},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(root)))
	defer server.Close()
	client := NewClient(t.TempDir(), []string{server.URL})
	client.Platform = "linux_amd64"
	result, err := client.Install(publishResource, "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "resource.dsc.resource.json")); err != nil {
		t.Fatal(err)
	}
}

func TestPublishLocalArchiveConsumerUnderURLPrefix(t *testing.T) {
	archiveBytes := makeZip(t, "users.dsc.resource.json", []byte(`{"type":"example/users"}`))
	archive := writePublishTestArchive(t, archiveBytes)
	root := t.TempDir()
	if err := Publish(PublishOptions{
		Repository: root, Package: publishPackage, PackageVersion: "1.0.0",
		Platform: "linux_amd64", Archive: archive, Resources: []string{"example/users@1.0.0"},
	}); err != nil {
		t.Fatal(err)
	}
	const prefix = "/dsc-resources/"
	server := httptest.NewServer(http.StripPrefix(prefix, http.FileServer(http.Dir(root))))
	defer server.Close()
	client := NewClient(t.TempDir(), []string{server.URL + strings.TrimSuffix(prefix, "/")})
	client.Platform = "linux_amd64"
	result, err := client.Install(publishResource, "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "users.dsc.resource.json")); err != nil {
		t.Fatalf("local-published package was not extracted: %v", err)
	}
}
