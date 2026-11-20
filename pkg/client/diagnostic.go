package client

import (
	"context"
	"errors"
	"fmt"
	"io"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
)

// RunDiagnostic runs one of the node's diagnostics, writes what it prints to stdout and stderr,
// and returns its exit, nil when the node ended the stream without one. Returning early closes the
// stream, and the node stops the run.
func RunDiagnostic(ctx context.Context, node nodev1connect.NodeServiceClient, name string, args map[string]string, stdout, stderr io.Writer) (*nodev1.DiagnosticExit, error) {
	stream, err := node.RunDiagnostic(ctx, &nodev1.RunDiagnosticRequest{Name: name, Arguments: args})
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	var exit *nodev1.DiagnosticExit
	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return exit, nil
		}
		if err != nil {
			return exit, err
		}
		switch o := msg.Output.(type) {
		case *nodev1.RunDiagnosticResponse_Stdout:
			_, err = stdout.Write(o.Stdout)
		case *nodev1.RunDiagnosticResponse_Stderr:
			_, err = stderr.Write(o.Stderr)
		case *nodev1.RunDiagnosticResponse_Exit:
			exit = o.Exit
		}
		if err != nil {
			return nil, fmt.Errorf("print what %s prints: %w", name, err)
		}
	}
}
