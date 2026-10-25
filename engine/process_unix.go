//go:build !windows

package engine

import (
	"errors"
	"os/exec"
	"syscall"
)

// ownGroup puts the process in a process group of its own, so killGroup
// reaches every process it starts (a wrapper's docker client, for example).
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the process and its process group.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		// Already gone.
		return nil
	}
	return err
}
