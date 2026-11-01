//go:build windows

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
)

// detach keeps the engine alive after the command that started it exits, without a console window.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | createNoWindow}
}

// terminate has no gentler option on Windows without a shared console.
func terminate(p *os.Process) error { return p.Kill() }
