//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

func configurePreview(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 | 0x00004000}
} // CREATE_NO_WINDOW | BELOW_NORMAL_PRIORITY_CLASS
