package imagecheck

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
)

// The checker's unit runs the image's chalkd, bound over an empty file chalkd leaves in the
// public directory, with the image's store bound over /nix/store and the facts socket bound from
// chalkd's own directory, in the sandbox.
func TestUnitArgs(t *testing.T) {
	got := DefaultSandbox("/run/chalkd/check").unitArgs(Image{Store: "/run/chalkd/upgrade-store/nix/store", System: "/nix/store/s-system", Checker: "/run/chalkd/upgrade-store/nix/store/c-chalkd/bin/chalkd"})
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
		"--property=InaccessiblePaths=-/state -/var -/run/chalkos",
		"--property=MemorySwapMax=0",
		"--property=LimitFSIZE=33554432",
		"--property=DefaultDependencies=no",
		"--property=BindReadOnlyPaths=/run/chalkd/upgrade-store/nix/store:/nix/store",
		"--property=BindReadOnlyPaths=/run/chalkd/upgrade-store/nix/store/c-chalkd/bin/chalkd:/run/chalkos-check/chalkd",
		"--property=BindPaths=/run/chalkd/check/facts.sock:/run/chalkos-check/facts.sock",
		"--property=MemoryMax=512M",
		"--property=TimeoutStartSec=120000ms",
		"--property=StandardInput=file:/run/chalkd/check/input",
		"--property=StandardOutput=truncate:/run/chalkd/check/result",
		"--property=StandardError=journal",
		"--", "/run/chalkos-check/chalkd", "check", "--system", "/nix/store/s-system", "--facts", "/run/chalkos-check/facts.sock",
	}
	if !slices.Equal(got, want) {
		t.Errorf("unitArgs =\n%q\nwant\n%q", got, want)
	}
}

// fakeSystemd puts a systemd-run and a systemctl on PATH. systemd-run notes the modes of the
// check's files and runs body with $in and $out, the input and the result systemd opens;
// systemctl answers show with show. Both log their arguments to the returned file.
func fakeSystemd(t *testing.T, body, show string) (log string) {
	t.Helper()
	bin := t.TempDir()
	log = filepath.Join(bin, "log")
	run := `#!/bin/sh
for a in "$@"; do
  case $a in
  --property=StandardInput=file:*) in=${a#*file:} ;;
  --property=StandardOutput=truncate:*) out=${a#*truncate:} ;;
  esac
done
dir=$(dirname "$out")
echo "systemd-run modes $(stat -c %a "$dir" "$dir/input" "$out" "$dir/facts.sock" | tr '\n' ' ')" >>` + log + `
` + body + "\n"
	ctl := "#!/bin/sh\necho \"systemctl $*\" >>" + log + "\n[ \"$1\" = show ] && printf '%s' '" + show + "'\nexit 0\n"
	for name, script := range map[string]string{"systemd-run": run, "systemctl": ctl} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return log
}

// testSandbox is a sandbox below a directory of the test's own.
func testSandbox(t *testing.T) Sandbox {
	root := t.TempDir()
	return Sandbox{Dir: filepath.Join(root, "private"), Public: filepath.Join(root, "public"), Memory: "512M", Timeout: time.Minute}
}

// Run stops a unit a crashed chalkd left behind, hands the unit its input in a directory and a
// result root's alone, reads the result and removes its directories after.
func TestSandboxRun(t *testing.T) {
	log := fakeSystemd(t, `grep -q '"node":"n1"' "$in" && printf '{"identity":"id"}' >"$out"`, "")
	s := testSandbox(t)
	res, err := s.Run(context.Background(), Image{Store: "/s", System: "/nix/store/x", Checker: "/c"}, &checkv1.Input{Node: "n1"}, &fakeFacts{})
	if err != nil || res.GetIdentity() != "id" {
		t.Fatalf("result %v, %v", res, err)
	}
	data, _ := os.ReadFile(log)
	want := "systemctl stop chalkos-check.service\nsystemctl reset-failed chalkos-check.service\nsystemd-run modes 700 600 600 666 \n"
	if string(data) != want {
		t.Errorf("ran\n%s\nwant\n%s", data, want)
	}
	for _, dir := range []string{s.Dir, s.Public} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s is left: %v", dir, err)
		}
	}
}

// A checker that fails is an error with the unit's status, unknown when the manager knows none,
// and one that answers too much or nothing is refused.
func TestSandboxRunFails(t *testing.T) {
	for name, tc := range map[string]struct {
		body, show, want string
	}{
		"exit":      {"exit 1", "Result=exit-code\nExecMainStatus=3\n", "the image's checker failed (exit 3); see the journal of chalkos-check.service"},
		"timeout":   {"exit 1", "Result=timeout\n", "the image's checker failed (timeout); see the journal of chalkos-check.service"},
		"unknown":   {"exit 1", "Result=exit-code\n", "the image's checker failed (unknown); see the journal of chalkos-check.service"},
		"no result": {"printf 'nonsense' >\"$out\"", "", "the image's checker answered no result: "},
		"too much":  {"head -c 33554433 /dev/zero >\"$out\"", "", "the image's checker answered more than 33554432 bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			log := fakeSystemd(t, tc.body, tc.show)
			_, err := testSandbox(t).Run(context.Background(), Image{}, &checkv1.Input{}, &fakeFacts{})
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if data, _ := os.ReadFile(log); tc.show != "" && !strings.HasSuffix(string(data), "systemctl show --property=Result --property=ExecMainStatus chalkos-check.service\nsystemctl reset-failed chalkos-check.service\n") {
				t.Errorf("ran\n%s", data)
			}
		})
	}
}

// A checker chalkd stops waiting for is stopped.
func TestSandboxRunStops(t *testing.T) {
	log := fakeSystemd(t, "exec sleep 5", "")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := testSandbox(t).Run(ctx, Image{}, &checkv1.Input{}, &fakeFacts{})
	if err == nil || !strings.HasPrefix(err.Error(), "the image's checker did not finish: ") {
		t.Errorf("err = %v", err)
	}
	if data, _ := os.ReadFile(log); !strings.HasSuffix(string(data), "systemctl stop chalkos-check.service\nsystemctl reset-failed chalkos-check.service\n") {
		t.Errorf("ran\n%s", data)
	}
}
