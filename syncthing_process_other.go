//go:build !windows && !linux

package main

import "os/exec"

func configureSyncthingProcess(cmd *exec.Cmd)              {}
func attachSyncthingProcess(cmd *exec.Cmd) (func(), error) { return func() {}, nil }
