package main

import (
	"strings"
	"testing"
)

func TestRunSignRequiresAllFlags(t *testing.T) {
	err := runSign([]string{"--image", "disk.raw"})
	if err == nil || !strings.Contains(err.Error(), "--repart-json") {
		t.Fatalf("err = %v, want a missing --repart-json error", err)
	}
}
