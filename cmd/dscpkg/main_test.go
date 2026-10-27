package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDscpkgProcess(t *testing.T) {
	if os.Getenv("DSCPKG_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"dscpkg"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestDiscoverProcess(t *testing.T) {
	for _, test := range []struct {
		name, metadata string
		manifest       bool
		exit           int
		diagnostic     string
		args           []string
	}{
		{name: "missing store"},
		{name: "single manifest", manifest: true, metadata: `{"schemaVersion":1,"platform":"` + currentPlatform() + `"}`},
		{name: "legacy excluded", manifest: true, diagnostic: "reinstall"},
		{name: "corrupt metadata", manifest: true, metadata: "{", exit: 1, diagnostic: "reinstall"},
		{name: "invalid argument", args: []string{"--invalid"}, exit: 1, diagnostic: "flag provided but not defined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := filepath.Join(root, "store with spaces")
			manifest := filepath.Join(store, "example", "shared", "1", "one.dsc.resource.json")
			if test.manifest {
				if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(manifest, []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
				if test.metadata != "" {
					path := filepath.Join(filepath.Dir(filepath.Dir(manifest)), ".1.dscpkg.json")
					if err := os.WriteFile(path, []byte(test.metadata), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Setenv("DSC_RESOURCE_PATH", root)
			t.Setenv("DSCPKG_TEST_PROCESS", "1")
			args := []string{"-test.run=^TestDscpkgProcess$", "--", "discover", "--packages-dir", filepath.Base(store)}
			command := exec.Command(os.Args[0], append(args, test.args...)...)
			command.Dir = root
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			exit := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exit = exitErr.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if exit != test.exit {
				t.Fatalf("exit=%d want=%d stderr=%s", exit, test.exit, &stderr)
			}
			if test.diagnostic != "" {
				if !strings.Contains(stderr.String(), test.diagnostic) {
					t.Fatalf("stderr=%q, want %q", stderr.String(), test.diagnostic)
				}
			} else if stderr.Len() != 0 {
				t.Fatalf("unexpected stderr: %s", &stderr)
			}
			if test.name == "single manifest" {
				encoded, _ := json.Marshal(map[string]string{"manifestPath": manifest})
				if stdout.String() != string(encoded)+"\n" {
					t.Fatalf("stdout=%q, want JSONL %q", stdout.String(), encoded)
				}
			} else if stdout.Len() != 0 {
				t.Fatalf("stdout must contain only discovered JSON Lines: %q", stdout.String())
			}
		})
	}
}

func TestEnvPrintsPathValue(t *testing.T) {
	for _, test := range []struct {
		name     string
		existing bool
		cached   bool
	}{
		{name: "empty"},
		{name: "existing", existing: true},
		{name: "cached", cached: true},
		{name: "existing and cached", existing: true, cached: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			packagesDir := filepath.Join(root, "packages")
			var paths []string
			existing := ""
			if test.existing {
				existing = filepath.Join(root, "custom resources")
				paths = append(paths, existing)
			}

			t.Setenv("DSC_RESOURCE_PATH", existing)
			if test.cached {
				cached := filepath.Join(packagesDir, "example.test", "shared", "1.0.0")
				if err := os.MkdirAll(cached, 0o755); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, cached)
			}

			stdout, err := os.CreateTemp(root, "stdout")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stdout.Close() })
			stderr, err := os.CreateTemp(root, "stderr")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stderr.Close() })

			if err := run([]string{"env", "--packages-dir", packagesDir}, stdout, stderr); err != nil {
				t.Fatal(err)
			}
			output, err := os.ReadFile(stdout.Name())
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Join(paths, string(os.PathListSeparator)) + "\n"
			if string(output) != want {
				t.Fatalf("stdout = %q, want %q", output, want)
			}
			diagnostics, err := os.ReadFile(stderr.Name())
			if err != nil {
				t.Fatal(err)
			}
			if len(diagnostics) != 0 {
				t.Fatalf("unexpected stderr: %q", diagnostics)
			}
		})
	}
}

