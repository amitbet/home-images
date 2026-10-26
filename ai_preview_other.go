//go:build !windows

package main

import "os/exec"

func configurePreview(cmd *exec.Cmd) {}
