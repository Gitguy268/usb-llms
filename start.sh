#!/bin/sh
# Linux. Run with: sh start.sh
cd "$(dirname "$0")" || exit 1
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "USB LLMS has no build for $(uname -m)."; exit 1 ;;
esac
bin="bin/usb-llms-linux-$arch"
if [ ! -f "$bin" ]; then
  echo "This folder has no $bin. Download a full USB LLMS release."
  exit 1
fi
chmod +x "$bin" 2>/dev/null
if ! "./$bin" version >/dev/null 2>&1; then
  mount_point=$(df -P . | awk 'NR==2 {print $6}')
  echo "This drive can't run programs. It is probably mounted with noexec."
  echo "Fix it with: sudo mount -o remount,exec \"$mount_point\""
  exit 1
fi
exec "./$bin" "$@"
