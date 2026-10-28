#!/usr/bin/env bash
# Extract the SHA-256-pinned official codec; never install it system-wide.
set -euo pipefail
arch=${1:-$(uname -m)}
out=${2:-dist}
case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo 'Unsupported architecture' >&2; exit 1 ;; esac
version=3.2.0
case "$(uname -s)-$arch" in
  Linux-amd64) asset="libjpeg-turbo-official_${version}_amd64.deb"; sha=21297da4a4eb34ebefc54afca5d8dd86c0fdd6a9dfe49b1b962c5d1eeeafd8ec ;;
  Linux-arm64) asset="libjpeg-turbo-official_${version}_arm64.deb"; sha=4bf51ee5dc130052ee19ac47cc2b7b613961dcd9519398fce976bead58be36eb ;;
  Darwin-*) asset="libjpeg-turbo-${version}.dmg"; sha=e69c4a3bb6bf8d6cc4f329ac9c8cf36c033d69e8f6b02e45cc324cf697ddfe1f ;;
  *) echo 'Only Linux and macOS are supported' >&2; exit 1 ;;
esac
mkdir -p "$out/turbojpeg-licenses"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
mounted=false
cleanup() {
  if $mounted; then hdiutil detach "$work/mount" -quiet || true; fi
  rm -rf "$work"
}
trap cleanup EXIT
curl --fail --location --retry 3 --connect-timeout 30 "https://github.com/libjpeg-turbo/libjpeg-turbo/releases/download/$version/$asset" --output "$work/$asset"
if command -v sha256sum >/dev/null; then actual=$(sha256sum "$work/$asset" | awk '{print $1}'); else actual=$(shasum -a 256 "$work/$asset" | awk '{print $1}'); fi
[[ "$actual" == "$sha" ]] || { echo 'TurboJPEG checksum mismatch' >&2; exit 1; }
if [[ $(uname -s) == Linux ]]; then
  dpkg-deb --extract "$work/$asset" "$work/extracted"
  name=libturbojpeg.so
  library=$(find "$work/extracted" -type f -name 'libturbojpeg.so.*' -print -quit)
else
  mkdir "$work/mount"
  hdiutil attach "$work/$asset" -mountpoint "$work/mount" -nobrowse -readonly -quiet
  mounted=true
  package=$(find "$work/mount" -name '*.pkg' -prune -print -quit)
  [[ -n "$package" ]] || { echo 'Codec installer package is missing' >&2; exit 1; }
  pkgutil --expand-full "$package" "$work/extracted"
  name=libturbojpeg.dylib
  library=$(find "$work/extracted" -type f -name 'libturbojpeg*.dylib' -print -quit)
fi
[[ -n "$library" ]] || { echo 'Codec library is missing' >&2; exit 1; }
cp "$library" "$out/$name"
for license in LICENSE.md README.ijg README.md; do
  file=$(find "$work/extracted" -type f -name "$license" -print -quit)
  [[ -n "$file" ]] || { echo "Missing codec license: $license" >&2; exit 1; }
  cp "$file" "$out/turbojpeg-licenses/$license"
done
if [[ $(uname -s) == Darwin ]]; then
  mach_arch=arm64; [[ "$arch" != amd64 ]] || mach_arch=x86_64
  lipo "$out/$name" -verify_arch "$mach_arch"
fi
echo "$out/$name"
