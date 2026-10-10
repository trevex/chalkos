package chalkd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/storage"
)

func (s *Server) Status(ctx context.Context, _ *connect.Request[nodev1.StatusRequest]) (*connect.Response[nodev1.StatusResponse], error) {
	resp := &nodev1.StatusResponse{Platform: readOSRelease(s.Paths.OSRelease)["CHALKOS_PLATFORM"]}
	data, err := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json"))
	if err != nil {
		return nil, failed(connect.CodeInternal, "read the identity: %v", err)
	}
	resp.IdentityVersion = identity.IdentityVersion(data)

	var status storage.Status
	if raw, err := os.ReadFile(s.Paths.StorageStatus); err == nil {
		if err := json.Unmarshal(raw, &status); err != nil {
			return nil, failed(connect.CodeInternal, "parse %s: %v", s.Paths.StorageStatus, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	// The initrd writes the disks' errors before it can tell the node's name.
	for _, name := range sortedNames(status.Disks) {
		d := status.Disks[name]
		resp.Disks = append(resp.Disks, &nodev1.DiskStatus{Name: name, Device: d.Device, Error: strings.ReplaceAll(d.Error, "<node>", s.nodeName())})
	}

	section, pins, err := s.recorded()
	if err != nil {
		return nil, err
	}
	mounts, err := mountPoints(s.Paths.MountInfo)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	for _, name := range sortedNames(section.Volumes) {
		v := section.Volumes[name]
		resp.Volumes = append(resp.Volumes, &nodev1.VolumeStatus{
			Name:       name,
			Disk:       v.Disk,
			MountPoint: v.MountPoint,
			Present:    pins.Disks[v.Disk].Partitions[name] != "",
			Mounted:    v.MountPoint != "" && mounts[v.MountPoint],
		})
	}

	out, err := s.Run.Run(ctx, "systemctl", "list-units", "--state=failed", "--plain", "--no-legend", "--no-pager")
	if err != nil {
		return nil, failed(connect.CodeInternal, "list failed units: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			resp.FailedUnits = append(resp.FailedUnits, fields[0])
		}
	}
	if s.Kubernetes != nil {
		if resp.Kubernetes, err = s.Kubernetes.status(ctx); err != nil {
			return nil, failed(connect.CodeInternal, "kubernetes: %v", err)
		}
	}
	resp.Certificates = s.certificates(time.Now())
	resp.Trust = s.trust()
	resp.Time = s.timeStatus(ctx)
	resp.Boot = s.bootStatus()
	return connect.NewResponse(resp), nil
}

func mountPoints(mountInfo string) (map[string]bool, error) {
	data, err := os.ReadFile(mountInfo)
	if err != nil {
		return nil, err
	}
	points := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 4 {
			points[fields[4]] = true
		}
	}
	return points, nil
}

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

func (s *Server) Reboot(ctx context.Context, _ *connect.Request[nodev1.RebootRequest]) (*connect.Response[nodev1.RebootResponse], error) {
	s.rebootSoon()
	return connect.NewResponse(&nodev1.RebootResponse{}), nil
}
