package chalkd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
)

func (s *Server) Logs(ctx context.Context, req *nodev1.LogsRequest, stream nodev1connect.NodeServiceLogsServerStream) error {
	release, err := s.logStreams.take(ctx, logLimits)
	if err != nil {
		return err
	}
	defer release()
	// chalkd does not wait for a stream that follows the journal when it stops.
	if s.Stopping != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(s.Stopping, cancel)()
	}
	r, err := s.Journal(ctx, req.Unit, req.Follow, req.Lines)
	if err != nil {
		return fmt.Errorf("read the journal: %w", err)
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
		return fmt.Errorf("read the journal: %w", err)
	}
	return nil
}

// Journal runs journalctl for the current boot, from its last lines entries when lines is not zero;
// following ends when ctx does.
func Journal(ctx context.Context, unit string, follow bool, lines uint32) (io.ReadCloser, error) {
	args := []string{"--boot", "--no-pager", "--output=short-iso"}
	if lines > 0 {
		args = append(args, "--lines="+strconv.FormatUint(uint64(lines), 10))
	}
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
