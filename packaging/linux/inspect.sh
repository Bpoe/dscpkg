#!/usr/bin/env bash
set -Eeuo pipefail
trap 'printf "ERROR %s:%s: %s\n" "$0" "$LINENO" "$BASH_COMMAND" >&2' ERR

[[ $# == 3 ]] || { echo "Usage: $0 VERSION BINARY OUTPUT_DIRECTORY" >&2; exit 1; }
root=$(cd "$(dirname "$0")/../.." && pwd)
version=$(pwsh -NoProfile -File "$root/packaging/version.ps1" -Version "$1")
binary=$(realpath "$2")
output=$(realpath "$3")
deb="$output/dscpkg_${version}_amd64.deb"
rpm="$output/dscpkg-${version}-1.x86_64.rpm"
[[ $(dpkg-deb -f "$deb" Package) == dscpkg ]]
[[ $(dpkg-deb -f "$deb" Version) == "$version" ]]
[[ $(dpkg-deb -f "$deb" Architecture) == amd64 ]]
[[ -z $(dpkg-deb -f "$deb" Depends) ]]
[[ $(rpm -qp --qf '%{NAME} %{VERSION} %{RELEASE} %{ARCH}' "$rpm") == "dscpkg $version 1 x86_64" ]]
[[ -z $(rpm -qp --scripts "$rpm") ]]
rpm --checksig "$rpm"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
dpkg-deb --extract "$deb" "$stage/deb"
dpkg-deb --control "$deb" "$stage/control"
[[ $(find "$stage/control" -type f ! -name control ! -name md5sums | wc -l) == 0 ]]
mkdir "$stage/rpm"
rpm2archive -n < "$rpm" > "$stage/rpm.tar"
tar -xf "$stage/rpm.tar" -C "$stage/rpm"
for format in deb rpm; do
    extracted="$stage/$format"
    diff <(printf '%s\n' usr/bin/dscpkg usr/share/doc/dscpkg/README.md usr/share/licenses/dscpkg/LICENSE | sort) \
         <(find "$extracted" -type f -printf '%P\n' | sort)
    [[ $(stat -c %a "$extracted/usr/bin/dscpkg") == 755 ]]
    cmp "$binary" "$extracted/usr/bin/dscpkg"
    cmp "$root/README.md" "$extracted/usr/share/doc/dscpkg/README.md"
    cmp "$root/LICENSE" "$extracted/usr/share/licenses/dscpkg/LICENSE"
    "$extracted/usr/bin/dscpkg" publish -h
done
dpkg-deb --contents "$deb" | awk '$2 != "root/root" { bad=1 } END { exit bad }'
rpm -qp --qf '[%{FILEUSERNAME} %{FILEGROUPNAME}\n]' "$rpm" |
    awk '$0 != "root root" { bad=1 } END { exit bad }'
echo 'DEB/RPM metadata, permissions, and identical executable payloads verified.'
