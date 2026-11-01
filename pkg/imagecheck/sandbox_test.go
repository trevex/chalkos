package imagecheck

import (
	"slices"
	"testing"
	"time"
)

// The checker's unit runs the image's chalkd, bound over an empty file chalkd leaves in the
// check's directory, with the image's store bound over /nix/store, in the sandbox.
func TestUnitArgs(t *testing.T) {
	s := Sandbox{Dir: "/run/chalkos-check", Memory: "512M", Timeout: 2 * time.Minute}
	got := s.unitArgs(Image{Store: "/run/chalkd/upgrade-store/nix/store", System: "/nix/store/s-system", Checker: "/run/chalkd/upgrade-store/nix/store/c-chalkd/bin/chalkd"})
	want := []string{
		"systemd-run", "--quiet", "--service-type=oneshot", "--unit=chalkos-check.service",
		"--property=DynamicUser=yes",
		"--property=CapabilityBoundingSet=",
		"--property=AmbientCapabilities=",
		"--property=NoNewPrivileges=yes",
		"--property=ProtectSystem=strict",
		"--property=ProtectHome=yes",
		"--property=ProtectProc=invisible",
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
		"--property=BindReadOnlyPaths=/run/chalkd/upgrade-store/nix/store:/nix/store",
		"--property=BindReadOnlyPaths=/run/chalkd/upgrade-store/nix/store/c-chalkd/bin/chalkd:/run/chalkos-check/chalkd",
		"--property=MemoryMax=512M",
		"--property=TimeoutStartSec=120000ms",
		"--property=StandardInput=file:/run/chalkos-check/input",
		"--property=StandardOutput=truncate:/run/chalkos-check/result",
		"--property=StandardError=journal",
		"--", "/run/chalkos-check/chalkd", "check", "--system", "/nix/store/s-system", "--facts", "/run/chalkos-check/facts.sock",
	}
	if !slices.Equal(got, want) {
		t.Errorf("unitArgs =\n%q\nwant\n%q", got, want)
	}
}
