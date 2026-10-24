#!/usr/bin/env bash

case "$(uname -m)" in
    x86_64) export PACKAGE_ARCH=amd64 RPM_ARCH=x86_64 ;;
    aarch64|arm64) export PACKAGE_ARCH=arm64 RPM_ARCH=aarch64 ;;
    *) echo "Unsupported Linux package architecture: $(uname -m)" >&2; exit 1 ;;
esac
