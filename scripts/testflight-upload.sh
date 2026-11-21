#!/bin/sh
# Upload the Mac App Store package to App Store Connect (TestFlight) from this Mac.
#
#   sh scripts/testflight-upload.sh [path/to/Banyan.pkg]
#
# Tagged releases upload from CI; this is for a package built here with
# `STORE=mas sh scripts/build-mac.sh`.
#
# The Apple ID comes from $APPLE_ID, or else ~/.config/banyan/apple-id.
# The app-specific password (appleid.apple.com → Sign-In and Security) comes
# from $APPLE_APP_PASSWORD, or else is asked for without echoing it.
set -eu

pkg="${1:-$(cd "$(dirname "$0")/.." && pwd)/dist/appstore/Banyan.pkg}"
config="$HOME/.config/banyan"

test -f "$pkg" || { echo "No package at $pkg. Build it with: STORE=mas sh scripts/build-mac.sh" >&2; exit 1; }

apple_id="${APPLE_ID:-}"
if [ -z "$apple_id" ] && [ -f "$config/apple-id" ]; then
  apple_id="$(tr -d '[:space:]' < "$config/apple-id")"
fi
test -n "$apple_id" || { echo "Set APPLE_ID, or put your Apple ID in $config/apple-id" >&2; exit 1; }

# altool lives in Xcode, not in the Command Line Tools xcode-select may point at.
if [ -z "${DEVELOPER_DIR:-}" ] && [ -d /Applications/Xcode.app/Contents/Developer ]; then
  export DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer
fi
xcrun --find altool >/dev/null 2>&1 || { echo "altool not found: install Xcode, or set DEVELOPER_DIR" >&2; exit 1; }

# Refuse anything not signed for the store: App Store Connect would reject it
# after the upload anyway.
signer="$(pkgutil --check-signature "$pkg" | grep -oE '(3rd Party Mac Developer Installer|Mac Installer Distribution): [^(]+' | head -1 | sed 's/ *$//' || true)"
test -n "$signer" || { echo "$pkg is not signed with a Mac Installer Distribution certificate" >&2; exit 1; }

version="$(tar -xOf "$pkg" Distribution 2>/dev/null | grep -oE 'CFBundleShortVersionString="[^"]+"' | head -1 | cut -d'"' -f2 || true)"
echo "Package:  $pkg"
echo "Version:  ${version:-unknown}"
echo "Signed:   $signer"
echo "Apple ID: $apple_id"

password="${APPLE_APP_PASSWORD:-}"
if [ -z "$password" ]; then
  printf 'App-specific password: ' >&2
  stty -echo; trap 'stty echo' EXIT INT TERM
  read -r password
  stty echo; trap - EXIT INT TERM
  echo >&2
fi
test -n "$password" || { echo "No password given" >&2; exit 1; }

# Pass the password through the environment, not argv, so it stays out of ps.
export BANYAN_ALTOOL_PASSWORD="$password"
xcrun altool --upload-app --type macos --file "$pkg" \
  --username "$apple_id" --password @env:BANYAN_ALTOOL_PASSWORD
echo "Uploaded. It appears in App Store Connect → TestFlight once Apple has processed it (usually 10–30 minutes)."
