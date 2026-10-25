//go:build windows

package engine

import (
	"errors"
	"os"
	"os/exec"
)

// ownGroup does nothing on Windows: there killGroup kills only the process
// itself.
func ownGroup(cmd *exec.Cmd) {}

// killGroup kills the process.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
