package node

import (
	"context"
	"errors"
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
