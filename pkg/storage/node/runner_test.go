package node

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestExecRunnerPassesInputOnStdin(t *testing.T) {
	const input = "not-on-the-command-line"
	out, err := ExecRunner{}.RunWithInput(context.Background(), []byte(input), "cat")
	if err != nil || string(out) != input {
		t.Fatalf("out = %q, %v, want the input back", out, err)
	}
	_, err = ExecRunner{}.RunWithInput(context.Background(), []byte(input), "sh", "-c", "exit 2")
	var te *ToolError
	if !errors.As(err, &te) || te.Code != 2 || strings.Contains(err.Error(), input) {
		t.Errorf("err = %v, want a ToolError with status 2 that does not repeat the input", err)
	}
}

// RunQuiet keeps a tool's standard error out of the console; its error carries it.
func TestExecRunnerRunQuiet(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	_, runErr := ExecRunner{}.RunQuiet(context.Background(), "sh", "-c", "echo cannot talk to daemon >&2; exit 1")
	os.Stderr = stderr
	w.Close()
	logged, _ := io.ReadAll(r)
	if len(logged) != 0 {
		t.Errorf("wrote %q to the console", logged)
	}
	var te *ToolError
	if !errors.As(runErr, &te) || !strings.Contains(te.Stderr, "cannot talk to daemon") {
		t.Errorf("err = %v, want a ToolError with the standard error", runErr)
	}
}
