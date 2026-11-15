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

// unary calls a unary method of the node with req and returns its response's message.
func unary[Req, Resp any](ctx context.Context, call func(context.Context, *connect.Request[Req]) (*connect.Response[Resp], error), req *Req) (*Resp, error) {
	resp, err := call(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c nodeClient) Info(ctx context.Context) (*nodev1.InfoResponse, error) {
	return unary(ctx, c.node.Info, &nodev1.InfoRequest{})
}

func (c nodeClient) Status(ctx context.Context) (*nodev1.StatusResponse, error) {
	return unary(ctx, c.node.Status, &nodev1.StatusRequest{})
}

func (c nodeClient) Disks(ctx context.Context) (*nodev1.DisksResponse, error) {
	return unary(ctx, c.node.Disks, &nodev1.DisksRequest{})
}

func (c nodeClient) Reboot(ctx context.Context) error {
	_, err := unary(ctx, c.node.Reboot, &nodev1.RebootRequest{})
	return err
}

func (c nodeClient) Shutdown(ctx context.Context) error {
	_, err := unary(ctx, c.node.Shutdown, &nodev1.ShutdownRequest{})
	return err
}

func (c nodeClient) RollbackImage(ctx context.Context, rootHash []byte) (*nodev1.RollbackImageResponse, error) {
	return unary(ctx, c.node.RollbackImage, &nodev1.RollbackImageRequest{RootHash: rootHash, Reboot: true})
}

func (c nodeClient) RollbackConfig(ctx context.Context, version string) (*nodev1.RollbackConfigResponse, error) {
	return unary(ctx, c.node.RollbackConfig, &nodev1.RollbackConfigRequest{Version: version})
}

func (c nodeClient) Diagnostic(ctx context.Context, name string, args map[string]string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	exit, err := client.RunDiagnostic(ctx, c.node, name, args, &stdout, &stderr)
	if err != nil {
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
