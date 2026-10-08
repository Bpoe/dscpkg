package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
