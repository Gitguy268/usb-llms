//go:build windows

package sys

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
	globalMemoryStatus  = kernel32.NewProc("GlobalMemoryStatusEx")
)

// FreeBytes reports the space available to this user on the volume holding path.
func FreeBytes(path string) (uint64, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	if r, _, e := getDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), 0, 0); r == 0 {
		return 0, e
	}
	return free, nil
}

type memoryStatusEx struct {
	length, memoryLoad                   uint32
	totalPhys, availPhys                 uint64
	totalPageFile, availPageFile         uint64
	totalVirtual, availVirtual, extended uint64
}

// RAMBytes is the machine's total memory, or 0 when it can't be read.
func RAMBytes() uint64 {
	m := memoryStatusEx{}
	m.length = uint32(unsafe.Sizeof(m))
	if r, _, _ := globalMemoryStatus.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0
	}
	return m.totalPhys
}

// HasDedicatedGPU looks for the driver DLLs NVIDIA and AMD install into System32.
func HasDedicatedGPU() (nvidia, amd bool) {
	sys32 := filepath.Join(os.Getenv("SystemRoot"), "System32")
	_, err := os.Stat(filepath.Join(sys32, "nvcuda.dll"))
	nvidia = err == nil
	_, err = os.Stat(filepath.Join(sys32, "amdhip64.dll"))
	amd = err == nil
	return nvidia, amd
}
