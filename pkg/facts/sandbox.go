package facts

import (
	"context"
	"fmt"

	"github.com/trevex/chalkos/pkg/command"
)

// Sandbox runs command facts as transient units of systemd, so a fact reads the node without
// changing it: the file system, STATE among it where the node has one, is read-only but for /dev
// and the unit's own, writable /tmp and /var/tmp; home directories are hidden; the kernel's
// tunables and control groups cannot be changed, and the unit cannot load a kernel module itself,
// although the kernel still loads one on request, as for a socket family; the unit has no
// network, opens no socket but Unix and kernel crypto ones, and cannot gain privileges. Devices
// stay accessible and the unit keeps root's capabilities but CAP_SYS_MODULE, since resolving
// disks and testing a passphrase read block devices and cryptsetup may set up a temporary
// device-mapper mapping for a cipher the kernel crypto API lacks; cryptsetup's lock directory
// stays writable, where it exists, for the passphrase test.
//
// Store, when set, is where the store of an image that does not run is on this node, as chalkd
// mounted it: the unit sees it as its /nix/store, so a fact of that image runs that image's
// programs. The fact's own program runs from Store, where systemd-run finds it: the running
// system's store may lack it. A program that is a link, as a package's bin/ often holds one into
// another package, is followed within Store first, as the running system would follow it into its
// own store.
type Sandbox struct {
	Store string
}

func (s Sandbox) Run(ctx context.Context, argv []string, stdin []byte, limit int) ([]byte, error) {
	cmd, err := s.command(argv)
	if err != nil {
		return nil, err
	}
	return command.Run(ctx, cmd, stdin, limit)
}

// command is the command line that runs argv in the sandbox.
func (s Sandbox) command(argv []string) ([]string, error) {
	extra := []string{"--property=ReadWritePaths=-/run/cryptsetup"}
	if s.Store != "" {
		extra = append(extra, "--property=BindReadOnlyPaths="+s.Store+":/nix/store")
		if command.InStore(argv[0]) {
			program, err := command.ResolveInStore(s.Store, argv[0])
			if err != nil {
				return nil, err
			}
			argv = append([]string{program}, argv[1:]...)
		}
	}
	return sandboxed(argv, extra...), nil
}

// sandboxed is the command line that runs argv in the sandbox Sandbox describes, with the extra
// options of systemd-run given.
func sandboxed(argv []string, extra ...string) []string {
	args := append([]string{"systemd-run", "--pipe", "--wait", "--quiet", "--collect"}, sandboxProperties()...)
	// RuntimeMaxSec stops the unit itself when chalkd stops waiting.
	args = append(args, fmt.Sprintf("--property=RuntimeMaxSec=%d", int(Timeout.Seconds())))
	args = append(args, extra...)
	return append(append(args, "--"), argv...)
}

// sandboxProperties are the properties of the sandbox Sandbox describes, without cryptsetup's
// lock directory and the time limit, as options of systemd-run.
func sandboxProperties() []string {
	return []string{
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
	}
}