func TestPublishHelpListsPublishingFlags(t *testing.T) {
	root := t.TempDir()
	stdout, err := os.CreateTemp(root, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(root, "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	err = run([]string{"publish", "-h"}, stdout, stderr)
	if err != nil {
		t.Fatalf("help request failed: %v", err)
	}
	output, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--repository", "--package", "--package-version", "--platform", "--archive", "--archive-url", "--resource"} {
		if !strings.Contains(string(output), flag) {
			t.Fatalf("publish help does not include %s: %s", flag, output)
		}
	}
}

func TestResourceCommands(t *testing.T) {
	t.Setenv("DSCPKG_PACKAGES_DIR", filepath.Join(t.TempDir(), "absent"))
	for _, test := range []struct {
		operation, input, output, errorText string
	}{
		{"get", `{"resource":"Example/One"}`, `{"resource":"Example/One"}` + "\n", ""},
		{"test", `{"resource":"Example/One"}`, `{"resource":"Example/One","_inDesiredState":false}` + "\n" + `["version"]` + "\n", ""},
		{"test", `{"resource":"Example/One","version":"1.0.0"}`, `{"resource":"Example/One","_inDesiredState":false}` + "\n" + `["version"]` + "\n", ""},
		{"export", "", "", ""},
		{"export", "not read", "", ""},
		{"get", "{}", "", "invalid resource"},
		{"get", "null", "", "invalid resource"},
		{"get", `{"resource":"Example/One","unknown":true}`, "", "unknown field"},
		{"test", `{"resource":"Example/One","state":"installed"}`, "", `unknown field "state"`},
		{"get", `{"resource":"Example/One"} {}`, "", "exactly one"},
		{"set", `{"resource":"Example/One"}`, "", "repository"},
		{"set", `{"resource":"Example/One","state":"missing"}`, "", `unknown field "state"`},
	} {
		t.Run(test.operation+test.input, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			err := runResource([]string{test.operation}, strings.NewReader(test.input), &out, &diagnostics)
			if test.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), test.errorText) {
					t.Fatalf("err=%v want=%q", err, test.errorText)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if out.String() != test.output || diagnostics.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q", out.String(), diagnostics.String())
			}
		})
	}
}

func TestResourceExportNames(t *testing.T) {
	store := t.TempDir()
	t.Setenv("DSCPKG_PACKAGES_DIR", store)
	for _, version := range []string{"0.1.1", "0.2.0"} {
		directory := filepath.Join(store, "dscextras", "linux", version)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		metadata := `{"schemaVersion":1,"platform":"` + currentPlatform() + `"}`
		if err := os.WriteFile(filepath.Join(filepath.Dir(directory), "."+version+".dscpkg.json"), []byte(metadata), 0o644); err != nil {
			t.Fatal(err)
		}
		manifests := `{"resources":[{"type":"DscExtras.Linux/groups","version":"` + version +
			`"},{"type":"DscExtras.Linux/services","version":"` + version + `"}]}`
		if err := os.WriteFile(filepath.Join(directory, "linux.dsc.manifests.json"), []byte(manifests), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := runResource([]string{"export"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n"))
	if len(lines) != 4 {
		t.Fatalf("expected one instance per resource/version pair: %s", &stdout)
	}
	names := make(map[string]bool)
	for _, line := range lines {
		var instance struct {
			Name     string `json:"_name"`
			Resource string `json:"resource"`
			Version  string `json:"version"`
		}
		if err := json.Unmarshal(line, &instance); err != nil {
			t.Fatal(err)
		}
		want := instance.Resource + "@" + instance.Version
		if instance.Name != want || names[instance.Name] {
			t.Fatalf("expected unique unencoded resource@version name %q: %s", want, line)
		}
		names[instance.Name] = true
	}
	for _, operation := range []string{"get", "test", "set"} {
		stdout.Reset()
		if err := runResource([]string{operation}, strings.NewReader(`{"resource":"DscExtras.Linux/groups"}`), &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(stdout.Bytes(), []byte(`"_name"`)) {
			t.Fatalf("%s emitted Export-only _name: %s", operation, &stdout)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %s", &stderr)
	}
}

func TestPackageStoreOverride(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "shared store")
	t.Setenv("DSCPKG_PACKAGES_DIR", directory)
	if got := defaultPackagesDir(); got != directory {
		t.Fatalf("default store=%q want=%q", got, directory)
	}
	var stdout, stderr bytes.Buffer
	if err := runResource([]string{"get", "--packages-dir", filepath.Join(t.TempDir(), "explicit")},
		strings.NewReader(`{"resource":"Example/One"}`), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
}
