package facts

import (
	"context"
	"fmt"

	"github.com/trevex/chalkos/pkg/command"
)

// Sandbox runs command facts as transient units that cannot change the node: read-only file
// systems but /dev and a private /tmp, programs from the store alone, no network, no kernel
// tunables or module loading, and no new privileges. Devices stay readable and root keeps its
// capabilities but CAP_SYS_MODULE, because resolving disks and testing a passphrase read block
// devices, and cryptsetup may map a device for a cipher the kernel crypto API lacks.
//
// Store, when set, is a non-running image's store, mounted as the unit's /nix/store; the fact's
// program runs from it, links followed within it. systemd-run sets no separate argv[0], so a fact
// names its program, not a link to a multi-call binary such as kmod's lsmod.
type Sandbox struct {
	Store string
}

// sandbox holds the properties of every fact's unit, which runs programs from the store alone.
var sandbox = append([]string{
	"--property=ProtectSystem=strict",
	"--property=ReadOnlyPaths=-/state",
	"--property=ProtectHome=yes",
	"--property=PrivateTmp=yes",
	"--property=ProtectKernelTunables=yes",
	"--property=ProtectKernelModules=yes",
	"--property=ProtectControlGroups=yes",
	"--property=PrivateNetwork=yes",
	"--property=RestrictAddressFamilies=AF_UNIX AF_ALG",
	"--property=NoNewPrivileges=yes",
}, command.StoreOnly...)

func (s Sandbox) Run(ctx context.Context, argv []string, stdin []byte, limit int) ([]byte, error) {
	cmd, err := s.command(argv)
	if err != nil {
		return nil, err
	}
	return command.Run(ctx, cmd, stdin, limit)
}

// command is the command line that runs argv in the sandbox.
func (s Sandbox) command(argv []string) ([]string, error) {
	args := append([]string{"systemd-run", "--pipe", "--wait", "--quiet", "--collect"}, sandbox...)
	args = append(args,
		// RuntimeMaxSec stops the unit itself when chalkd stops waiting.
		fmt.Sprintf("--property=RuntimeMaxSec=%d", int(Timeout.Seconds())),
		// cryptsetup's lock directory, for the passphrase test.
		"--property=ReadWritePaths=-/run/cryptsetup")
	if s.Store != "" {
		// The program runs from where the store is on the node, which systemd-run finds.
		args = append(args, "--property=BindReadOnlyPaths="+s.Store+":/nix/store", "--property=ExecPaths="+s.Store)
		if command.InStore(argv[0]) {
			program, err := command.ResolveInStore(s.Store, argv[0])
			if err != nil {
				return nil, err
			}
			argv = append([]string{program}, argv[1:]...)
		}
	}
	return append(append(args, "--"), argv...), nil
}
