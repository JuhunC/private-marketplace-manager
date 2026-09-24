#!/usr/bin/env bash
set -euo pipefail
version="${1:?Pass release version, for example v0.2.0}"
rm -rf dist
mkdir -p dist
for tool in marketplace-sync marketplace-mcp; do
  for os in windows linux darwin; do
    for arch in amd64 arm64; do
      name="${tool}_${version}_${os}_${arch}"
      directory="dist/$name"
      mkdir -p "$directory"
      binary="$tool"
      if [ "$os" = windows ]; then binary="$tool.exe"; fi
      CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="-s -w -X github.com/JuhunC/private-marketplace-manager/internal/buildinfo.Version=$version" -o "$directory/$binary" "./cmd/$tool"
      cp README.md LICENSE "$directory/"
      if [ "$tool" = marketplace-sync ]; then
        cp examples/extensions.example.txt examples/sync-settings.example.json docs/sync.md "$directory/"
      else
        cp examples/mcp-settings.example.json docs/mcp.md "$directory/"
      fi
      if [ "$os" = windows ]; then (cd "$directory" && zip -q -r "../$name.zip" .); else tar -czf "dist/$name.tar.gz" -C "$directory" .; fi
      rm -r "$directory"
    done
  done
done
(cd dist && shasum -a 256 *.zip *.tar.gz > SHA256SUMS)
