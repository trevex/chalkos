package main

import (
	"context"
	"errors"
	"io"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/hostops"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/tool"
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
	run := tool.ExecRunner{}
	s := &node.Storage{
		Run:            run,
		Host:           storage.DefaultHost(),
		Ops:            hostops.System(run),
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
