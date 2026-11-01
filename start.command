#!/bin/bash
# Double-click on macOS. Clears the download quarantine that would otherwise block the binaries.
cd "$(dirname "$0")" || exit 1
case "$(uname -m)" in arm64) arch=arm64 ;; *) arch=amd64 ;; esac
bin="bin/usb-llms-darwin-$arch"
if [ ! -f "$bin" ]; then
  echo "This folder has no $bin. Download a full USB LLMS release."
  read -r -p "Press Enter to close." _
  exit 1
fi
xattr -dr com.apple.quarantine . 2>/dev/null
find . -name '._*' -type f -not -path './models/blobs/*' -delete 2>/dev/null
"./$bin" "$@"
status=$?
if [ $status -ne 0 ]; then read -r -p "Press Enter to close." _; fi
exit $status
