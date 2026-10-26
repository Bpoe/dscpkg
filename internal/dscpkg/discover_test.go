package dscpkg

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDiscoverEmptyStore(t *testing.T) {
	for _, root := range []string{t.TempDir(), filepath.Join(t.TempDir(), "absent")} {
		var out, diagnostics bytes.Buffer
		if err := Discover(root, &out, &diagnostics); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 || diagnostics.Len() != 0 {
			t.Fatalf("empty store: stdout=%q stderr=%q", out.String(), diagnostics.String())
		}
		if _, err := os.Stat(filepath.Join(root, ".dscpkg")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("discovery created store state: %v", err)
		}
	}
}

func TestDiscoverManifests(t *testing.T) {
	for _, platform := range []string{"windows_amd64", "linux_amd64", "linux_arm64"} {
		t.Run(platform, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "package store & spaces")
			names := []string{
				"groups.dsc.resource.json", "services.dsc.resource.yaml", "nested/third.dsc.resource.yml",
				"UPPER.DSC.RESOURCE.JSON", "adapted.dsc.adaptedresource.json",
				"adapted.dsc.adaptedresource.yaml", "adapted.dsc.adaptedresource.yml",
				"list.dsc.manifests.json", "list.dsc.manifests.yaml", "list.dsc.manifests.yml",
			}
			if runtime.GOOS != "windows" {
				names = append(names, "quoted\"name.dsc.resource.json", "back\\slash.dsc.resource.json")
			}
			var want []string
			for _, pkg := range []string{"z/shared/2.0.0", "a/shared/1.0.0", "a/shared/2.0.0"} {
				directory := discoveryPackage(t, root, pkg, platform)
				for _, name := range names {
					path := filepath.Join(directory, filepath.FromSlash(name))
					discoveryFile(t, path, "{}")
					want = append(want, path)
				}
				for _, name := range []string{"README.md", "resource.json", "ignored.dsc.resource.json.bak", "ignored.dsc.extension.json"} {
					discoveryFile(t, filepath.Join(directory, name), "{}")
				}
			}
			t.Setenv("DSC_RESOURCE_PATH", filepath.Dir(root))
			slices.Sort(want)
			var previous string
			for range 3 {
				var out, diagnostics bytes.Buffer
				if err := discover(root, platform, &out, &diagnostics); err != nil {
					t.Fatal(err)
				}
				got := decodeDiscovery(t, out.Bytes())
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("manifests = %q, want %q", got, want)
				}
				if previous != "" && previous != out.String() {
					t.Fatal("discovery output is not deterministic")
				}
				previous = out.String()
				if diagnostics.Len() != 0 {
					t.Fatalf("unexpected diagnostics: %s", &diagnostics)
				}
			}
		})
	}
}

