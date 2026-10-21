//go:build !unix

package process

import "os/exec"

// setProcessGroup keeps exec's default: cancelling kills the tool only.
func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(*exec.Cmd) {}
