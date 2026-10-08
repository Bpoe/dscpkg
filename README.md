# dscpkg

`dscpkg` is a cross-platform Go CLI for acquiring DSC v3 resource packages from
static DSC Resource Repositories. It resolves a resource type and version through
an ordered repository catalog, verifies metadata and archive integrity, safely
extracts the package, and records the resource in a local registry.

## Build

Requires Go 1.23 or later:

```sh
go build -o dscpkg ./cmd/dscpkg
```

On Windows, use `go build -o dscpkg.exe ./cmd/dscpkg`.

## Configure repositories

Pass repository origins in precedence order with repeatable `--repository` flags.
A bare host defaults to HTTPS; specify `http://` explicitly for local testing.
For each resource, the first repository catalog containing that exact resource
type and version is selected. A later repository is not used to recover from an
integrity or signature error in the selected repository.

```sh
dscpkg install \
  --repository https://resources.example.com \
  --repository https://public.example.com \
  --resource Microsoft.GuestConfiguration/users \
  --version 2026-06-30-preview
```

`--packages-dir` selects the package cache and registry directory. By default it
uses `<user cache directory>/dscpkg/packages`. `--platform` overrides the detected
`os_arch` archive key, for example `linux_amd64`.

## Commands

Install a resource and optionally select an exact package version:

```sh
dscpkg install --repository https://resources.example.com \
  --resource Microsoft.GuestConfiguration/users \
  --version 2026-06-30-preview \
  --package-version 1.0.1
```

Re-resolve and update all registered resources, then regenerate the active package
path list for the command's process:

```sh
dscpkg update --repository https://resources.example.com
```

Remove cached package versions not required by registered resources. Package
directories currently listed in `DSC_RESOURCE_PATH` are retained:

```sh
dscpkg cleanup --repository https://resources.example.com
```

List the local resource registry or remove a resource registration. Removing a
registration does not delete package files; use `cleanup` to reclaim unused cache
versions.

```sh
dscpkg list
dscpkg remove --resource Microsoft.GuestConfiguration/users \
  --version 2026-06-30-preview
```

Print the current package paths for shell integration:

```sh
dscpkg env
```

The CLI cannot change its parent shell's environment. For Bash, import the printed
value after install or update:

```sh
export DSC_RESOURCE_PATH="$(dscpkg env | sed 's/^DSC_RESOURCE_PATH=//')"
dsc.exe get --resource Microsoft.GuestConfiguration/users
```

For PowerShell:

```powershell
$env:DSC_RESOURCE_PATH = ((dscpkg env) -replace '^DSC_RESOURCE_PATH=', '')
dsc.exe get --resource Microsoft.GuestConfiguration/users
```

`DSC_RESOURCE_PATH` uses the platform's path-list separator. `dscpkg env` preserves
existing paths outside the package cache and includes cached package-version
directories. Add it to the environment used to launch DSC so DSC can discover
the package's `*.dsc.resource.json` manifests.

## Repository format and signing

The client follows the discovery, catalog, resource descriptor, package descriptor,
and archive URL conventions in the
[DSC Resource Repository design](https://github.com/Bpoe/DSC/blob/main/docs/design-resource-download/dsc-resource-download.md).
Publish `/.well-known/dsc.json`, a catalog with ETags and descriptor digests,
lowercase resource/package metadata paths, and platform-specific zip archives with
hashes.

Resource and package descriptors are verified with detached compact JWS when a
namespace signing policy requires it. Policies may be published through
`signing-policies.v1` in discovery. The design does not publish bundled public-key
material, so this implementation has no built-in namespace trust keys; repositories
must supply policy keys for namespaces they require the client to verify. Policies
from a future built-in table take precedence over repository policies.

The local `resources.json` file stores registered resource type, resource version,
and the last processed descriptor digest. It intentionally does not store package
identity or package versions; those are re-derived from repository metadata.
Catalog response bodies and ETags are cached separately under `.dscpkg/`.

## Development and CI checks

Run the same checks locally as CI:

```sh
test -z "$(gofmt -l $(git ls-files '*.go'))"
go vet ./...
go test -race ./...
go build -trimpath -o dist/dscpkg ./cmd/dscpkg
```

The GitHub Actions workflow runs formatting, vetting, tests (with race detection on
Linux), and builds on Linux, Windows, and macOS. It uploads one executable artifact
for each runner operating system. Go build caching is enabled through
`actions/setup-go`.
