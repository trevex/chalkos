// Package tooltest holds a fake tool.Runner for tests.
package tooltest

import (
	"context"
	"strings"
	"sync"

	"github.com/trevex/chalkos/pkg/tool"
)

// Rule answers the commands whose line, the name and arguments joined by spaces, starts with
// Prefix. A rule with Times > 0 answers that many commands and then no more.
type Rule struct {
	Prefix string
	Out    string
	Err    error
	Times  int
}

// Runner records the commands it runs and answers each from the first rule that matches. A
// command no rule matches succeeds without output. It is safe for concurrent use; tests read its
// fields once the code under test has returned.
type Runner struct {
	mu    sync.Mutex
	Rules []Rule
	// Calls holds the command lines run, in order.
	Calls []string
	// Cmds holds the commands run, in order, with their environment, input and quietness.
	Cmds []tool.Cmd
	// Before, when set, sees each command line before the command is answered.
	Before func(line string)
}

func (f *Runner) Exec(_ context.Context, c tool.Cmd) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := c.String()
	f.Calls = append(f.Calls, l)
	f.Cmds = append(f.Cmds, c)
	if f.Before != nil {
		f.Before(l)
	}
	for i := range f.Rules {
		r := &f.Rules[i]
		if r.Times < 0 || !strings.HasPrefix(l, r.Prefix) {
			continue
		}
		if r.Times > 0 {
			r.Times--
			if r.Times == 0 {
				r.Times = -1
			}
		}
		return []byte(r.Out), r.Err
	}
	return nil, nil
}

// Cmd is the last command run whose line is line, and whether one was.
func (f *Runner) Cmd(line string) (tool.Cmd, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.Cmds) - 1; i >= 0; i-- {
		if f.Cmds[i].String() == line {
			return f.Cmds[i], true
		}
	}
	return tool.Cmd{}, false
}

// ResetCalls forgets the commands run so far, while code under test may still run some.
func (f *Runner) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = nil
	f.Cmds = nil
}
