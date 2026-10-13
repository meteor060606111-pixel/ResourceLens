#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"

resourcelens_arch="${RESOURCELENS_ARCH:-$(uname -m)}"
if [ "$resourcelens_arch" = x86_64 ]; then resourcelens_arch=amd64; fi
case "$resourcelens_arch" in arm64|amd64) ;; *) echo 'RESOURCELENS_ARCH must be arm64 or amd64' >&2; exit 1;; esac
resourcelens_go="${GO_EXECUTABLE:-go}"
resourcelens_python="${PYTHON_EXECUTABLE:-python3}"
resourcelens_tools="${RESOURCELENS_TOOLS_DIR:-$PWD/video-engine}"
resourcelens_out="${RESOURCELENS_OUTPUT_DIR:-$PWD/release/macos-$resourcelens_arch}"
resourcelens_version="$(sed -n 's/^const appVersion = "\(.*\)"/\1/p' app/features.go)"
resourcelens_bundle="$resourcelens_out/ResourceLens.app"

if [ "$(uname -s)" != Darwin ]; then echo 'Build on macOS with Xcode Command Line Tools.' >&2; exit 1; fi
command -v "$resourcelens_go" >/dev/null
command -v npm >/dev/null

if [ "${RESOURCELENS_FETCH_TOOLS:-1}" = 1 ]; then
  "$resourcelens_python" installer/macos/prepare-tools.py --arch "$resourcelens_arch" --output "$resourcelens_tools"
fi
for resourcelens_tool in yt-dlp ffmpeg ffprobe node; do
  test -x "$resourcelens_tools/$resourcelens_tool"
  resourcelens_macho_arch="$resourcelens_arch"
  if [ "$resourcelens_arch" = amd64 ]; then resourcelens_macho_arch=x86_64; fi
  lipo "$resourcelens_tools/$resourcelens_tool" -verify_arch "$resourcelens_macho_arch"
  # Ship only standalone executables: users do not need Homebrew libraries.
  if otool -L "$resourcelens_tools/$resourcelens_tool" | awk '/compatibility version/ {print $1}' | grep -Ev '^(/System/Library/|/usr/lib/)' >/dev/null; then
    echo "Non-system library dependency in $resourcelens_tool" >&2; exit 1
  fi
done

npm ci --prefix frontend --ignore-scripts
npm run build --prefix frontend
if [ "${RESOURCELENS_SKIP_TESTS:-0}" != 1 ]; then
  "$resourcelens_go" test -tags desktop,production ./app ./frontend
  "$resourcelens_go" vet -tags desktop,production ./app ./frontend
fi

mkdir -p "$resourcelens_bundle/Contents/MacOS" "$resourcelens_bundle/Contents/Resources/video-engine"
cp installer/macos/Info.plist "$resourcelens_bundle/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $resourcelens_version" "$resourcelens_bundle/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $resourcelens_version" "$resourcelens_bundle/Contents/Info.plist"
resourcelens_cc="clang -arch $resourcelens_macho_arch"
MACOSX_DEPLOYMENT_TARGET=13.5 GOOS=darwin GOARCH="$resourcelens_arch" CGO_ENABLED=1 CC="$resourcelens_cc" "$resourcelens_go" build -trimpath -tags desktop,production -ldflags '-s -w' -o "$resourcelens_bundle/Contents/MacOS/ResourceLens" ./app
for resourcelens_tool in yt-dlp ffmpeg ffprobe node; do
  cp "$resourcelens_tools/$resourcelens_tool" "$resourcelens_bundle/Contents/Resources/video-engine/"
done
cp video-engine/yt-dlp-LICENSE.txt video-engine/yt-dlp-THIRD_PARTY_LICENSES.txt video-engine/ffmpeg-LICENSE video-engine/ORIGIN.md video-engine/MACOS.md "$resourcelens_bundle/Contents/Resources/video-engine/"
cp "$resourcelens_tools/node-LICENSE.txt" installer/macos/toolchain.json "$resourcelens_bundle/Contents/Resources/video-engine/"
cp LICENSE "$resourcelens_bundle/Contents/Resources/ResourceLens-LICENSE.txt"
"$resourcelens_tools/ffmpeg" -version > "$resourcelens_bundle/Contents/Resources/video-engine/ffmpeg-build.txt" 2>&1 || true

resourcelens_icons="$resourcelens_out/ResourceLens.iconset"
mkdir -p "$resourcelens_icons"
for resourcelens_size in 16 32 128 256 512; do
  sips -z "$resourcelens_size" "$resourcelens_size" winres/icon.png --out "$resourcelens_icons/icon_${resourcelens_size}x${resourcelens_size}.png" >/dev/null
  resourcelens_double=$((resourcelens_size * 2))
  sips -z "$resourcelens_double" "$resourcelens_double" winres/icon.png --out "$resourcelens_icons/icon_${resourcelens_size}x${resourcelens_size}@2x.png" >/dev/null
done
iconutil -c icns "$resourcelens_icons" -o "$resourcelens_bundle/Contents/Resources/ResourceLens.icns"

resourcelens_identity="${MACOS_SIGN_IDENTITY:--}"
for resourcelens_tool in yt-dlp ffmpeg ffprobe node; do
  if [ "$resourcelens_identity" = - ]; then
    codesign --force --sign - "$resourcelens_bundle/Contents/Resources/video-engine/$resourcelens_tool"
  else
    codesign --force --timestamp --options runtime --entitlements installer/macos/entitlements.plist --sign "$resourcelens_identity" "$resourcelens_bundle/Contents/Resources/video-engine/$resourcelens_tool"
  fi
done
if [ "$resourcelens_identity" = - ]; then
  codesign --force --sign - "$resourcelens_bundle"
else
  codesign --force --timestamp --options runtime --sign "$resourcelens_identity" "$resourcelens_bundle"
fi
codesign --verify --deep --strict "$resourcelens_bundle"
plutil -lint "$resourcelens_bundle/Contents/Info.plist"

resourcelens_stage="$resourcelens_out/dmg-content"
mkdir -p "$resourcelens_stage"
ditto "$resourcelens_bundle" "$resourcelens_stage/ResourceLens.app"
ln -sfn /Applications "$resourcelens_stage/Applications"
cp installer/macos/INSTALL.txt "$resourcelens_stage/安装说明.txt"
resourcelens_dmg="$resourcelens_out/ResourceLens-$resourcelens_version-macos-$resourcelens_arch.dmg"
hdiutil create -ov -volname "ResourceLens $resourcelens_version" -srcfolder "$resourcelens_stage" -format UDZO "$resourcelens_dmg"
if [ -n "${MACOS_NOTARY_PROFILE:-}" ]; then
  xcrun notarytool submit "$resourcelens_dmg" --keychain-profile "$MACOS_NOTARY_PROFILE" --wait
  xcrun stapler staple "$resourcelens_dmg"
fi
resourcelens_digest="$(shasum -a 256 "$resourcelens_dmg" | awk '{print $1}')"
printf '%s  %s\n' "$resourcelens_digest" "$(basename "$resourcelens_dmg")" > "$resourcelens_dmg.sha256"
echo "macOS app and DMG ready: $resourcelens_out"
