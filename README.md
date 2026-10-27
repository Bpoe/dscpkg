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
For each resource, the first repository that can satisfy the request is selected.
With `--version`, the resource version is exact. Without it, dscpkg selects the
newest compatible stable resource version **within that repository**, not the
newest version across all repositories. Numeric version components and prerelease
precedence are compared numerically, not lexically. Latest selection excludes
prereleases and resource versions without a package archive for the target
OS/architecture; it also considers older package versions when necessary.
A later repository or older resource version is never used to bypass malformed
metadata, integrity failures, or signing errors.

```sh
dscpkg install \
  --repository https://resources.example.com \
  --repository https://public.example.com \
  --resource Microsoft.GuestConfiguration/users \
  --version 2026-06-30-preview
```

`--packages-dir` selects the package cache and registry directory. All commands,
including discovery and DSC resource operations, also honor
`DSCPKG_PACKAGES_DIR`; the flag takes precedence. Without either override it
uses `<user cache directory>/dscpkg/packages`. `--platform` overrides the detected
`os_arch` archive key, for example `linux_amd64`.

## Declare installed resources with DscPkg/resources

DSC **3.3.0** can discover the native `DscPkg/resources` resource directly beside
the dscpkg executable. It does not need to install itself through dscpkg.
Use [`examples/00-resources.yaml`](examples/00-resources.yaml) as a dependency
configuration:

```yaml
$schema: https://aka.ms/dsc/schemas/v3/bundled/config/document.json
resources:
  - name: LinuxGroups
    type: DscPkg/resources
    properties:
      resource: DscExtras.Linux/groups
      repositories:
        - https://bpoe.github.io/dsc-extras/
  - name: LinuxServices
    type: DscPkg/resources
    properties:
      resource: DscExtras.Linux/services
      repositories:
        - https://bpoe.github.io/dsc-extras/
```

Run `sudo dsc config set --file 00-resources.yaml` on compatible Linux.
The first instance installs the latest compatible shared `dscextras/linux`
package; the second recognizes its services manifest without another download,
registration, or executable copy. Subsequent configurations can use both resources
through `DscPkg/Discover`, with no per-package `DSC_RESOURCE_PATH` changes.
Use the same identity for installation and subsequent DSC runs.

The instance properties are:

- `resource` (required): DSC type, such as `DscExtras.Linux/groups`.
- `version` (optional): exact resource version. Add `version: 0.0.1` to pin it.
- `repositories` (optional): ordered origins used only when installation is
  necessary. No implicit repository is configured; supply these when applying
  a configuration to an empty store.
- `package`, `packageVersion`, `path`: read-only observed package identity,
  package version, and absolute installation directory.

`version` and `packageVersion` are independent: the latter describes the package
providing the resource, not the desired resource version. CLI
`--package-version` remains an independent exact selector.

Get reports the exact installed version, or omits `version` and the observed
package fields when no matching resource is installed. Without a version,
it reports the newest locally installed matching version. Test uses that same
local inspection and reports `_inDesiredState` and differing properties using
DSC's `stateAndDiff` contract. A missing resource reports `version` as differing,
including when no exact version was requested. **Neither Get nor Test contacts repositories,
creates a store, or writes state.** A compatible installation of any version
satisfies an unversioned desired state, including one supplied by a shared package
without an individual registry entry.

Set first checks local compliance, then calls the same `Client.Install` operation
as `dscpkg install` only when needed. **Latest-on-install is not automatic
updating**: an unversioned Set never upgrades a compliant installed resource,
even if repositories publish newer versions. Repeated Set works offline and
does not download again. Pin versions for reproducible installation. Older
package versions remain installed; no implicit cleanup runs.

The underlying commands accept properties as one JSON object on stdin:

```sh
echo '{"resource":"DscExtras.Linux/groups"}' | dscpkg resource get
echo '{"resource":"DscExtras.Linux/groups"}' | dscpkg resource test
echo '{"resource":"DscExtras.Linux/groups","repositories":["https://bpoe.github.io/dsc-extras/"]}' | dscpkg resource set
dscpkg resource export
```

Test returns two JSON Lines: the observed state including `_inDesiredState`,
then an array of differing property names. Export needs no input and emits one
`DscPkg/resources` instance per installed resource type/exact-version pair, in
deterministic, deduplicated JSON Lines. It includes unregistered resources from
shared packages, never contacts repositories, and does not modify the store.
Only Export adds `_name`, using the existing resource type and exact version
directly, for example `"DscExtras.Linux/groups@0.1.1"`. DSC uses this as the
configuration instance name and removes `_name` from the generated properties.
Get, Test, and Set do not emit `_name`.
Use `dsc resource export --resource DscPkg/resources` to obtain a DSC configuration.
Export filtering is disabled.

