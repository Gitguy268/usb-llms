//go:build !windows

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

// detach keeps the engine alive after the command that started it exits.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
