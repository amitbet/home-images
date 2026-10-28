#!/usr/bin/env bash
# Download a complete release bundle, verify its checksum, then install it.
set -euo pipefail
version=latest
if [[ ${1:-} == --version ]]; then version=${2:?Missing release version}; shift 2; fi
[[ "$version" =~ ^[A-Za-z0-9._-]+$ ]] || { echo 'Invalid release version' >&2; exit 1; }
case $(uname -s) in Linux) os=linux ;; Darwin) os=darwin ;; *) echo 'Use install.ps1 on Windows' >&2; exit 1 ;; esac
case $(uname -m) in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo 'Unsupported architecture' >&2; exit 1 ;; esac
base="https://github.com/amitbet/home-images/releases/download/$version"
[[ "$version" != latest ]] || base=https://github.com/amitbet/home-images/releases/latest/download
base=${HOME_IMAGES_RELEASE_BASE_URL:-$base}
asset="home-images-$os-$arch.tar.gz"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl --fail --location --retry 3 --connect-timeout 30 "$base/$asset" --output "$work/$asset"
curl --fail --location --retry 3 --connect-timeout 30 "$base/SHA256SUMS" --output "$work/SHA256SUMS"
expected=$(awk -v name="$asset" '$2==name {print $1}' "$work/SHA256SUMS")
[[ "$expected" =~ ^[a-fA-F0-9]{64}$ ]] || { echo 'Release checksum is missing or ambiguous' >&2; exit 1; }
if command -v sha256sum >/dev/null; then actual=$(sha256sum "$work/$asset" | awk '{print $1}'); else actual=$(shasum -a 256 "$work/$asset" | awk '{print $1}'); fi
[[ "$actual" == "$expected" ]] || { echo 'Release checksum mismatch' >&2; exit 1; }
mkdir "$work/package"
tar -xzf "$work/$asset" -C "$work/package"
bash "$work/package/install-unix.sh" "$@"
