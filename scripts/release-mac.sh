#!/bin/sh
# Builds the disk image a release carries: sh scripts/release-mac.sh 2.0.0
#
# Signs Banyan.app with a Developer ID, packs dist/Banyan-mac-arm64.dmg, has
# Apple notarize it and staples the ticket. Nothing is pushed or published.
#
#   SIGN_IDENTITY    the Developer ID Application identity, as `security
#                    find-identity -v -p codesigning` lists it
#   NOTARY_PROFILE   a profile saved with `xcrun notarytool store-credentials`
set -eu

version="${1:-}"
echo "$version" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$' || { echo "usage: sh scripts/release-mac.sh 2.0.0" >&2; exit 1; }
: "${SIGN_IDENTITY:?set SIGN_IDENTITY to a Developer ID Application identity}"
: "${NOTARY_PROFILE:?set NOTARY_PROFILE to a notarytool keychain profile}"
cd "$(dirname "$0")/.."

VERSION="$version" sh scripts/build-mac.sh
codesign --verify --strict dist/Banyan.app

DMG="dist/Banyan-mac-arm64.dmg"
STAGE="$(mktemp -d)"
cp -R dist/Banyan.app "$STAGE/"
ln -s /Applications "$STAGE/Applications"
rm -f "$DMG"
hdiutil create -quiet -volname Banyan -srcfolder "$STAGE" -fs HFS+ -format UDZO "$DMG"
rm -rf "$STAGE"
codesign --force --timestamp --sign "$SIGN_IDENTITY" "$DMG"

xcrun notarytool submit "$DMG" --keychain-profile "$NOTARY_PROFILE" --wait
xcrun stapler staple "$DMG"
spctl --assess --type open --context context:primary-signature "$DMG"
ls -l "$DMG"
