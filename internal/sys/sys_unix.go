//go:build !windows

package sys

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// FreeBytes reports the space available to this user on the volume holding path.
func FreeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// RAMBytes is the machine's total memory, or 0 when it can't be read.
func RAMBytes() uint64 {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0
		}
		n, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
		return n
	}
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
			n, _ := strconv.ParseUint(f[1], 10, 64)
			return n * 1024
		}
	}
	return 0
}

// HasDedicatedGPU reports an NVIDIA or AMD compute driver. Macs always use Metal from the one engine build.
func HasDedicatedGPU() (nvidia, amd bool) {
	if runtime.GOOS != "linux" {
		return false, false
	}
	_, err := os.Stat("/proc/driver/nvidia/version")
	nvidia = err == nil
	_, err = os.Stat("/dev/kfd")
	amd = err == nil
	return nvidia, amd
}
