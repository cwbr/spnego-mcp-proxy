#!/bin/sh
# Builds spnego-mcp-proxy for Windows and macOS (amd64 and arm64 each).
# Windows binaries are cross-compiled without cgo. The macOS binaries link
# the GSS framework through cgo and therefore must be built on a Mac.
set -eu

cd "$(dirname "$0")"
name=spnego-mcp-proxy
out=dist
ldflags="-s -w -X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)"

rm -rf "$out"

go vet ./...
go test ./...

for arch in amd64 arm64; do
	GOOS=windows GOARCH=$arch CGO_ENABLED=0 \
		go build -trimpath -ldflags "$ldflags" -o "$out/windows-$arch/$name.exe" .
done

if [ "$(uname -s)" = Darwin ]; then
	for arch in amd64 arm64; do
		GOOS=darwin GOARCH=$arch CGO_ENABLED=1 \
			go build -trimpath -ldflags "$ldflags" -o "$out/macos-$arch/$name" .
	done
else
	echo "Skipping macOS build: it requires a Mac (cgo with GSS.framework)." >&2
fi

ls -l "$out"/*/*
