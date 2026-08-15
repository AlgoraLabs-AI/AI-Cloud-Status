#!/bin/bash
# Build the macOS release artifact: an .app BUNDLE, not a bare executable.
#
# Why a bundle at all, when the binary runs fine on its own: macOS types a bare
# Mach-O as "public.unix-executable", and the only way Finder knows to "open"
# one is to hand it to Terminal.app. Double-clicking the bare binary therefore
# opened a stray Terminal window that OWNED the app — closing that window took
# the app with it. A bundle is typed "com.apple.application-bundle" and is
# launched by launchd directly, with no terminal in the picture.
#
# It also gives the app the identity a bare binary cannot have: a bundle ID, a
# real icon, a Dock entry, and the LaunchServices registration the system-tray
# item wants.
#
# macOS only — iconutil and codesign are Apple tools. Run from the repo root.
set -euo pipefail

VERSION="${1:-dev}"
APP="AI-Cloud-Status.app"
BIN="AI-Cloud-Status"
# CFBundleShortVersionString wants a bare dotted number; the git tag carries a
# leading "v" that Info.plist must not.
PLIST_VERSION="${VERSION#v}"

cd "$(dirname "$0")/.."

echo "==> building $BIN ($VERSION)"
CGO_ENABLED=1 go build \
  -ldflags "-s -w -X github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/ui.Version=${VERSION}" \
  -o "$BIN" .

echo "==> generating AppIcon.icns"
# iconutil insists on a directory named *.iconset, so it gets a scratch parent
# of its own that goes away with the script rather than accumulating in /tmp.
SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT
ICONSET="$SCRATCH/AppIcon.iconset"
mkdir -p "$ICONSET"
cp assets/icon-16.png  "$ICONSET/icon_16x16.png"
cp assets/icon-32.png  "$ICONSET/icon_16x16@2x.png"
cp assets/icon-32.png  "$ICONSET/icon_32x32.png"
cp assets/icon-64.png  "$ICONSET/icon_32x32@2x.png"
cp assets/icon-128.png "$ICONSET/icon_128x128.png"
cp assets/icon-256.png "$ICONSET/icon_128x128@2x.png"
cp assets/icon-256.png "$ICONSET/icon_256x256.png"
iconutil -c icns "$ICONSET" -o AppIcon.icns

echo "==> assembling $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
mv "$BIN" "$APP/Contents/MacOS/$BIN"
mv AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>                  <string>AI-Cloud-Status</string>
  <key>CFBundleDisplayName</key>           <string>AI-Cloud-Status</string>
  <key>CFBundleExecutable</key>            <string>${BIN}</string>
  <key>CFBundleIdentifier</key>            <string>ai.algoralabs.ai-cloud-status</string>
  <key>CFBundlePackageType</key>           <string>APPL</string>
  <key>CFBundleInfoDictionaryVersion</key> <string>6.0</string>
  <key>CFBundleShortVersionString</key>    <string>${PLIST_VERSION}</string>
  <key>CFBundleVersion</key>               <string>${PLIST_VERSION}</string>
  <key>CFBundleIconFile</key>              <string>AppIcon</string>
  <key>LSMinimumSystemVersion</key>        <string>11.0</string>
  <key>NSHighResolutionCapable</key>       <true/>
</dict>
</plist>
PLIST
plutil -lint "$APP/Contents/Info.plist" >/dev/null

# Ad-hoc sign the assembled bundle. This is NOT notarization and does not stop
# Gatekeeper warning on a downloaded copy — it makes the bundle internally
# coherent, so the warning is the ordinary unidentified-developer one rather
# than "the app is damaged", which is what an unsigned bundle around an
# already-signed executable reads as.
echo "==> ad-hoc signing"
codesign --force --deep --sign - "$APP"
codesign --verify --deep --strict "$APP" && echo "    signature verifies"

echo "==> done"
find "$APP" -type f | sed 's|^|    |'
