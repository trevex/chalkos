package lab

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// run executes a helper tool and includes its output in the error when it fails.
func run(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}
