#!/usr/bin/env bash
set -euo pipefail

[[ $# == 3 ]] || { echo "Usage: $0 VERSION BINARY OUTPUT_DIRECTORY" >&2; exit 1; }
root=$(cd "$(dirname "$0")/../.." && pwd)
export VERSION
VERSION=$(pwsh -NoProfile -File "$root/packaging/version.ps1" -Version "$1")
export BINARY_PATH
BINARY_PATH=$(realpath "$2")
output=$(realpath "$3")
cd "$root"
[[ $(go version -m "$BINARY_PATH") == *'GOOS=linux'* ]]
[[ $(go version -m "$BINARY_PATH") == *'GOARCH=amd64'* ]]
nfpm package --config packaging/linux/nfpm.yaml --packager deb \
    --target "$output/dscpkg_${VERSION}_amd64.deb"
nfpm package --config packaging/linux/nfpm.yaml --packager rpm \
    --target "$output/dscpkg-${VERSION}-1.x86_64.rpm"
