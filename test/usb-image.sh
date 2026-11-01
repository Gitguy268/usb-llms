#!/bin/sh
# Real test on a simulated exFAT drive, macOS only: engine, model, offline mode and no traces on the host.
# Usage: test/usb-image.sh /path/to/model.gguf
set -eu
gguf=$1
repo=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/usb-llms-test.XXXXXX")
free_gb=$(df -g /System/Volumes/Data | awk 'NR==2 {print $4}')
if [ "$free_gb" -lt 5 ]; then echo "Only $free_gb GB free; stopping."; exit 1; fi

mnt="$work/mnt"; home="$work/home"; hosttmp="$work/tmp"
mkdir -p "$mnt" "$home" "$hosttmp"
cleanup() {
  HOME="$home" "$mnt/bin/usb-llms" stop 2>/dev/null || true
  hdiutil detach "$mnt" -quiet -force 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT
hdiutil create -size 2g -fs ExFAT -type SPARSE -volname USBTEST "$work/usb" -quiet
hdiutil attach "$work/usb.sparseimage" -mountpoint "$mnt" -nobrowse -quiet
mkdir -p "$mnt/bin"
(cd "$repo" && CGO_ENABLED=0 go build -o "$mnt/bin/usb-llms" .)
cp "$repo/start.command" "$mnt/"

export HOME="$home" TMPDIR="$hosttmp"
u="$mnt/bin/usb-llms"
"$u" setup --lean
"$u" pull "$gguf" --name smollm2
"$u" mode offline

if "$u" pull smollm2-135m 2>/dev/null; then echo "FAIL: pull worked while offline"; exit 1; fi
"$u" start
if curl -s -m 30 127.0.0.1:11435/api/pull -d '{"model":"tinyllama","stream":false}' | grep -q '"status":"success"'; then
  echo "FAIL: the engine reached the network while offline"; exit 1
fi
"$u" stop

cat > "$work/nonet.sb" <<'SB'
(version 1)
(allow default)
(deny network-outbound)
(allow network-outbound (remote ip "localhost:*"))
SB
sandbox-exec -f "$work/nonet.sb" "$u" start
reply=$(sandbox-exec -f "$work/nonet.sb" "$u" run smollm2 "Say hi." </dev/null 2>/dev/null)
[ -n "$reply" ] || { echo "FAIL: no reply with the network blocked"; exit 1; }
"$u" stop

left=$(find "$home" "$hosttmp" -mindepth 1 | wc -l | tr -d ' ')
[ "$left" = 0 ] || { echo "FAIL: left $left files on the host"; find "$home" "$hosttmp" -mindepth 1; exit 1; }
echo "PASS"
