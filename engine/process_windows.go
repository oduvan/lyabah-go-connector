//go:build windows

package engine

import (
	"errors"
	"fmt"
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

// killedError is the error of Wait after Kill. Windows ends a killed
// process with exit status 1, so the error says it was killed.
func killedError(err error) error { return fmt.Errorf("killed (%w)", err) }
