#!/bin/sh
# Builds the desktop app for the platform this runs on, with the sheaf CLI
# bundled next to it, into dist/. The webview needs native code, so each
# platform builds on its own runner.
#
# Usage: scripts/build-desktop.sh v0.1.0
#
# macOS:   dist/Sheaf_VERSION_macos_universal.zip (Sheaf.app, ad-hoc signed)
# Linux:   dist/sheaf-desktop_VERSION_linux_ARCH.tar.gz (needs WebKitGTK 4.1)
# Windows: dist/sheaf-desktop_VERSION_windows_ARCH.zip (needs WebView2)
set -eu

version=${1:?usage: $0 VERSION}
v=${version#v}
root=$(pwd)
work="$root/build/desktop"
rm -rf "$work"
mkdir -p "$work" dist
cliflags="-s -w -X main.version=$version"

case "$(uname -s)" in
Darwin)
	export MACOSX_DEPLOYMENT_TARGET=11.0
	for arch in arm64 amd64; do
		(cd desktop && CGO_ENABLED=1 GOARCH=$arch CGO_LDFLAGS="-framework UniformTypeIdentifiers" \
			go build -trimpath -tags desktop,production -ldflags "-s -w" -o "$work/sheaf-desktop-$arch" .)
		CGO_ENABLED=0 GOARCH=$arch go build -trimpath -ldflags "$cliflags" -o "$work/sheaf-$arch" ./cmd/sheaf
	done
	app="$work/Sheaf.app"
	mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
	lipo -create -output "$app/Contents/MacOS/sheaf-desktop" "$work/sheaf-desktop-arm64" "$work/sheaf-desktop-amd64"
	lipo -create -output "$app/Contents/MacOS/sheaf" "$work/sheaf-arm64" "$work/sheaf-amd64"

	set=$work/icon.iconset
	mkdir -p "$set"
	for s in 16 32 128 256 512; do
		sips -z $s $s desktop/build/appicon.png --out "$set/icon_${s}x${s}.png" >/dev/null
		sips -z $((s * 2)) $((s * 2)) desktop/build/appicon.png --out "$set/icon_${s}x${s}@2x.png" >/dev/null
	done
	iconutil -c icns "$set" -o "$app/Contents/Resources/icon.icns"

	sed "s/@VERSION@/$v/g" desktop/build/Info.plist > "$app/Contents/Info.plist"
	# Ad-hoc signature: Apple Silicon runs only signed code. A Developer ID
	# signature and notarization come with the signed macOS identity.
	codesign --force --deep --sign - "$app"
	ditto -c -k --keepParent "$app" "dist/Sheaf_${v}_macos_universal.zip"
	;;
Linux)
	arch=$(go env GOARCH)
	name="sheaf-desktop_${v}_linux_${arch}"
	dir="$work/$name"
	mkdir -p "$dir"
	(cd desktop && CGO_ENABLED=1 go build -trimpath -tags desktop,production,webkit2_41 -ldflags "-s -w" -o "$dir/sheaf-desktop" .)
	CGO_ENABLED=0 go build -trimpath -ldflags "$cliflags" -o "$dir/sheaf" ./cmd/sheaf
	cp desktop/build/appicon.png "$dir/sheaf.png"
	cp desktop/build/sheaf.desktop LICENSE "$dir/"
	tar -C "$work" -czf "dist/$name.tar.gz" "$name"
	;;
MINGW* | MSYS* | CYGWIN* | Windows_NT)
	arch=$(go env GOARCH)
	name="sheaf-desktop_${v}_windows_${arch}"
	dir="$work/$name"
	mkdir -p "$dir"
	(cd desktop && go build -trimpath -tags desktop,production -ldflags "-s -w -H windowsgui" -o "$dir/sheaf-desktop.exe" .)
	CGO_ENABLED=0 go build -trimpath -ldflags "$cliflags" -o "$dir/sheaf.exe" ./cmd/sheaf
	cp LICENSE "$dir/"
	(cd "$work" && 7z a -tzip -bd "$root/dist/$name.zip" "$name" >/dev/null)
	;;
*)
	echo "unsupported platform: $(uname -s)" >&2
	exit 1
	;;
esac
ls -l dist
