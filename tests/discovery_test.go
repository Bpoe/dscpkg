//go:build integration

package tests

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDiscoveryDSC33(t *testing.T) {
	dsc := os.Getenv("DSC_TEST_BIN")
	if dsc == "" {
		var err error
		dsc, err = exec.LookPath("dsc")
		if err != nil {
			t.Fatal("DSC 3.3.0 is required; set DSC_TEST_BIN to its executable")
		}
	}
	dsc, err := filepath.Abs(dsc)
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command(dsc, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "dsc 3.3.0" {
		t.Fatalf("expected DSC 3.3.0, got %q: %v", version, err)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	bin := filepath.Join(temp, "bin with spaces")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	engine := filepath.Join(temp, "engine")
	if err := os.Mkdir(engine, 0o755); err != nil {
		t.Fatal(err)
	}
	engineData, err := os.ReadFile(dsc)
	if err != nil {
		t.Fatal(err)
	}
	dsc = filepath.Join(engine, "dsc"+suffix)
	if err := os.WriteFile(dsc, engineData, 0o755); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(bin, "dscpkg"+suffix)
	shared := filepath.Join(temp, "shared"+suffix)
	for _, build := range []struct{ output, source string }{
		{cli, "./cmd/dscpkg"}, {shared, "./tests/testdata/shared"},
	} {
		command := exec.Command("go", "build", "-trimpath", "-o", build.output, build.source)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", build.source, err, output)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(root, "dscpkg.dsc.extension.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "dscpkg.dsc.extension.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	// Both tools use an isolated default per-user store; no machine configuration is changed.
	t.Setenv("LOCALAPPDATA", filepath.Join(temp, "cache"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(temp, "cache"))
	t.Setenv("HOME", temp)
	t.Setenv("USERPROFILE", temp)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	unsetIntegrationEnv(t, "DSC_RESOURCE_PATH")
	unsetIntegrationEnv(t, "DSC_RESTRICTED_PATH")

	var lastDSCStderr string
	run := func(executable string, args ...string) []byte {
		t.Helper()
		if executable == dsc {
			args = append([]string{"--ignore-settings-file", "--trace-level", "info", "--trace-format", "plaintext", "--progress-format", "none"}, args...)
		}
		command := exec.Command(executable, args...)
		command.Dir = temp
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("%s %q: %v\nstdout: %s\nstderr: %s", executable, args, err, &stdout, &stderr)
		}
		if executable == dsc {
			lastDSCStderr = stderr.String()
		}
		return stdout.Bytes()
	}
	list := func() map[string]json.RawMessage {
		t.Helper()
		return integrationObjects(t, run(dsc, "resource", "list", "DscPkg.Test/*", "--output-format", "json"))
	}
	extensions := integrationObjects(t, run(dsc, "extension", "list", "DscPkg/Discover", "--output-format", "json"))
	if len(extensions) != 1 || extensions["DscPkg/Discover"] == nil {
		t.Fatalf("DSC did not discover the staged extension through PATH: %v", extensions)
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("isolated store is not empty: %v", got)
	}

	repository := filepath.Join(temp, "repository")
	server := httptest.NewServer(http.FileServer(http.Dir(repository)))
	defer server.Close()
	platform := runtime.GOOS + "_" + runtime.GOARCH
	install := func(pkg, targetPlatform string, resources ...string) string {
		t.Helper()
		archive := integrationArchive(t, temp, shared, resources)
		args := []string{"publish", "--repository", repository, "--package", pkg,
			"--package-version", "1.0.0", "--platform", targetPlatform, "--archive", archive}
		for _, resource := range resources {
			args = append(args, "--resource", resource+"@1.0.0")
		}
		run(cli, args...)
		output := run(cli, "install", "--repository", server.URL, "--platform", targetPlatform,
			"--resource", resources[0], "--version", "1.0.0")
		var result struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(output, &result); err != nil || result.Path == "" {
			t.Fatalf("invalid install result: %s, %v", output, err)
		}
		return result.Path
	}
	packagePath := install("example/shared", platform, "DscPkg.Test/One", "DscPkg.Test/Two")
	got := list()
	for _, name := range []string{"DscPkg.Test/One", "DscPkg.Test/Two"} {
		if got[name] == nil {
			t.Fatalf("DSC did not discover shared resource %s: %v\n%s", name, got, lastDSCStderr)
		}
	}
	var executables []string
	for _, name := range []string{"One", "Two"} {
		output := run(dsc, "resource", "get", "--resource", "DscPkg.Test/"+name, "--output-format", "json")
		var result struct {
			ActualState struct {
				Name             string `json:"name"`
				SharedExecutable string `json:"sharedExecutable"`
				PackageData      string `json:"packageData"`
			} `json:"actualState"`
		}
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		state := result.ActualState
		if state.Name != name || state.PackageData != "shared package data" {
			t.Fatalf("resource did not execute in its original package directory: %s", output)
		}
		want := filepath.Join(packagePath, filepath.Base(shared))
		if !sameIntegrationPath(t, state.SharedExecutable, want) {
			t.Fatalf("executable=%q, want original package executable %q", state.SharedExecutable, want)
		}
		executables = append(executables, state.SharedExecutable)
	}
	if !sameIntegrationPath(t, executables[0], executables[1]) {
		t.Fatalf("resources did not share an executable: %q", executables)
	}
	foreignPlatform := "windows_amd64"
	if runtime.GOOS == "windows" {
		foreignPlatform = "linux_amd64"
	}
	install("dscextras/linux", foreignPlatform, "DscPkg.Test/Incompatible")
	if got := list(); got["DscPkg.Test/Incompatible"] != nil || len(got) != 2 {
		t.Fatalf("incompatible platform package was advertised: %v", got)
	}
	install("example/new", platform, "DscPkg.Test/New")
	if got := list(); got["DscPkg.Test/New"] == nil || len(got) != 3 {
		t.Fatalf("new DSC process did not discover a newly installed package: %v", got)
	}
	t.Run("explicit resource path needs extension bootstrap", func(t *testing.T) {
		empty := filepath.Join(temp, "empty")
		if err := os.Mkdir(empty, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DSC_RESOURCE_PATH", empty)
		if got := integrationObjects(t, run(dsc, "extension", "list", "DscPkg/Discover", "-o", "json")); len(got) != 0 {
			t.Fatalf("DSC unexpectedly searched PATH when DSC_RESOURCE_PATH was set: %v", got)
		}
		t.Setenv("DSC_RESOURCE_PATH", bin)
		if got := list(); len(got) != 3 {
			t.Fatalf("bootstrapping only the extension directory did not expose package resources: %v", got)
		}
		run(dsc, "resource", "get", "--resource", "DscPkg.Test/Two", "-o", "json")
	})
}

func integrationArchive(t *testing.T, temp, shared string, resources []string) string {
	t.Helper()
	file, err := os.CreateTemp(temp, "resources-*.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	files := map[string][]byte{"package-data.txt": []byte("shared package data")}
	files[filepath.Base(shared)], err = os.ReadFile(shared)
	if err != nil {
		t.Fatal(err)
	}
	for i, resource := range resources {
		name := strings.Split(resource, "/")[1]
		manifest := map[string]any{
			"$schema": "https://aka.ms/dsc/schemas/v3/bundled/resource/manifest.json",
			"type":    resource, "version": "1.0.0",
			"get": map[string]any{"executable": filepath.Base(shared), "args": []string{name}},
			"schema": map[string]any{"embedded": map[string]any{
				"type": "object", "properties": map[string]any{
					"name": map[string]string{"type": "string"}, "sharedExecutable": map[string]string{"type": "string"},
					"packageData": map[string]string{"type": "string"},
				},
			}},
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		extension := ".json"
		if i%2 == 1 {
			extension = ".yaml"
			// DSC 3.3's YAML deserializer requires a tag for the schema enum.
			data = []byte(fmt.Sprintf(`$schema: https://aka.ms/dsc/schemas/v3/bundled/resource/manifest.json
type: %q
version: 1.0.0
get:
  executable: %q
  args: [%q]
schema: !embedded
  type: object
  properties:
    name: {type: string}
    sharedExecutable: {type: string}
    packageData: {type: string}
`, resource, filepath.Base(shared), name))
		}
		files[name+".dsc.resource"+extension] = data
	}
	for name, data := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		if name == filepath.Base(shared) {
			header.SetMode(0o755)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return file.Name()
}

func integrationObjects(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	objects := make(map[string]json.RawMessage)
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var object json.RawMessage
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("invalid DSC JSON output %s: %v", data, err)
		}
		var identity struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(object, &identity); err != nil || identity.Type == "" {
			t.Fatalf("invalid DSC object: %s, %v", object, err)
		}
		objects[identity.Type] = object
	}
	return objects
}

func sameIntegrationPath(t *testing.T, a, b string) bool {
	t.Helper()
	first, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(first, second)
}

func unsetIntegrationEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}
