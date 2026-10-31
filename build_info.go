package main

import (
	"os"
	"time"
)

// binaryCreatedAt identifies the executable currently serving requests. The
// filesystem timestamp is available on every supported platform (unlike the
// Windows-only creation timestamp) and survives the Windows installer copy.
func binaryCreatedAt() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339Nano)
}