Get/Test/Export parse JSON and YAML native manifests and the `resources` entries
of `.dsc.manifests.*` lists; a list file is not itself a resource instance.
Adapter-backed entries (`.dsc.adaptedresource.*` or `requireAdapter`) are
deliberately excluded with a diagnostic: an offline inventory cannot establish
whether their external adapter is available. Discovery still advertises those
files unchanged for DSC to handle. Invalid native manifest identities or corrupt
platform provenance fail explicitly; legacy packages without provenance are
skipped with reinstall guidance, as in discovery.

Export cannot recover repository origins reliably and therefore omits them.
When reapplying exported configuration to a machine without those resources,
add `repositories` to the exported instances. Observed package/path fields are
not desired-state constraints.

### dscd and persistent DSC server discovery

The isolated DSC 3.3.0 integration test runs `00-resources.yaml` and then a
configuration using a newly installed resource **in the same `dsc server`
process**. Default `preDeployment` discovery retains stale resource lookup state
and the second configuration fails with `Resource not found`. Add the following
to the **consuming configuration** to refresh discovery:

```yaml
directives:
  resourceDiscovery: duringDeployment
```

The same-server test succeeds with this directive. Separate DSC CLI processes
already see newly installed packages without it. This change does not modify
dscd or its configuration ordering. It implements declarative local dependencies,
not Steve's proposed installation extension, remote discovery, or DSC's future
automatic resource-acquisition protocol.

## Discover installed resources with DSC

With DSC **3.3.0 or later**, dscpkg's `DscPkg/Discover` extension exposes locally
installed packages without adding every package directory to `DSC_RESOURCE_PATH`.
The native installers and release archives place `dscpkg.dsc.extension.json`
and `dscpkg.dsc.resource.json` alongside the executable. DSC finds the extension
manifest through PATH and runs
`dscpkg discover`. Verify the extension and resources in a new terminal:

```sh
dsc extension list DscPkg/Discover
dsc resource list
dsc resource get --resource DscExtras.Linux/groups --input '{"name":"root"}'
```

For example, on supported Linux systems, installing
`DscExtras.Linux/groups` from a repository publishing the shared `dscextras/linux`
package also exposes its `DscExtras.Linux/services` manifest. Both resources run
the package's existing `dsclinux` executable in the original package directory.
Packages are never split into resource-specific directories or copied for
discovery. A newly launched DSC process sees newly installed packages immediately;
there is no service to restart.

The public example package can be installed on Linux with:

```sh
dscpkg install --repository https://bpoe.github.io/dsc-extras/ \
  --resource DscExtras.Linux/groups
dsc resource list 'DscExtras.Linux/*'
dsc resource get --resource DscExtras.Linux/groups --input '{"name":"root"}'
dsc resource get --resource DscExtras.Linux/services \
  --input '{"name":"systemd-journald.service"}'
```

The `get` examples only read state; the service example requires systemd.

Inspect the extension's output manually:

```sh
dscpkg discover
dscpkg discover --packages-dir /path/to/packages
```

Each stdout line is a JSON object with an absolute `manifestPath`. Discovery
recursively enumerates installed package-version directories, recognizing
`.dsc.resource.json`, `.yaml`, and `.yml`, plus DSC's
`.dsc.adaptedresource.*` and `.dsc.manifests.*` formats. Paths are sorted and
deduplicated; an empty or missing store succeeds with no stdout output. Discovery
does not follow symlinks, inspect inherited `DSC_RESOURCE_PATH` directories, write
files, acquire a write lock, query repositories, or download anything. Operational
errors go to stderr with a nonzero exit code. DSC validates resource manifests
when loading them.

### Platform provenance and older packages

New installations record the selected archive's exact `os_arch` platform in
`<namespace>/<package>/.<version>.dscpkg.json`, beside (not inside) the
package-version directory. Verified archive contents remain unchanged. Discovery
advertises only packages whose recorded OS **and architecture** match the running
dscpkg executable, never inferring compatibility from a package or resource name.
An incompatible cached version is not reused by install/update for another
platform; use separate `--packages-dir` stores for different target platforms.
Cleanup removes a deleted version's provenance along with that version.

Packages installed by older dscpkg versions have no reliable platform provenance.
Discovery excludes them and writes reinstall guidance to stderr, while still
returning verified compatible packages successfully. Existing install/update cache
reuse and `dscpkg env` remain available for those legacy packages, but do not
retroactively assert their platform. To migrate, move the affected version
directory **outside the package store**, then reinstall its resources with the
current dscpkg and the intended platform. Re-running install without moving the
legacy directory reuses the old cache and cannot establish provenance. Do not
fabricate sidecars or copy them between platforms. Corrupt provenance is an error;
move the affected version aside and reinstall to repair it.

