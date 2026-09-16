#!/usr/bin/env bash
set -euo pipefail
version="${1:?Pass release version, for example v0.1.0}"
mkdir -p dist
for os in windows linux darwin; do
  for arch in amd64 arm64; do
    name="marketplace-sync_${version}_${os}_${arch}"
    directory="dist/$name"
    mkdir -p "$directory"
    binary=marketplace-sync
    if [ "$os" = windows ]; then binary=marketplace-sync.exe; fi
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="-s -w -X github.com/JuhunC/private-marketplace-manager/internal/buildinfo.Version=$version" -o "$directory/$binary" ./cmd/marketplace-sync
    cp README.md LICENSE "$directory/"
    cp examples/extensions.example.txt examples/sync-settings.example.json "$directory/"
    if [ "$os" = windows ]; then (cd "$directory" && zip -q -r "../$name.zip" .); else tar -czf "dist/$name.tar.gz" -C "$directory" .; fi
    rm -r "$directory"
  done
done
(cd dist && shasum -a 256 *.zip *.tar.gz > SHA256SUMS)
