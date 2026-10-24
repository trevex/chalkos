// Package tooltest holds a fake tool.Runner for tests.
package tooltest

import (
	"context"
	"strings"
	"sync"
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
	// Envs holds the environment variables commands received, by command line.
	Envs map[string][]string
	// Inputs holds what commands received on standard input, by command line.
	Inputs map[string]string
	// Quiet holds the command lines run without their standard error reaching the console.
	Quiet map[string]bool
	// Before, when set, sees each command line before the command is answered.
	Before func(line string)
}

func (f *Runner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.RunWithEnv(ctx, nil, name, args...)
}

func (f *Runner) RunQuiet(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	if f.Quiet == nil {
		f.Quiet = map[string]bool{}
	}
	f.Quiet[line(name, args)] = true
	f.mu.Unlock()
	return f.RunWithEnv(ctx, nil, name, args...)
}

func (f *Runner) RunWithInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	if f.Inputs == nil {
		f.Inputs = map[string]string{}
	}
	f.Inputs[line(name, args)] = string(input)
	f.mu.Unlock()
	return f.RunWithEnv(ctx, nil, name, args...)
}

func (f *Runner) RunWithEnv(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := line(name, args)
	f.Calls = append(f.Calls, l)
	if f.Before != nil {
		f.Before(l)
	}
	if env != nil {
		if f.Envs == nil {
			f.Envs = map[string][]string{}
		}
		f.Envs[l] = env
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

func line(name string, args []string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

// ResetCalls forgets the commands run so far, while code under test may still run some.
func (f *Runner) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = nil
}
