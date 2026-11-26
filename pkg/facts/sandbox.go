package facts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/trevex/chalkos/pkg/command"
)

// Sandbox runs command facts as transient units, in command.Hardened as root without network, with
// a private /tmp and STATE read-only. A fact reads the node's disks through sysfs, the udev
// database and the links of /dev, and the files of STATE that root alone reads, so it runs as
// root, but without capabilities and devices. A fact that reads the fallback secret tests it on
// the node's encrypted volumes: it may read and write block devices and keep CAP_SYS_ADMIN, since
// cryptsetup maps a device for a cipher the kernel crypto API lacks, and it has no memory cap,
// since cryptsetup's argon2 takes up to half of the node's memory.
//
// Store, when set, is a non-running image's store, mounted as the unit's /nix/store; the fact's
// program runs from it, links followed within it. systemd-run sets no separate argv[0], so a fact
// names its program, not a link to a multi-call binary such as kmod's lsmod.
type Sandbox struct {
	Store string
}

// sandbox holds the properties of every fact's unit, command.Hardened and its own.
var sandbox = append(slices.Clone(command.Hardened),
	"--property=ReadOnlyPaths=-/state",
	"--property=PrivateTmp=yes",
	"--property=PrivateNetwork=yes",
	"--property=RestrictNamespaces=yes",
	"--property=RestrictAddressFamilies=AF_UNIX AF_ALG",
	// The device nodes stay, for the links of /dev, but the unit opens none it is not given.
	"--property=DevicePolicy=closed",
)

// unprivileged are the properties of a fact that does not read the fallback secret.
var unprivileged = []string{
	"--property=CapabilityBoundingSet=",
	"--property=AmbientCapabilities=",
	"--property=MemoryMax=256M",
}

// passphraseTest are the properties of a fact that reads the fallback secret, for cryptsetup's
// passphrase test.
var passphraseTest = []string{
	"--property=CapabilityBoundingSet=CAP_SYS_ADMIN",
	"--property=AmbientCapabilities=",
	"--property=DeviceAllow=block-* rw",
	"--property=DeviceAllow=/dev/mapper/control rw",
	// cryptsetup's lock directory.
	"--property=ReadWritePaths=-/run/cryptsetup",
}

func (s Sandbox) Run(ctx context.Context, c Command, stdin []byte, limit int) ([]byte, error) {
	unit := unitName(c.Name)
	args, err := s.command(unit, c, runTime(ctx))
	if err != nil {
		return nil, err
	}
	out, err := command.Run(ctx, args, stdin, limit)
	if ctx.Err() != nil {
		// systemd-run is gone, and the unit may run on.
		command.StopUnit(unit)
	}
	return out, err
}

// runTime is how long a unit runs before systemd stops it: until ctx's deadline, or Timeout.
func runTime(ctx context.Context) time.Duration {
	if d, ok := ctx.Deadline(); ok {
		return max(time.Until(d), time.Millisecond)
	}
	return Timeout
}

// unitName is a name for a run of the fact that no other run has.
func unitName(fact string) string {
	b := make([]byte, 6)
	rand.Read(b)
	return "chalkos-fact-" + fact + "-" + hex.EncodeToString(b) + ".service"
}

// command is the command line that runs c as unit for at most runFor.
func (s Sandbox) command(unit string, c Command, runFor time.Duration) ([]string, error) {
	args := append([]string{"systemd-run", "--pipe", "--wait", "--quiet", "--collect", "--unit=" + unit}, sandbox...)
	if c.Secret {
		args = append(args, passphraseTest...)
	} else {
		args = append(args, unprivileged...)
	}
	// RuntimeMaxSec stops the unit itself when chalkd stops waiting.
	args = append(args, fmt.Sprintf("--property=RuntimeMaxSec=%dms", runFor.Milliseconds()))
	argv := c.Argv
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
