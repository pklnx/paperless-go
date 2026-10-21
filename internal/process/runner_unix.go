//go:build unix

package process

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts cmd in a process group of its own and makes
// cancelling send SIGTERM to the whole group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

// killProcessGroup kills what is left of a cancelled tool's group, such as
// children that ignored SIGTERM. The group's ID is the tool's PID.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
