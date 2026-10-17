package main

import (
	"context"
	"errors"
	"io"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

// runFacts prints a fact of the storage kinds' rules for the input chalkd writes to a command
// fact. It runs in chalkd's sandbox on the booted node, where STATE is at /state, read-only.
func runFacts(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("facts takes the fact to print: " + node.FactDisks + " or " + node.FactFallback)
	}
	input, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	s := &node.Storage{
		Run:            node.ExecRunner{},
		Host:           storage.DefaultHost(),
		StateDir:       "/state",
		BootPartitions: "/dev/disk/chalk-boot",
	}
	out, err := s.Facts(context.Background(), args[0], input)
	if err != nil {
		return err
	}
	_, err = stdout.Write(out)
	return err
}
