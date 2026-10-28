//go:build !windows

package dlsvc

import (
	"os/exec"
	"syscall"
)

// detach starts the service in its own session, so closing the terminal or
// quitting the TUI that started it does not take it down.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
