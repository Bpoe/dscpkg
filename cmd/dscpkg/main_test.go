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
