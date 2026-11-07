package tool

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestExecRunnerPassesStdin(t *testing.T) {
	const input = "not-on-the-command-line"
	out, err := ExecRunner{}.Exec(context.Background(), Cmd{Name: "cat", Stdin: []byte(input)})
	if err != nil || string(out) != input {
		t.Fatalf("out = %q, %v, want the input back", out, err)
	}
	_, err = ExecRunner{}.Exec(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "exit 2"}, Stdin: []byte(input)})
	if te, ok := errors.AsType[*Error](err); !ok || te.Code != 2 || strings.Contains(err.Error(), input) {
		t.Errorf("err = %v, want an Error with status 2 that does not repeat the input", err)
	}
}

func TestExecRunnerSetsEnv(t *testing.T) {
	out, err := ExecRunner{}.Exec(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "printf %s \"$SECRET\""}, Env: []string{"SECRET=s3cret"}})
	if err != nil || string(out) != "s3cret" {
		t.Errorf("out = %q, %v, want the variable's value", out, err)
	}
}

// A quiet tool's standard error stays off the console; its error carries it.
func TestExecRunnerQuiet(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	_, runErr := ExecRunner{}.Exec(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo cannot talk to daemon >&2; exit 1"}, Quiet: true})
	os.Stderr = stderr
	w.Close()
	logged, _ := io.ReadAll(r)
	if len(logged) != 0 {
		t.Errorf("wrote %q to the console", logged)
	}
	if te, ok := errors.AsType[*Error](runErr); !ok || !strings.Contains(te.Stderr, "cannot talk to daemon") {
		t.Errorf("err = %v, want an Error with the standard error", runErr)
	}
}
