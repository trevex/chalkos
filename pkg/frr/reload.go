package frr

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/trevex/chalkos/pkg/tool"
)

// restart is the way out of a config the reload cannot apply.
const restart = "restart frr.service to load frr.conf whole"

// Running reads FRR's running config with vtysh.
func Running(ctx context.Context, r tool.Runner, vtysh string) (*Config, error) {
	out, err := tool.Run(ctx, r, vtysh, "-c", "show running-config")
	if err != nil {
		return nil, err
	}
	c, err := Parse(string(out))
	if err != nil {
		return nil, fmt.Errorf("FRR's running config: %w; %s", err, restart)
	}
	return c, nil
}

// Load parses frr.conf.
func Load(text string) (*Config, error) {
	c, err := Parse(text)
	if err != nil {
		return nil, fmt.Errorf("frr.conf: %w; %s", err, restart)
	}
	return c, nil
}

// Reload makes FRR run the target: it applies the plan from the running config with vtysh -f, from
// a file in dir, and reads the running config again. A deleted neighbour can take commands FRR
// then lacks, so a second plan may follow; a config that still differs after it is an error that
// names the commands left.
func Reload(ctx context.Context, r tool.Runner, vtysh, dir string, target *Config, logger *log.Logger) error {
	for pass := 1; ; pass++ {
		running, err := Running(ctx, r, vtysh)
		if err != nil {
			return err
		}
		plan := Diff(running, target)
		switch {
		case plan.Empty() && pass == 1:
			logger.Print("FRR runs frr.conf already")
			return nil
		case plan.Empty():
			logger.Print("FRR runs frr.conf")
			return nil
		case pass > 2:
			return fmt.Errorf("FRR's running config still differs from frr.conf after two passes, by:\n%s%s", plan, restart)
		}
		logger.Printf("pass %d applies:\n%s", pass, plan)
		if err := apply(ctx, r, vtysh, dir, plan); err != nil {
			// The next pass reads what FRR took, and fails on what it did not.
			logger.Printf("vtysh refused commands of pass %d: %v", pass, err)
		}
	}
}

// apply runs the plan's script with vtysh -f, which reads the file once per daemon, so it is a
// file in dir, readable by its owner alone as it holds the passwords, rather than a pipe.
func apply(ctx context.Context, r tool.Runner, vtysh, dir string, plan Plan) error {
	f, err := os.CreateTemp(dir, "chalkos-frr-reload-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(plan.Script()); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// vtysh repeats a line it fails on, a password among them, on standard error, which reaches
	// the journal only through hide.
	_, err = r.Exec(ctx, tool.Cmd{Name: vtysh, Args: []string{"-f", f.Name()}, Quiet: true})
	if err != nil {
		return errors.New(hide(err.Error()))
	}
	return nil
}
