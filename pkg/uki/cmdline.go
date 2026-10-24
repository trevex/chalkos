package uki

import (
	"fmt"
	"os"
)

// RunningHash is the verity root hash of the store the running kernel boots: usrhash= on the
// kernel command line at path, /proc/cmdline on a node.
func RunningHash(path string) ([]byte, error) {
	cmdline, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the kernel command line: %w", err)
	}
	hash, err := UsrHash(string(cmdline))
	if err != nil {
		return nil, fmt.Errorf("the running store: %w", err)
	}
	return hash, nil
}
