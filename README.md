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

Print the current package paths for shell integration, without a
`DSC_RESOURCE_PATH=` prefix:

```sh
dscpkg env
```

The CLI cannot change its parent shell's environment. For Bash, import the printed
value after install or update:

```sh
export DSC_RESOURCE_PATH="$(dscpkg env)"
dsc.exe resource get --resource Microsoft.GuestConfiguration/users
```

For PowerShell:

```powershell
$env:DSC_RESOURCE_PATH = dscpkg env
dsc.exe resource get --resource Microsoft.GuestConfiguration/users
```

`DSC_RESOURCE_PATH` uses the platform's path-list separator. `dscpkg env` preserves
existing paths outside the package cache and includes cached package-version
directories. Add it to the environment used to launch DSC so DSC can discover
the package's `*.dsc.resource.json` manifests.

## Publish a repository

`dscpkg publish` creates or updates the static repository metadata in a local
directory. It does not create ZIP archives, commit files, upload content, or deploy
a website. Supply each resource type and its resource version explicitly; resource
versions are independent of package versions.

Publish a local ZIP (the archive is validated and copied byte-for-byte into the
repository):

```sh
dscpkg publish \
  --repository ./wwwroot \
  --package example/resources \
  --package-version 1.0.0 \
  --platform linux_amd64 \
  --archive ./dist/resources.zip \
  --resource example/users@1.0.0 \
  --resource example/groups@1.0.0
```

Or publish metadata for an externally hosted ZIP. The publisher follows HTTP
redirects, validates and hashes the downloaded archive, but does not copy it into
`wwwroot`:

```sh
dscpkg publish \
  --repository ./wwwroot \
  --package example/resources \
  --package-version 1.0.0 \
  --platform linux_amd64 \
  --archive-url https://github.com/example/dsc-resources/releases/download/v1.0.0/resources_linux_amd64.zip \
  --resource example/users@1.0.0
```

The first publish initializes `.well-known/dsc.json`, `v1/catalog.json`,
`v1/resources/`, and `v1/packages/`. Discovery uses relative URLs, so the same
repository works at a site root and under a path prefix such as
`https://example.github.io/dsc-resources/`. Subsequent publishes preserve unrelated
resources, package versions, and platforms. A package identity/version/platform
publication is immutable: identical repeats are safe, while changed archive bytes
or a changed archive URL are rejected. Resource descriptors use content-addressed
filenames, and the catalog is atomically replaced only after referenced files have
been written.

For GitHub Pages, keep `wwwroot/` in a Git repository and configure **Settings →
Pages** to publish that directory (for example, choose **GitHub Actions** and make
the Pages artifact from `wwwroot/`, or publish a branch folder containing the
generated files). The archive in the external-URL example remains on GitHub
Releases; it is not committed to the Pages repository. If Jekyll processing is
enabled, add an empty `wwwroot/.nojekyll` file yourself so `.well-known/dsc.json`
is served. The CLI does not commit, push, upload, or deploy anything.

To test the exact same static files locally:

```sh
python3 -m http.server 8080 --directory ./wwwroot
```

In another terminal:

```sh
dscpkg install \
  --repository http://localhost:8080 \
  --resource example/users \
  --version 1.0.0
```

The generated files can also be served without modification by IIS, Nginx, Apache,
or another static HTTP host with suitable static-file configuration.

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

`dscpkg publish` does not sign metadata. It preserves existing discovery configuration
and signature sidecars, but refuses changes that would invalidate a signed metadata
file or publish unsigned metadata for a namespace with a required signing policy.

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
Linux and macOS), and builds on Linux AMD64/ARM64, Windows, and macOS. It uploads
one executable artifact for each runner operating system and architecture.
Go build caching is enabled through `actions/setup-go`.

CI also builds and inspects release archives and native installers. Windows MSI
tests and Linux AMD64/ARM64 Ubuntu DEB and containerized Fedora RPM tests cover
fresh installation, reinstallation/repair, upgrade replacement using isolated
fixtures, uninstall, and preservation of downloaded resources and caches.
Windows checks system PATH registration/removal and rejects downgrades. Installer tests run only on
disposable GitHub-hosted runners, never on a developer workstation.

## Release downloads and installation

Releases include Linux AMD64/ARM64 and Windows AMD64 archives, a macOS archive for the
native architecture of the macOS CI runner, Windows MSI, Linux AMD64/ARM64 DEB/RPM, and
`SHA256SUMS`. macOS filenames use Go's `darwin` platform name and the actual
`amd64` or `arm64` build architecture. Extract an archive and put its `bin`
directory on PATH, or use the appropriate native installer:

```sh
sudo apt install ./dscpkg_1.2.3_amd64.deb
# On an RPM-based distribution:
sudo dnf install ./dscpkg-1.2.3-1.x86_64.rpm
```

On ARM64 Linux, use `dscpkg_1.2.3_arm64.deb` or
`dscpkg-1.2.3-1.aarch64.rpm` instead.

On Windows, run `dscpkg-v1.2.3-windows-amd64.msi` as administrator, or use:

