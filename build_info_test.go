package main

import (
	"testing"
	"time"
)

func TestBinaryCreatedAtIsRFC3339(t *testing.T) {
	value := binaryCreatedAt()
	if value == "" {
		t.Fatal("binary creation time is empty")
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		t.Fatalf("binary creation time %q: %v", value, err)
	}
}
