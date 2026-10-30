//go:build !windows

package main

import (
	"context"
	"log"
)

func (l *Library) watchChanges(ctx context.Context) {
	log.Print("Filesystem notifications unavailable on this platform; periodic reconciliation remains enabled")
	<-ctx.Done()
}
