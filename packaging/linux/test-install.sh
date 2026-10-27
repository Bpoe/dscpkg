#!/usr/bin/env bash
set -Eeuo pipefail
trap 'printf "ERROR %s:%s: %s\n" "$0" "$LINENO" "$BASH_COMMAND" >&2' ERR

if [[ $# != 7 || $1 != --disposable || ${GITHUB_ACTIONS:-} != true ||
      ${RUNNER_ENVIRONMENT:-} != github-hosted || $EUID != 0 ]]; then
    echo "Usage (disposable GitHub-hosted runner/container only): $0 --disposable deb|rpm VERSION ASSETS BINARY FIXTURES LOGS" >&2
    exit 1
fi
[[ -x ${DSC_TEST_BIN:?Set DSC_TEST_BIN to the isolated DSC 3.3.0 executable} ]]
root=$(cd "$(dirname "$0")/../.." && pwd)
# shellcheck source=packaging/linux/architecture.sh
source "$root/packaging/linux/architecture.sh"
format=$2
version=${3#v}
assets=$(realpath "$4")
binary=$(realpath "$5")
fixtures=$(realpath "$6")
mkdir -p "$7"
logs=$(realpath "$7")
exec > >(tee "$logs/$format-install.log") 2>&1
case "$format" in
    deb)
        command -v dpkg
        if dpkg-query -W dscpkg; then echo 'dscpkg is already installed.' >&2; exit 1; fi ;;
    rpm)
        command -v rpm
        if rpm -q dscpkg; then echo 'dscpkg is already installed.' >&2; exit 1; fi ;;
    *) echo "Unknown package format: $format" >&2; exit 1 ;;
esac
[[ ! -e /usr/bin/dscpkg ]]
[[ ! -e /usr/bin/dscpkg.dsc.extension.json ]]
[[ ! -e /usr/bin/dscpkg.dsc.resource.json ]]
cache="$HOME/.cache/dscpkg/packages"
[[ ! -e "$cache" ]]
mkdir -p "$cache/.dscpkg" "$cache/test-resource"
sentinels=("$cache/resources.json" "$cache/.dscpkg/metadata" "$cache/test-resource/resource")
for file in "${sentinels[@]}"; do printf 'preserve-me\n' > "$file"; done
installed=false

package_path() {
    if [[ $format == deb ]]; then printf '%s/dscpkg_%s_%s.deb' "$1" "$2" "$PACKAGE_ARCH"
    else printf '%s/dscpkg-%s-1.%s.rpm' "$1" "$2" "$RPM_ARCH"; fi
}
install_package() {
    if [[ $format == deb ]]; then dpkg -i "$1"; else rpm -U --replacepkgs "$1"; fi
    installed=true
}
remove_package() {
    if [[ $format == deb ]]; then dpkg --purge dscpkg; else rpm -e dscpkg; fi
    installed=false
}
preserved() {
    for file in "${sentinels[@]}"; do [[ $(cat "$file") == preserve-me ]]; done
}
verify_install() {
    # Merged bin/sbin layouts may expose the installed file through another PATH alias.
    [[ $(command -v dscpkg) -ef /usr/bin/dscpkg ]]
    cmp "$1" /usr/bin/dscpkg
    cmp "$root/dscpkg.dsc.extension.json" /usr/bin/dscpkg.dsc.extension.json
    cmp "$root/dscpkg.dsc.resource.json" /usr/bin/dscpkg.dsc.resource.json
    env -u DSC_RESOURCE_PATH -u DSC_RESTRICTED_PATH "$DSC_TEST_BIN" --ignore-settings-file \
        extension list DscPkg/Discover -o json | grep -F '"type":"DscPkg/Discover"'
    env -u DSC_RESOURCE_PATH -u DSC_RESTRICTED_PATH "$DSC_TEST_BIN" --ignore-settings-file \
        resource list DscPkg/resources -o json | grep -F '"type":"DscPkg/resources"'
    dscpkg publish -h
    if [[ $format == deb ]]; then
        # dpkg-query, not Bash, expands this field.
        # shellcheck disable=SC2016
        [[ $(dpkg-query -W -f '${Version}' dscpkg) == "$2" ]]
    else
        [[ $(rpm -q --qf '%{VERSION}' dscpkg) == "$2" ]]
    fi
    preserved
}
cleanup() { if $installed; then remove_package; fi; }
trap cleanup EXIT

candidate=$(package_path "$assets" "$version")
install_package "$candidate"
verify_install "$binary" "$version"
rm /usr/bin/dscpkg.dsc.extension.json
rm /usr/bin/dscpkg.dsc.resource.json
install_package "$candidate"
verify_install "$binary" "$version"
remove_package
[[ ! -e /usr/bin/dscpkg ]]
[[ ! -e /usr/bin/dscpkg.dsc.extension.json ]]
[[ ! -e /usr/bin/dscpkg.dsc.resource.json ]]
preserved

if cmp -s "$fixtures/v1.0.0/dscpkg" "$fixtures/v1.0.1/dscpkg"; then
    echo 'Upgrade fixture executables must differ.' >&2
    exit 1
fi
install_package "$(package_path "$fixtures/v1.0.0/assets" 1.0.0)"
verify_install "$fixtures/v1.0.0/dscpkg" 1.0.0
printf '{"stale":"upgrade fixture"}\n' > /usr/bin/dscpkg.dsc.extension.json
printf '{"stale":"upgrade fixture"}\n' > /usr/bin/dscpkg.dsc.resource.json
install_package "$(package_path "$fixtures/v1.0.1/assets" 1.0.1)"
verify_install "$fixtures/v1.0.1/dscpkg" 1.0.1
remove_package
[[ ! -e /usr/bin/dscpkg ]]
[[ ! -e /usr/bin/dscpkg.dsc.extension.json ]]
[[ ! -e /usr/bin/dscpkg.dsc.resource.json ]]
preserved
echo "$format install, reinstall, upgrade, uninstall and data retention passed."
