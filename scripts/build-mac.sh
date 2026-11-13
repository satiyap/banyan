#!/bin/sh
# Builds Banyan.app for this Mac into dist/.
#
#   sh scripts/build-mac.sh                    a development build, ad-hoc signed
#   VERSION=2.0.0 SIGN_IDENTITY=<id> sh ...    a release build, signed for notarization
#
# A version ending in -dev keeps its own data folder and never prints.
set -eu
cd "$(dirname "$0")/.."

VERSION="${VERSION:-2.0.0-dev}"
OUT="dist"
APP="$OUT/Banyan.app"

# An unrelated go.work may sit in a parent folder.
export GOWORK=off
export CGO_ENABLED=1
export MACOSX_DEPLOYMENT_TARGET=12.0
export CGO_CFLAGS="-mmacosx-version-min=12.0"
export CGO_LDFLAGS="-mmacosx-version-min=12.0"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

go build -tags production -trimpath -buildvcs=false -ldflags="-w -s -X main.version=${VERSION}" \
  -o "$APP/Contents/MacOS/Banyan" ./cmd/banyan

# The icon, from the same drawing the Electron build used.
ICONSET="$(mktemp -d)/icon.iconset"
mkdir -p "$ICONSET"
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" build/icon.png --out "$ICONSET/icon_${size}x${size}.png" >/dev/null
  double=$((size * 2))
  sips -z "$double" "$double" build/icon.png --out "$ICONSET/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/icon.icns"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleName</key><string>Banyan</string>
  <key>CFBundleDisplayName</key><string>Banyan</string>
  <key>CFBundleExecutable</key><string>Banyan</string>
  <key>CFBundleIdentifier</key><string>com.banyanrwa.chequedesk</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleIconFile</key><string>icon</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>LSApplicationCategoryType</key><string>public.app-category.finance</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

if [ -n "${SIGN_IDENTITY:-}" ]; then
  codesign --force --options runtime --timestamp --sign "$SIGN_IDENTITY" "$APP"
else
  codesign --force --sign - "$APP" >/dev/null 2>&1
fi
du -sh "$APP"
