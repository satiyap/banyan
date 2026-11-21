#!/bin/sh
# Builds Banyan.app for this Mac into dist/.
#
#   sh scripts/build-mac.sh                    a development build, ad-hoc signed
#   VERSION=2.0.0 SIGN_IDENTITY=<id> sh ...    a release build, signed for notarization
#
# The Mac App Store build, from the same code, into dist/appstore/:
#   STORE=mas sh scripts/build-mac.sh          sandboxed, ad-hoc signed, to try locally
#   STORE=mas VERSION=2.5.0 TEAM_ID=<team> MAS_SIGN_IDENTITY=<Apple Distribution id> \
#     PROVISION_PROFILE=<path> INSTALLER_IDENTITY=<Mac Installer Distribution id> sh ...
#                                              signed for the store, with Banyan.pkg to upload
# The store build is sandboxed, has no updater of its own (the App Store updates
# it), and moves the direct download's data folder into its container once.
#
# A version ending in -dev keeps its own data folder and never prints.
set -eu
cd "$(dirname "$0")/.."

VERSION="${VERSION:-2.1.0-dev}"
STORE="${STORE:-direct}"
TAGS="production"
OUT="dist"
if [ "$STORE" = mas ]; then
  TAGS="production,mas"
  OUT="dist/appstore"
elif [ "$STORE" != direct ]; then
  echo "STORE is direct or mas" >&2; exit 1
fi
APP="$OUT/Banyan.app"

# An unrelated go.work may sit in a parent folder.
export GOWORK=off
export CGO_ENABLED=1
export MACOSX_DEPLOYMENT_TARGET=12.0
export CGO_CFLAGS="-mmacosx-version-min=12.0"
export CGO_LDFLAGS="-mmacosx-version-min=12.0"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

go build -tags "$TAGS" -trimpath -buildvcs=false -ldflags="-w -s -X main.version=${VERSION}" \
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
  <key>NSLocalNetworkUsageDescription</key><string>Banyan looks for your printer on the local network so it can check that it's ready before printing a cheque.</string>
  <key>NSBonjourServices</key>
  <array>
    <string>_ipp._tcp</string>
    <string>_ipps._tcp</string>
  </array>
</dict>
</plist>
PLIST

if [ "$STORE" = mas ]; then
  # What the store needs on top of the shared Info.plist: no export-compliance
  # question (HTTPS only), and the one-time move of the data folder.
  # Its own identifier: the direct download's is tied to its updater.
  /usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier com.banyan.desk" \
    -c "Add :ITSAppUsesNonExemptEncryption bool false" "$APP/Contents/Info.plist"
  cp build/appstore/container-migration.plist "$APP/Contents/Resources/container-migration.plist"
  ENTITLEMENTS="$(mktemp -d)/entitlements.plist"
  cp build/appstore/entitlements.plist "$ENTITLEMENTS"
  if [ -n "${TEAM_ID:-}" ]; then
    /usr/libexec/PlistBuddy -c "Add :com.apple.application-identifier string ${TEAM_ID}.com.banyan.desk" \
      -c "Add :com.apple.developer.team-identifier string ${TEAM_ID}" "$ENTITLEMENTS"
  fi
  if [ -n "${PROVISION_PROFILE:-}" ]; then
    cp "$PROVISION_PROFILE" "$APP/Contents/embedded.provisionprofile"
  fi
  codesign --force --sign "${MAS_SIGN_IDENTITY:--}" --entitlements "$ENTITLEMENTS" "$APP"
  if [ -n "${INSTALLER_IDENTITY:-}" ]; then
    productbuild --component "$APP" /Applications --sign "$INSTALLER_IDENTITY" "$OUT/Banyan.pkg"
  fi
elif [ -n "${SIGN_IDENTITY:-}" ]; then
  codesign --force --options runtime --timestamp --sign "$SIGN_IDENTITY" "$APP"
else
  codesign --force --sign - "$APP" >/dev/null 2>&1
fi
du -sh "$APP"
