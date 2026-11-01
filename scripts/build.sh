#!/bin/sh
# Cross-compiles every release binary into bin/. Pass one platform, e.g. darwin-arm64, to build only that.
set -e
cd "$(dirname "$0")/.."
targets="${1:-darwin-arm64 darwin-amd64 linux-amd64 linux-arm64 windows-amd64 windows-arm64}"
for t in $targets; do
  os=${t%-*}; arch=${t#*-}; ext=""; [ "$os" = windows ] && ext=.exe
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags="-s -w" -o "bin/usb-llms-$t$ext" .
  echo "built bin/usb-llms-$t$ext"
done
