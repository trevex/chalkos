package imagecheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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
//     no Secret's value; checks may open netlink sockets in a network namespace of their own, as
//     the renderer sandbox's do;
//   - runs the checker from Dir/chalkd, an empty file on the running system that the image's
//     chalkd is bound over: systemd-run looks the program up on the running system, whose store
//     lacks the image's paths, and systemd runs it once the unit's mounts are in place;
//   - reads its input from Dir/input, writes its result to Dir/result, which systemd opens before
//     the unit runs, and asks for facts on Dir/facts.sock;
//   - may take Memory and Timeout, for the checker and its renderers together.
//
// Dir is chalkd's directory of the check, which the checker's user may enter but not list.
type Sandbox struct {
	Dir     string
	Memory  string
	Timeout time.Duration
}

// Unit is the unit the checker runs as. chalkd runs one check at a time, under its config lock.
const Unit = "chalkos-check.service"

// manager reaches systemd's manager on its private socket, as the renderer sandbox does.
const manager = "DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/systemd/private"

// stopMargin is how much longer than the unit's timeout chalkd waits for systemd-run.
const stopMargin = 10 * time.Second

// DefaultSandbox is the sandbox of a node: 512 MiB and two minutes.
func DefaultSandbox() Sandbox {
	return Sandbox{Dir: "/run/chalkos-check", Memory: "512M", Timeout: 2 * time.Minute}
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
	"--property=ProtectKernelTunables=yes",
	"--property=ProtectKernelModules=yes",
	"--property=ProtectKernelLogs=yes",
	"--property=ProtectControlGroups=yes",
	"--property=RestrictAddressFamilies=AF_UNIX AF_NETLINK",
	"--property=InaccessiblePaths=-/state -/var -/run/chalkos",
	"--property=DefaultDependencies=no",
}

// unitArgs is the systemd-run command line that runs the checker of img from s.Dir.
func (s Sandbox) unitArgs(img Image) []string {
	args := append([]string{"systemd-run", "--quiet", "--service-type=oneshot", "--unit=" + Unit}, properties...)
	checker := filepath.Join(s.Dir, "chalkd")
	return append(args,
		"--property=BindReadOnlyPaths="+img.Store+":/nix/store",
		"--property=BindReadOnlyPaths="+img.Checker+":"+checker,
		"--property=MemoryMax="+s.Memory,
		fmt.Sprintf("--property=TimeoutStartSec=%dms", s.Timeout.Milliseconds()),
		"--property=StandardInput=file:"+filepath.Join(s.Dir, "input"),
		"--property=StandardOutput=truncate:"+filepath.Join(s.Dir, "result"),
		"--property=StandardError=journal",
		"--", checker, "check", "--system", img.System, "--facts", filepath.Join(s.Dir, "facts.sock"),
	)
}

// Run runs the checker of img on in, with f's facts.
func (s Sandbox) Run(ctx context.Context, img Image, in *checkv1.Input, f Facts) (*checkv1.Result, error) {
	if err := os.RemoveAll(s.Dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Dir, 0o711); err != nil {
		return nil, err
	}
	defer os.RemoveAll(s.Dir)
	input, err := protojson.Marshal(in)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "input"), input, 0o600); err != nil {
		return nil, err
	}
	// What systemd-run finds and the unit binds the image's checker over.
	if err := os.WriteFile(filepath.Join(s.Dir, "chalkd"), nil, 0o755); err != nil {
		return nil, err
	}
	sock := filepath.Join(s.Dir, "facts.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sock, 0o666); err != nil {
		l.Close()
		return nil, err
	}
	stop := ServeFacts(l, f)
	defer stop()
	waitCtx, cancel := context.WithTimeout(ctx, s.Timeout+stopMargin)
	defer cancel()
	_, err = command.Run(waitCtx, s.unitArgs(img), nil, command.MaxOutput, manager)
	if err := s.failed(waitCtx, err); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, "result"))
	if err != nil {
		return nil, err
	}
	res := &checkv1.Result{}
	if err := protojson.Unmarshal(data, res); err != nil {
		return nil, fmt.Errorf("the image's checker answered no result: %w", err)
	}
	return res, nil
}

// failed is the error of a checker that did not end well: systemd-run's err, the unit's result
// in place of an exit status, or the wait's end. A unit that failed or timed out is reset.
func (s Sandbox) failed(waitCtx context.Context, err error) error {
	systemctl := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), stopMargin)
		defer cancel()
		return command.Run(ctx, append([]string{"systemctl"}, args...), nil, command.MaxOutput, manager)
	}
	if waitCtx.Err() != nil {
		systemctl("stop", Unit)
		systemctl("reset-failed", Unit)
		return fmt.Errorf("the image's checker did not finish: %w", waitCtx.Err())
	}
	var exit *command.Error
	if err == nil || !errors.As(err, &exit) {
		return err
	}
	show, serr := systemctl("show", "--property=Result", "--property=ExecMainStatus", Unit)
	systemctl("reset-failed", Unit)
	status := exit.Status
	if serr == nil {
		props := map[string]string{}
		for _, line := range strings.Split(string(show), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				props[k] = v
			}
		}
		status = "exit " + props["ExecMainStatus"]
		if r := props["Result"]; r != "exit-code" && r != "" {
			status = r
		}
	}
	return fmt.Errorf("the image's checker failed (%s); see the journal of %s", status, Unit)
}
