package chalkd

import (
	"bufio"
	"context"
	"io"
	"os/exec"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

func (s *Server) Logs(ctx context.Context, req *connect.Request[nodev1.LogsRequest], stream *connect.ServerStream[nodev1.LogsResponse]) error {
	if s.logStreams.Add(1) > maxLogStreams {
		s.logStreams.Add(-1)
		return failed(connect.CodeResourceExhausted, "%d log streams are open already; close one first", maxLogStreams)
	}
	defer s.logStreams.Add(-1)
	r, err := s.Journal(ctx, req.Msg.Unit, req.Msg.Follow)
	if err != nil {
		return failed(connect.CodeInternal, "read the journal: %v", err)
	}
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if err := stream.Send(&nodev1.LogsResponse{Line: sc.Text()}); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return failed(connect.CodeInternal, "read the journal: %v", err)
	}
	return nil
}

// Journal runs journalctl for the current boot; following ends when ctx does.
func Journal(ctx context.Context, unit string, follow bool) (io.ReadCloser, error) {
	args := []string{"--boot", "--no-pager", "--output=short-iso"}
	if unit != "" {
		args = append(args, "--unit="+unit)
	}
	if follow {
		args = append(args, "--follow")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &process{ReadCloser: out, cmd: cmd}, nil
}

type process struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (p *process) Close() error {
	p.ReadCloser.Close()
	p.cmd.Process.Kill()
	p.cmd.Wait()
	return nil
}