```powershell
msiexec /i dscpkg-v1.2.3-windows-amd64.msi /qn /norestart
```

The MSI installs to `%ProgramFiles%\dscpkg` and adds that directory to the system
PATH; open a new terminal after installation. DEB/RPM install `/usr/bin/dscpkg`.
These are machine-wide CLI installations, not services. No DSC runtime is
bundled or required to install dscpkg.

Use Installed apps on Windows, `sudo apt remove dscpkg`, or `sudo dnf remove
dscpkg` to uninstall. Upgrades replace the executable; uninstall removes the
executable and its installer-managed PATH entry, but never deletes users'
downloaded resource packages, registries, or caches. Linux follows the native
package manager's version ordering; MSI blocks downgrades.

Packages are unsigned. Verify downloaded files against the release's
`SHA256SUMS` before installation. On Linux, use
`sha256sum --check --ignore-missing SHA256SUMS`; on macOS, compare
`shasum -a 256 <download>`; on Windows, compare
`Get-FileHash -Algorithm SHA256 <download>` with its manifest entry. Checksums
detect corruption but do not replace trusted download provenance or signing.

## Building release packages

Packaging requires PowerShell 7.2 or later, Git, Go, and native platform tools.
Windows additionally requires the .NET 8 SDK/runtime (or a compatible installed
toolchain); the project pins WiX SDK 7.0.0. Review the
[WiX license and fee requirements](https://docs.firegiant.com/wix/)
before using that toolchain; its build-time EULA setting is recorded in the
WiX project. This does not change dscpkg's MIT license.

Linux additionally requires Bash, tar, nFPM 2.47.0, `dpkg-deb`, `rpm`, and
`rpm2archive` (provided by Ubuntu's RPM tooling). Install nFPM as a separate build
tool, not a runtime module dependency:

```sh
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0
# Ensure Go's bin directory is on PATH. Go may download nFPM's required toolchain.
```

On each native platform (including native AMD64 or ARM64 Linux), build one
executable with CGO disabled and pass that same binary to the packager. The output
directory must be empty. For example, in PowerShell on Windows:

```powershell
$env:CGO_ENABLED = '0'
go build -trimpath -o dscpkg.exe ./cmd/dscpkg
./packaging/build.ps1 -Version v1.2.3 -BinaryPath ./dscpkg.exe -OutputDirectory ./dist
./packaging/verify.ps1 -Version v1.2.3 -BinaryPath ./dscpkg.exe -Directory ./dist
```

On Linux or macOS, use `CGO_ENABLED=0 go build -trimpath -o dscpkg ./cmd/dscpkg`,
then invoke the same scripts with `pwsh -NoProfile -File`, `-BinaryPath ./dscpkg`,
and the other arguments above. Inspection extracts packages without installing
the product. Windows MSI binding/ICE validation requires a Windows environment
whose installer policy permits validation; do not disable validation to produce
a release.

`packaging/test-version.ps1` tests tag/version boundaries.
`packaging/test-release.ps1` tests release safeguards using an isolated Git
repository and mocked GitHub API, without creating real tags or releases.
CI uses `v0.0.0` for non-release package checks. Separate `v1.0.0`/`v1.0.1`
fixtures exercise upgrades even before the first release; they are never shipped.

## Creating a draft release

After the workflow changes are merged, push a stable `vX.Y.Z` tag pointing to a
reviewed commit already in `main`:

```sh
git fetch origin
git tag v1.2.3 <reviewed-commit>
git push origin refs/tags/v1.2.3
```

The [release workflow](.github/workflows/release.yaml) validates the tag and runs
reusable CI on its exact commit. Lightweight and annotated tags are supported.
Tags must have exactly three numeric components without leading zeroes; RC tags,
build metadata, and other prereleases are unsupported. MSI uses the direct
`X.Y.Z` version: major/minor must be at most 255, and patch at most 65535. These
limits apply to the entire release, not just its MSI.

Every platform's archive and installer contain the same executable bytes. After
all native checks pass, the final job downloads those exact tested assets from
the same workflow run, verifies the expected file sets and SHA-256 checksums,
and rechecks the remote tag. It creates a **draft normal release**, with generated
release notes and a combined `SHA256SUMS`, not a published release or prerelease.
Review its assets, source commit, and workflow results in **Releases**, then
publish manually. The workflow never creates or moves a tag.

An existing release or draft stops the run rather than being overwritten.
Authentication/API failures are errors, not evidence that a release is absent.
If creation partially fails, inspect the draft and uploaded assets; remove an
incomplete draft manually only after review before retrying the unchanged tag.
For source fixes, merge the fix and push a new version tag instead of moving one.

Re-run failed jobs while the original artifacts remain available. A rebuilt
platform must pass its complete package checks again; never skip them to make
a draft. If artifacts have expired, re-run the whole workflow. Installer failure
diagnostics are available as `dscpkg-install-logs-<platform>` artifacts. There is
no automatic publication, signing/notarization, macOS PKG, package repository,
SBOM, or attestation in this flow.
