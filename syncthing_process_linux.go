//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

func configureSyncthingProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
func attachSyncthingProcess(cmd *exec.Cmd) (func(), error) { return func() {}, nil }
