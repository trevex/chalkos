package console

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/client"
)

// Client is what the console asks chalkd for. Tests pass a fake.
type Client interface {
	Info(ctx context.Context) (*nodev1.InfoResponse, error)
	Status(ctx context.Context) (*nodev1.StatusResponse, error)
	Disks(ctx context.Context) (*nodev1.DisksResponse, error)
	// Diagnostic runs one of the image's diagnostics and returns what it printed on standard
	// output. A run that does not exit 0 fails with its standard error.
	Diagnostic(ctx context.Context, name string, args map[string]string) ([]byte, error)
	// Logs streams the journal of the current boot, or of unit, from its last replayLines entries,
	// and calls line for each entry, until ctx ends or the stream fails.
	Logs(ctx context.Context, unit string, line func(string)) error
	Reboot(ctx context.Context) error
	Shutdown(ctx context.Context) error
	// RollbackImage rolls the node back to the image of rootHash and reboots it.
	RollbackImage(ctx context.Context, rootHash []byte) (*nodev1.RollbackImageResponse, error)
	RollbackConfig(ctx context.Context, version string) (*nodev1.RollbackConfigResponse, error)
}

// NewClient is the Client of the node conn reaches.
func NewClient(conn *client.Conn) Client { return nodeClient{node: conn.NodeServiceClient} }

type nodeClient struct {
	node nodev1connect.NodeServiceClient
}

func (c nodeClient) Info(ctx context.Context) (*nodev1.InfoResponse, error) {
	resp, err := c.node.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c nodeClient) Status(ctx context.Context) (*nodev1.StatusResponse, error) {
	resp, err := c.node.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c nodeClient) Disks(ctx context.Context) (*nodev1.DisksResponse, error) {
	resp, err := c.node.Disks(ctx, connect.NewRequest(&nodev1.DisksRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c nodeClient) Reboot(ctx context.Context) error {
	_, err := c.node.Reboot(ctx, connect.NewRequest(&nodev1.RebootRequest{}))
	return err
}

func (c nodeClient) Shutdown(ctx context.Context) error {
	_, err := c.node.Shutdown(ctx, connect.NewRequest(&nodev1.ShutdownRequest{}))
	return err
}

func (c nodeClient) RollbackImage(ctx context.Context, rootHash []byte) (*nodev1.RollbackImageResponse, error) {
	resp, err := c.node.RollbackImage(ctx, connect.NewRequest(&nodev1.RollbackImageRequest{RootHash: rootHash, Reboot: true}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c nodeClient) RollbackConfig(ctx context.Context, version string) (*nodev1.RollbackConfigResponse, error) {
	resp, err := c.node.RollbackConfig(ctx, connect.NewRequest(&nodev1.RollbackConfigRequest{Version: version}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c nodeClient) Diagnostic(ctx context.Context, name string, args map[string]string) ([]byte, error) {
	stream, err := c.node.RunDiagnostic(ctx, connect.NewRequest(&nodev1.RunDiagnosticRequest{Name: name, Arguments: args}))
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	var stdout, stderr bytes.Buffer
	var exit *nodev1.DiagnosticExit
	for stream.Receive() {
		m := stream.Msg()
		stdout.Write(m.GetStdout())
		stderr.Write(m.GetStderr())
		if e := m.GetExit(); e != nil {
			exit = e
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	switch {
	case exit == nil:
		return nil, fmt.Errorf("diagnostic %s ended without an exit", name)
	case exit.Stopped != "":
		return nil, fmt.Errorf("diagnostic %s stopped (%s): %s", name, exit.Stopped, strings.TrimSpace(stderr.String()))
	case exit.Code != 0:
		return nil, fmt.Errorf("diagnostic %s: exit %d: %s", name, exit.Code, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (c nodeClient) Logs(ctx context.Context, unit string, line func(string)) error {
	stream, err := c.node.Logs(ctx, connect.NewRequest(&nodev1.LogsRequest{Unit: unit, Follow: true, Lines: replayLines}))
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		line(stream.Msg().Line)
	}
	return stream.Err()
}
