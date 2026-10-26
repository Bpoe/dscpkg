package dscpkg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCachedPackagePlatformValidation(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata string
		wantErr  string
	}{
		{"compatible", `{"schemaVersion":1,"platform":"windows_amd64"}`, ""},
		{"incompatible OS", `{"schemaVersion":1,"platform":"linux_amd64"}`, "targets linux_amd64"},
		{"incompatible architecture", `{"schemaVersion":1,"platform":"windows_arm64"}`, "targets windows_arm64"},
		{"corrupt", "{", "reinstall"},
		{"legacy", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := discoveryPackage(t, root, "example/shared/1", "")
			if test.metadata != "" {
				discoveryFile(t, installedMetadataPath(path), test.metadata)
			}
			before := discoverySnapshot(t, root)
			client := NewClient(root, nil)
			client.Platform = "windows_amd64"
			// A cached install must not contact the repository or relabel its platform.
			downloaded, err := client.installPackage(nil, packageResolution{Path: path})
			if downloaded || (test.wantErr == "" && err != nil) ||
				(test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr))) {
				t.Fatalf("downloaded=%v err=%v, want %q", downloaded, err, test.wantErr)
			}
			if after := discoverySnapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("cached installation changed files: before=%v after=%v", before, after)
			}
		})
	}
}

func TestInstalledMetadataIsNotAPackage(t *testing.T) {
	root := t.TempDir()
	directory := discoveryPackage(t, root, "example/shared/1", currentPlatform())
	paths, err := packageDirectories(root)
	if err != nil || len(paths) != 1 || paths[0] != directory {
		t.Fatalf("sidecar enumerated as package: %v, %v", paths, err)
	}
	metadata := installedMetadataPath(directory)
	if filepath.Dir(metadata) != filepath.Dir(directory) {
		t.Fatal("provenance is not outside archive contents")
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	paths, err = packageDirectories(root)
	if err != nil || len(paths) != 0 {
		t.Fatalf("orphaned sidecar enumerated as package: %v, %v", paths, err)
	}
	if _, err := readInstalledPackage(directory); err != nil {
		t.Fatalf("test needs an orphaned sidecar: %v", err)
	}
}
