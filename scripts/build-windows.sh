#!/bin/sh
# Build the Windows app and its installer: VERSION=2.1.0 sh scripts/build-windows.sh [exe|installer]
#
# With no argument it makes both. "exe" stops after Banyan.exe and "installer"
# packs the dist/windows already there, so a release can have Banyan.exe
# code-signed before it goes into the installer, and the installer after.
#
# Runs on Windows (Git Bash, as on GitHub's Windows runners). Produces
#   dist/windows/Banyan.exe        the app, with pdfium.dll beside it
#   dist/Banyan-windows-x64-setup.exe   the installer (when makensis is present)
#
# PDFium is fetched at the release pinned in internal/printing/pdfium_windows.go
# and checked against its hash: the app refuses any other build of it.
set -eu
cd "$(dirname "$0")/.."

stage="${1:-all}"
case "$stage" in all|exe|installer) ;; *) echo "usage: sh scripts/build-windows.sh [exe|installer]" >&2; exit 1 ;; esac
version="${VERSION:-$(node -p "require('./package.json').version")-dev}"
numeric="$(echo "$version" | sed 's/-dev$//').0"
out=dist/windows

if [ "$stage" != installer ]; then
pdfium_release="chromium/8111"
pdfium_tgz_sha256="3452d11031050ec65e57f180215b57b9e6d465e3fbedafa8a77ad90885c7d23e"
pdfium_dll_sha256="$(sed -n 's/.*pdfiumSHA256  *= *"\([0-9a-f]*\)".*/\1/p' internal/printing/pdfium_windows.go)"
grep -q "PdfiumVersion = \"$pdfium_release\"" internal/printing/pdfium_windows.go \
  || { echo "pdfium release here and in pdfium_windows.go disagree" >&2; exit 1; }

rm -rf "$out" && mkdir -p "$out"

# PDFium, checked twice: the archive, then the DLL the app will load.
# A Windows path (D:\a\_temp) is turned into /d/a/_temp: tar reads "D:" as a host.
tmp="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
command -v cygpath >/dev/null 2>&1 && tmp="$(cygpath -u "$tmp")"
cache="$tmp/banyan-pdfium"
mkdir -p "$cache"
if [ ! -f "$cache/pdfium-win-x64.tgz" ] || ! echo "$pdfium_tgz_sha256  $cache/pdfium-win-x64.tgz" | sha256sum -c - >/dev/null 2>&1; then
  curl -fsSL -o "$cache/pdfium-win-x64.tgz" \
    "https://github.com/bblanchon/pdfium-binaries/releases/download/$(echo "$pdfium_release" | sed 's#/#%2F#')/pdfium-win-x64.tgz"
fi
echo "$pdfium_tgz_sha256  $cache/pdfium-win-x64.tgz" | sha256sum -c -
rm -rf "$cache/x" && mkdir -p "$cache/x"
tar -xzf "$cache/pdfium-win-x64.tgz" -C "$cache/x" bin/pdfium.dll LICENSE licenses
echo "$pdfium_dll_sha256  $cache/x/bin/pdfium.dll" | sha256sum -c -
cp "$cache/x/bin/pdfium.dll" "$out/"
mkdir -p "$out/licenses/pdfium"
cp "$cache/x/LICENSE" "$out/licenses/pdfium/LICENSE.txt"
cp "$cache/x/licenses/"* "$out/licenses/pdfium/"
cp LICENSE "$out/licenses/Banyan-LICENSE.txt"

# The icon and version go into the executable as a Windows resource.
GOWORK=off go run github.com/tc-hib/go-winres@v0.3.3 simply \
  --arch amd64 --out cmd/banyan/rsrc \
  --icon build/windows/icon.ico --manifest gui \
  --product-name Banyan --file-description "Banyan" \
  --product-version "$version" --file-version "$numeric" \
  --original-filename Banyan.exe
trap 'rm -f cmd/banyan/rsrc_windows_amd64.syso' EXIT

GOWORK=off CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags "-H windowsgui -s -w -X main.version=$version" \
  -o "$out/Banyan.exe" ./cmd/banyan
ls -l "$out"
rm -f cmd/banyan/rsrc_windows_amd64.syso
fi
[ "$stage" = exe ] && exit 0

test -s "$out/Banyan.exe" || { echo "no $out/Banyan.exe to pack" >&2; exit 1; }
if command -v makensis >/dev/null 2>&1; then
  makensis -V2 -DVERSION="$version" -DNUMERIC="$numeric" -DSOURCE="$(cd "$out" && pwd -W 2>/dev/null || pwd)" \
    -DOUTFILE="$(pwd -W 2>/dev/null || pwd)/dist/Banyan-windows-x64-setup.exe" build/windows/installer.nsi
  ls -l dist/Banyan-windows-x64-setup.exe
else
  echo "makensis not found: no installer was made" >&2
fi
