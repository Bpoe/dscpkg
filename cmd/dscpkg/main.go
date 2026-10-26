package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	if command == "resource" {
		return runResource(args[1:], os.Stdin, stdout, stderr)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var packagesDir *string
	var platform string
	var repositoryDirectory, packageName, packageVersion, archive, archiveURL string
	var publishResources stringList
	var repositories stringList
	var resource, version, selectedPackageVersion string
	switch command {
	case "install":
		packagesDir = flags.String("packages-dir", defaultPackagesDir(), "package cache directory")
		flags.Var(&repositories, "repository", "repository origin, in precedence order (repeatable)")
		flags.StringVar(&platform, "platform", currentPlatform(), "target os_arch platform")
		flags.StringVar(&resource, "resource", "", "resource type (Namespace/name)")
		flags.StringVar(&version, "version", "", "exact resource version (default: latest compatible stable)")
		flags.StringVar(&selectedPackageVersion, "package-version", "", "exact package version (optional)")
	case "update", "cleanup":
		packagesDir = flags.String("packages-dir", defaultPackagesDir(), "package cache directory")
		flags.Var(&repositories, "repository", "repository origin, in precedence order (repeatable)")
		flags.StringVar(&platform, "platform", currentPlatform(), "target os_arch platform")
	case "publish":
		flags.StringVar(&repositoryDirectory, "repository", "", "local repository directory")
		flags.StringVar(&packageName, "package", "", "package identity (namespace/name)")
		flags.StringVar(&packageVersion, "package-version", "", "package version")
		flags.StringVar(&platform, "platform", "", "target os_arch platform")
		flags.StringVar(&archive, "archive", "", "local ZIP archive")
		flags.StringVar(&archiveURL, "archive-url", "", "externally hosted HTTP(S) ZIP URL")
		flags.Var(&publishResources, "resource", "resource identity and version (Namespace/name@version; repeatable)")
		flags.Usage = func() {
			fmt.Fprintln(stderr, "usage: dscpkg publish --repository DIR --package namespace/name --package-version VERSION --platform os_arch (--archive FILE | --archive-url URL) --resource Namespace/name@version [--resource ...]")
			flags.PrintDefaults()
		}
	case "list", "env", "remove", "discover":
		packagesDir = flags.String("packages-dir", defaultPackagesDir(), "package cache directory")
	default:
		return usageError()
	}
	if command == "remove" {
		flags.StringVar(&resource, "resource", "", "resource type (Namespace/name)")
		flags.StringVar(&version, "version", "", "resource version (optional)")
	}
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}

	switch command {
	case "discover":
		return dscpkg.Discover(*packagesDir, stdout, stderr)
	case "install", "update", "cleanup":
		if len(repositories) == 0 {
			return errors.New("--repository is required")
		}
		client := dscpkg.NewClient(*packagesDir, repositories)
		client.Platform = platform
		switch command {
		case "install":
			if resource == "" {
				return errors.New("install requires --resource")
			}
			result, err := client.Install(resource, version, selectedPackageVersion)
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
	case "publish":
		if repositoryDirectory == "" || packageName == "" || packageVersion == "" || platform == "" {
			return errors.New("publish requires --repository, --package, --package-version, and --platform")
		}
		if err := dscpkg.Publish(dscpkg.PublishOptions{
			Repository: repositoryDirectory, Package: packageName, PackageVersion: packageVersion,
			Platform: platform, Archive: archive, ArchiveURL: archiveURL, Resources: publishResources,
		}); err != nil {
			return err
		}
		return nil
	case "env":
		value, err := dscpkg.ResourcePath(*packagesDir, os.Getenv("DSC_RESOURCE_PATH"))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, value)
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
	return errors.New("usage: dscpkg <install|update|cleanup|publish|list|remove|env|discover|resource> [flags]")
}

func defaultPackagesDir() string {
	if dir := os.Getenv("DSCPKG_PACKAGES_DIR"); dir != "" {
		return dir
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "dscpkg", "packages")
	}

	return filepath.Join(".", "packages")
}

func runResource(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: dscpkg resource <get|test|set|export> [--packages-dir DIR]")
	}
	operation := args[0]
	switch operation {
	case "get", "test", "set", "export":
	default:
		return fmt.Errorf("unknown resource operation %q", operation)
	}
	flags := flag.NewFlagSet("resource "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	packagesDir := flags.String("packages-dir", defaultPackagesDir(), "package cache directory")
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("resource operations do not accept positional arguments")
	}
	client := dscpkg.NewClient(*packagesDir, nil)
	encoder := json.NewEncoder(stdout)
	if operation == "export" {
		resources, err := client.InstalledResources(stderr)
		if err != nil {
			return err
		}
		for _, resource := range resources {
			if err := encoder.Encode(resource); err != nil {
				return err
			}
		}
		return nil
	}
	var desired dscpkg.ResourceState
	decoder := json.NewDecoder(io.LimitReader(stdin, 16<<20+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&desired); err != nil {
		return fmt.Errorf("read resource properties: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("expected exactly one resource properties object")
	}
	var actual dscpkg.ResourceState
	var differences []string
	var err error
	switch operation {
	case "get":
		actual, err = client.GetResource(desired, stderr)
	case "test":
		actual, differences, err = client.TestResource(desired, stderr)
	case "set":
		actual, err = client.SetResource(desired, stderr)
	}
	if err != nil {
		return err
	}
	if err := encoder.Encode(actual); err != nil {
		return err
	}
	if operation == "test" {
		return encoder.Encode(differences)
	}
	return nil
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
