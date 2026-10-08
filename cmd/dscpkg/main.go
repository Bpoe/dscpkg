package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Bpoe/dscpkg/internal/dscpkg"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "dscpkg:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	if len(args) == 0 {
		return usageError()
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	packagesDir := flags.String("packages-dir", defaultPackagesDir(), "package cache directory")
	platform := flags.String("platform", currentPlatform(), "target os_arch platform")
	var repositories stringList
	flags.Var(&repositories, "repository", "repository origin, in precedence order (repeatable)")
	var resource, version, packageVersion string
	switch command {
	case "install":
		flags.StringVar(&resource, "resource", "", "resource type (Namespace/name)")
		flags.StringVar(&version, "version", "", "resource version")
		flags.StringVar(&packageVersion, "package-version", "", "exact package version (optional)")
	case "update", "cleanup", "list", "env":
	case "remove":
		flags.StringVar(&resource, "resource", "", "resource type (Namespace/name)")
		flags.StringVar(&version, "version", "", "resource version (optional)")
	default:
		return usageError()
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}

	switch command {
	case "install", "update", "cleanup":
		if len(repositories) == 0 {
			return errors.New("--repository is required")
		}
		client := dscpkg.NewClient(*packagesDir, repositories)
		client.Platform = *platform
		switch command {
		case "install":
			if resource == "" || version == "" {
				return errors.New("install requires --resource and --version")
			}
			result, err := client.Install(resource, version, packageVersion)
			if err != nil {
				return err
			}
			return printJSON(stdout, result)
		case "update":
			result, err := client.Update()
			if err != nil {
				return err
			}
			return printJSON(stdout, result)
		default:
			removed, err := client.Cleanup()
			if err != nil {
				return err
			}
			return printJSON(stdout, removed)
		}
	case "list":
		registry, err := dscpkg.ReadRegistry(*packagesDir)
		if err != nil {
			return err
		}
		return printJSON(stdout, registry)
	case "remove":
		if resource == "" {
			return errors.New("remove requires --resource")
		}
		if err := dscpkg.RemoveResource(*packagesDir, resource, version); err != nil {
			return err
		}
		return nil
	case "env":
		value, err := dscpkg.ResourcePath(*packagesDir, os.Getenv("DSC_RESOURCE_PATH"))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "DSC_RESOURCE_PATH=%s\n", value)
		return err
	}
	return usageError()
}

func printJSON(out *os.File, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func usageError() error {
	return errors.New("usage: dscpkg <install|update|cleanup|list|remove|env> [flags]")
}

func defaultPackagesDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "dscpkg", "packages")
	}
	return filepath.Join(".", "packages")
}

func currentPlatform() string {
	osName := runtime.GOOS
	if osName == "darwin" {
		osName = "darwin"
	}
	arch := runtime.GOARCH
	if arch == "x86_64" {
		arch = "amd64"
	}
	return osName + "_" + arch
}
