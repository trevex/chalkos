package imagecheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	"github.com/trevex/chalkos/pkg/command"
)

// Sandbox runs an image's checker as the oneshot transient unit chalkos-check.service, with the
// image's store bound read-only over /nix/store: the checker's renderers and their checks, and
// every program they name, come from the image. The unit:
//   - runs as a user of its own (DynamicUser), without capabilities, devices or network, on a
//     read-only system without STATE, /var or what chalkd keeps below /run/chalkos, so it reads
//     no Secret's value; checks may open netlink sockets in a user and network namespace of their
//     own, as nft --check takes, and create no other namespace;
//   - runs the checker from Public/chalkd, an empty file on the running system that the image's
//     chalkd is bound over: systemd-run looks the program up on the running system, whose store
//     lacks the image's paths, and systemd runs it once the unit's mounts are in place;
//   - reads its input from Dir/input and writes its result to Dir/result, which systemd opens
//     before the unit runs, and asks for facts on Public/facts.sock, an empty file the socket
//     Dir/facts.sock is bound over in the unit alone;
//   - may take Memory and Timeout, for the checker and its renderers together, and write no file
//     larger than MaxResult.
//
// Dir is chalkd's directory of the check, root's alone, so no other user of the node reaches the
// socket, the input or the result. Public holds the empty files the unit binds over, which
// anyone may run or open, to no effect.
type Sandbox struct {
	Dir     string
	Public  string
	Memory  string
	Timeout time.Duration
}

// Unit is the unit the checker runs as. chalkd runs one check at a time, under its config lock.
const Unit = "chalkos-check.service"

// MaxResult bounds the checker's result and every file it or its renderers write: the result holds
// the config with its defaults, delivered within chalkd's 4 MiB of a message, and an output's
// files take at most 16 MiB.
const MaxResult = 32 << 20

// stopMargin is how much longer than the unit's timeout chalkd waits for systemd-run.
const stopMargin = 10 * time.Second

// DefaultSandbox is the sandbox of a node: 512 MiB and two minutes.
func DefaultSandbox() Sandbox {
	return Sandbox{Dir: "/run/chalkd/check", Public: "/run/chalkos-check", Memory: "512M", Timeout: 2 * time.Minute}
}

// properties are the unit's sandbox, as systemd-run's options.
var properties = []string{
	"--property=DynamicUser=yes",
	"--property=CapabilityBoundingSet=",
	"--property=AmbientCapabilities=",
	"--property=NoNewPrivileges=yes",
	"--property=ProtectSystem=strict",
	"--property=ProtectHome=yes",
	"--property=ProtectProc=invisible",
	// Renderers stage each output's files below /tmp; an output may take 16 MiB.
	"--property=TemporaryFileSystem=/tmp:size=128M,mode=1777",
	"--property=PrivateDevices=yes",
	"--property=PrivateNetwork=yes",
	"--property=IPAddressDeny=any",
	"--property=ProtectKernelTunables=yes",
	"--property=ProtectKernelModules=yes",
	"--property=ProtectKernelLogs=yes",
	"--property=ProtectControlGroups=yes",
	"--property=ProtectClock=yes",
	"--property=ProtectHostname=yes",
	"--property=LockPersonality=yes",
	"--property=RestrictRealtime=yes",
	"--property=SystemCallArchitectures=native",
	"--property=RestrictNamespaces=user net",
	"--property=RestrictAddressFamilies=AF_UNIX AF_NETLINK",
	// The ESP is root's alone (umask=0077), and images have no /boot.
	"--property=InaccessiblePaths=-/state -/var -/run/chalkos",
	"--property=MemorySwapMax=0",
	fmt.Sprintf("--property=LimitFSIZE=%d", MaxResult),
	"--property=DefaultDependencies=no",
}

// unitArgs is the systemd-run command line that runs the checker of img.
func (s Sandbox) unitArgs(img Image) []string {
	args := append([]string{"systemd-run", "--quiet", "--service-type=oneshot", "--unit=" + Unit}, properties...)
	checker, socket := filepath.Join(s.Public, "chalkd"), filepath.Join(s.Public, "facts.sock")
	return append(args,
		"--property=BindReadOnlyPaths="+img.Store+":/nix/store",
		"--property=BindReadOnlyPaths="+img.Checker+":"+checker,
		"--property=BindPaths="+filepath.Join(s.Dir, "facts.sock")+":"+socket,
		"--property=MemoryMax="+s.Memory,
		fmt.Sprintf("--property=TimeoutStartSec=%dms", s.Timeout.Milliseconds()),
		"--property=StandardInput=file:"+filepath.Join(s.Dir, "input"),
		"--property=StandardOutput=truncate:"+filepath.Join(s.Dir, "result"),
		"--property=StandardError=journal",
		"--", checker, "check", "--system", img.System, "--facts", socket,
	)
}

// Run runs the checker of img on in, with f's facts.
func (s Sandbox) Run(ctx context.Context, img Image, in *checkv1.Input, f Facts) (*checkv1.Result, error) {
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{{s.Dir, 0o700}, {s.Public, 0o755}} {
		if err := os.RemoveAll(dir.path); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir.path)
	}
	input, err := protojson.Marshal(in)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "input"), input, 0o600); err != nil {
		return nil, err
	}
	// systemd truncates the result and keeps its mode: root's alone.
	if err := os.WriteFile(filepath.Join(s.Dir, "result"), nil, 0o600); err != nil {
		return nil, err
	}
	// What systemd-run finds and the unit binds the image's checker and the socket over.
	if err := os.WriteFile(filepath.Join(s.Public, "chalkd"), nil, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(s.Public, "facts.sock"), nil, 0o600); err != nil {
		return nil, err
	}
	sock := filepath.Join(s.Dir, "facts.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	// The checker's user connects through the bind; Dir keeps everyone else out.
	if err := os.Chmod(sock, 0o666); err != nil {
		l.Close()
		return nil, err
	}
	stop := ServeFacts(l, f)
	defer stop()
	// A unit a chalkd that crashed left behind would keep systemd-run from starting one.
	command.StopUnit(Unit)
	waitCtx, cancel := context.WithTimeout(ctx, s.Timeout+stopMargin)
	defer cancel()
	_, err = command.Run(waitCtx, s.unitArgs(img), nil, command.MaxOutput, command.Manager)
	if err := failed(waitCtx, err); err != nil {
		return nil, err
	}
	data, err := readResult(filepath.Join(s.Dir, "result"))
	if err != nil {
		return nil, err
	}
	res := &checkv1.Result{}
	if err := protojson.Unmarshal(data, res); err != nil {
		return nil, fmt.Errorf("the image's checker answered no result: %w", err)
	}
	return res, nil
}

// readResult reads the result at path, refusing more than MaxResult bytes, which LimitFSIZE keeps
// the checker from writing.
func readResult(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxResult+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxResult {
		return nil, fmt.Errorf("the image's checker answered more than %d bytes", MaxResult)
	}
	return data, nil
}

// failed is the error of a checker that did not end well: systemd-run's err, the unit's result
// in place of an exit status, or the wait's end. A unit that failed or timed out is reset.
func failed(waitCtx context.Context, err error) error {
	if waitCtx.Err() != nil {
		command.StopUnit(Unit)
		return fmt.Errorf("the image's checker did not finish: %w", waitCtx.Err())
	}
	exit, ok := errors.AsType[*command.Error](err)
	if err == nil || !ok {
		return err
	}
	status := exit.Status
	if _, s, ok := command.FailedUnit(Unit); ok {
		status = s
	}
	return fmt.Errorf("the image's checker failed (%s); see the journal of %s", status, Unit)
}
