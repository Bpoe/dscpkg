#!/usr/bin/env bash
set -Eeuo pipefail
trap 'printf "ERROR %s:%s: %s\n" "$0" "$LINENO" "$BASH_COMMAND" >&2' ERR

[[ $# == 3 ]] || { echo "Usage: $0 VERSION BINARY OUTPUT_DIRECTORY" >&2; exit 1; }
root=$(cd "$(dirname "$0")/../.." && pwd)
# shellcheck source=packaging/linux/architecture.sh
source "$root/packaging/linux/architecture.sh"
export VERSION
VERSION=$(pwsh -NoProfile -File "$root/packaging/version.ps1" -Version "$1")
export BINARY_PATH
BINARY_PATH=$(realpath "$2")
output=$(realpath "$3")
cd "$root"
[[ $(go version -m "$BINARY_PATH") == *'GOOS=linux'* ]]
[[ $(go version -m "$BINARY_PATH") == *"GOARCH=$PACKAGE_ARCH"* ]]
nfpm package --config packaging/linux/nfpm.yaml --packager deb \
    --target "$output/dscpkg_${VERSION}_${PACKAGE_ARCH}.deb"
nfpm package --config packaging/linux/nfpm.yaml --packager rpm \
    --target "$output/dscpkg-${VERSION}-1.$RPM_ARCH.rpm"
