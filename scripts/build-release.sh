#!/bin/sh
# Builds release archives for every supported platform into dist/.
# Usage: scripts/build-release.sh v0.1.0
set -eu

version=${1:?usage: $0 VERSION}
targets="darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64"

rm -rf dist
mkdir -p dist
for t in $targets; do
	os=${t%/*}
	arch=${t#*/}
	name="sheaf_${version#v}_${os}_${arch}"
	dir="dist/$name"
	mkdir -p "$dir"
	bin=sheaf
	[ "$os" = windows ] && bin=sheaf.exe
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X main.version=$version" -o "$dir/$bin" ./cmd/sheaf
	cp LICENSE README.md "$dir/"
	if [ "$os" = windows ]; then
		(cd dist && zip -qr "$name.zip" "$name")
	else
		tar -C dist -czf "dist/$name.tar.gz" "$name"
	fi
	rm -rf "$dir"
done
(cd dist && sha256sum ./*.tar.gz ./*.zip | sed 's| \./| |' > checksums.txt)
ls -l dist