### Installation scope and package-store selection

Machine-wide MSI/DEB/RPM installation registers the **CLI, extension, and resource**, not a
machine-wide resource store. By default discovery uses the invoking identity's
`<user cache directory>/dscpkg/packages`: `%LOCALAPPDATA%\dscpkg\packages` on
Windows, `${XDG_CACHE_HOME:-$HOME/.cache}/dscpkg/packages` on Linux, and
`$HOME/Library/Caches/dscpkg/packages` on macOS. Installing packages as one account
does not expose them to another account or service identity. For example, dscd
running as LocalSystem or root does not use an interactive user's store.

For a custom or shared store, set `DSCPKG_PACKAGES_DIR` in the environment of
both the CLI and the DSC/dscd process. This selects one consistent store for
installation, resource state, and extension discovery without editing manifests.
For example, an administrator can set it to `/var/lib/dscpkg/packages` for a
system service. This is an explicit scope override, not a new default or layout.
An interactive user and `sudo`/root may have different environments, so configure
the variable for the service identity as well. Do not modify installer-owned
manifests, which upgrades replace.
Every invoking identity needs access to the store and executable; DSC executes
package code with that identity's permissions. Share stores only with trusted
writers.

### Interaction with DSC_RESOURCE_PATH

DSC 3.3.0 searches PATH for manifests when `DSC_RESOURCE_PATH` is **unset**.
If that variable is explicitly set, DSC searches those directories instead of
PATH for the extension manifest. Keep any existing entries, but also include
the directory containing both dscpkg manifests once (for example,
`%ProgramFiles%\dscpkg` or `/usr/bin`). The `dscpkg` executable must remain on PATH.
You do **not** need to add individual package directories. `DSC_RESTRICTED_PATH`
and DSC resource-path policy/settings can similarly restrict discovery; the
extension and its executable must be allowed by that configuration.

This extension is strictly **local discovery** of already installed packages.
Install dependencies explicitly with `dscpkg install` or declare them with
`DscPkg/resources`; no future remote-install protocol is implemented.

## Commands

Install the latest compatible stable resource version:

```sh
dscpkg install --repository https://resources.example.com \
  --resource Microsoft.GuestConfiguration/users
```

Or pin an exact resource version and optionally an independent exact package version:

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

For older DSC versions or explicit legacy shell integration, the CLI cannot
change its parent shell's environment. For Bash, import the printed value:

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
the package's `*.dsc.resource.json` manifests directly. This legacy mechanism does
not apply the discovery extension's platform filtering; prefer the extension
with DSC 3.3.0 or later.

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

CI also downloads DSC 3.3.0 into runner-local temporary directories and runs:

```powershell
./tests/test-discovery.ps1 -DscPath /path/to/dsc
```

This validates both manifests against the official bundled schemas exported by
that engine and runs `go test -tags=integration -count=1 -v ./tests`. Tests build
safe shared-executable resources, publish to a loopback HTTP server, install
through the CLI and native DSC resource into isolated per-user stores, and
exercise all four resource operations, instance-schema validation of Export,
one-download shared-package installation, offline idempotence, platform exclusion,
fresh-process discovery, same-server `preDeployment`/`duringDeployment` behavior,
and explicit `DSC_RESOURCE_PATH` bootstrap. They never install DSC system-wide or
change the developer's package store. To use an existing DSC 3.3.0 directly, set
`DSC_TEST_BIN` and run the tagged Go tests.

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
directory on PATH, keeping **all three files** together: `dscpkg` (`dscpkg.exe`
on Windows), `dscpkg.dsc.extension.json`, and `dscpkg.dsc.resource.json`.
This also applies to manual ZIP installation: copy all three files from `bin`
into a directory on PATH, open a new terminal, and verify with
`dsc extension list DscPkg/Discover` and `dsc resource list DscPkg/resources`.
For a source build, copy both repository manifests alongside the executable. If
`DSC_RESOURCE_PATH` is set, follow the bootstrap instructions above. Alternatively,
use the appropriate native installer:

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
Both install the discovery extension and resource manifests alongside the executable.
These are machine-wide CLI installations, not services. No DSC runtime is
bundled or required to install dscpkg.

Use Installed apps on Windows, `sudo apt remove dscpkg`, or `sudo dnf remove
dscpkg` to uninstall. Upgrades replace the executable and both manifests;
uninstall removes all three and the installer's managed PATH entry, but never deletes users'
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