func TestDiscoverSingleManifest(t *testing.T) {
	root := t.TempDir()
	directory := discoveryPackage(t, root, "example/shared/1", currentPlatform())
	path := filepath.Join(directory, "one.dsc.resource.json")
	discoveryFile(t, path, "{}")
	var out, diagnostics bytes.Buffer
	if err := Discover(root, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if got := decodeDiscovery(t, out.Bytes()); !reflect.DeepEqual(got, []string{path}) {
		t.Fatalf("got %q, want %q", got, path)
	}
}

func TestDiscoverPlatformAndLegacyExclusions(t *testing.T) {
	root := t.TempDir()
	for _, pkg := range []struct{ name, platform string }{
		{"example/native/1", "windows_amd64"},
		{"dscextras/linux/1", "linux_amd64"},
		{"example/otherarch/1", "windows_arm64"},
		{"example/legacy/1", ""},
		{"example/shared/.dscpkg-install-partial", ""},
	} {
		directory := discoveryPackage(t, root, pkg.name, pkg.platform)
		discoveryFile(t, filepath.Join(directory, "one.dsc.resource.json"), "{}")
	}
	// Neither arbitrary inherited paths nor incomplete store layouts are candidates.
	discoveryFile(t, filepath.Join(root, "not-a-namespace.dsc.resource.json"), "{}")
	discoveryFile(t, filepath.Join(root, "example", "not-a-package"), "{}")
	discoveryFile(t, filepath.Join(root, "example", "shared", "not-a-version"), "{}")
	outside := t.TempDir()
	discoveryFile(t, filepath.Join(outside, "outside.dsc.resource.json"), "{}")
	t.Setenv("DSC_RESOURCE_PATH", outside)
	var out, diagnostics bytes.Buffer
	if err := discover(root, "windows_amd64", &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "example", "native", "1", "one.dsc.resource.json")
	if got := decodeDiscovery(t, out.Bytes()); !reflect.DeepEqual(got, []string{want}) {
		t.Fatalf("advertised incompatible, legacy, partial, or external package: %q", got)
	}
	if !strings.Contains(diagnostics.String(), "legacy") || !strings.Contains(diagnostics.String(), "reinstall") {
		t.Fatalf("legacy exclusion needs actionable diagnostics: %s", &diagnostics)
	}
	if strings.Contains(diagnostics.String(), "partial") {
		t.Fatalf("staging directories should be ignored: %s", &diagnostics)
	}
}

func TestDiscoverCorruptMetadata(t *testing.T) {
	for _, data := range []string{"{", "null", `{}`, `{"schemaVersion":2,"platform":"linux_amd64"}`, `{"schemaVersion":1,"platform":"unknown"}`} {
		t.Run(data, func(t *testing.T) {
			root := t.TempDir()
			good := discoveryPackage(t, root, "a/good/1", currentPlatform())
			discoveryFile(t, filepath.Join(good, "one.dsc.resource.json"), "{}")
			bad := discoveryPackage(t, root, "z/bad/1", "")
			discoveryFile(t, installedMetadataPath(bad), data)
			var out, diagnostics bytes.Buffer
			err := Discover(root, &out, &diagnostics)
			if err == nil || !strings.Contains(err.Error(), "reinstall") || out.Len() != 0 {
				t.Fatalf("corrupt metadata: err=%v stdout=%s", err, &out)
			}
		})
	}
}

func TestDiscoverReadOnly(t *testing.T) {
	root := t.TempDir()
	directory := discoveryPackage(t, root, "example/shared/1", currentPlatform())
	discoveryFile(t, filepath.Join(directory, "one.dsc.resource.json"), "{}")
	original := discoverySnapshot(t, root)
	t.Cleanup(func() {
		for path, entry := range original {
			if err := os.Chmod(path, entry.mode.Perm()); err != nil {
				t.Errorf("restore fixture permissions: %v", err)
			}
		}
	})
	for path, entry := range original {
		mode := fs.FileMode(0o444)
		if entry.mode.IsDir() {
			mode = 0o555
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	before := discoverySnapshot(t, root)
	var out, diagnostics bytes.Buffer
	if err := Discover(root, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if len(decodeDiscovery(t, out.Bytes())) != 1 {
		t.Fatal("read-only discovery lost manifest")
	}
	if after := discoverySnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("discovery mutated store: before=%v after=%v", before, after)
	}
}

func TestDiscoverDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := discoveryPackage(t, t.TempDir(), "example/external/1", currentPlatform())
	discoveryFile(t, filepath.Join(outside, "outside.dsc.resource.json"), "{}")
	directory := discoveryPackage(t, root, "example/shared/1", currentPlatform())
	links := map[string]string{
		filepath.Join(directory, "alias.dsc.resource.json"): filepath.Join(outside, "outside.dsc.resource.json"),
		filepath.Join(directory, "nested"):                  outside,
		filepath.Join(root, "example", "shared", "2"):       outside,
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlinks require developer mode or privilege: %v", err)
			}
			t.Fatal(err)
		}
	}
	var out, diagnostics bytes.Buffer
	if err := Discover(root, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("followed links: stdout=%s stderr=%s", &out, &diagnostics)
	}
}

func TestDiscoverIOErrors(t *testing.T) {
	root := t.TempDir()
	directory := discoveryPackage(t, root, "example/shared/1", currentPlatform())
	discoveryFile(t, filepath.Join(directory, "one.dsc.resource.json"), "{}")
	if err := Discover(root, failingDiscoveryWriter{}, io.Discard); err == nil {
		t.Fatal("stdout error was hidden")
	}
	if err := os.Remove(installedMetadataPath(directory)); err != nil {
		t.Fatal(err)
	}
	if err := Discover(root, io.Discard, failingDiscoveryWriter{}); err == nil {
		t.Fatal("diagnostic write error was hidden")
	}
	file := filepath.Join(t.TempDir(), "file")
	discoveryFile(t, file, "{}")
	if err := Discover(file, io.Discard, io.Discard); err == nil {
		t.Fatal("invalid store did not fail")
	}
}

type failingDiscoveryWriter struct{}

func (failingDiscoveryWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func discoveryPackage(t *testing.T, root, name, platform string) string {
	t.Helper()
	directory := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if platform != "" {
		if err := writeInstalledPackage(directory, platform); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func discoveryFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decodeDiscovery(t *testing.T, output []byte) []string {
	t.Helper()
	var result []string
	for _, line := range bytes.Split(bytes.TrimSuffix(output, []byte("\n")), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var manifest map[string]string
		if err := json.Unmarshal(line, &manifest); err != nil {
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
		path := manifest["manifestPath"]
		if len(manifest) != 1 || !filepath.IsAbs(path) {
			t.Fatalf("expected exactly one absolute manifestPath: %q", line)
		}
		if slices.Contains(result, path) {
			t.Fatalf("duplicate manifestPath: %s", path)
		}
		result = append(result, path)
	}
	return result
}

type discoveryFileState struct {
	mode    fs.FileMode
	modTime time.Time
	content string
}

func discoverySnapshot(t *testing.T, root string) map[string]discoveryFileState {
	t.Helper()
	result := make(map[string]discoveryFileState)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state := discoveryFileState{mode: info.Mode()}
		if !entry.IsDir() {
			// Windows can defer directory timestamp updates from fixture creation.
			state.modTime = info.ModTime()
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			state.content = string(data)
		}
		result[path] = state
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
