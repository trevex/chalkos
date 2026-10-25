package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeSystemd puts systemd-run and systemctl first on PATH. systemd-run records its unit and runs
// the command after --; systemctl records each call and answers show with the properties given.
// It returns the directory they record in.
func fakeSystemd(t *testing.T, show string) string {
	t.Helper()
	dir, bin := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"systemd-run": `for a; do case $a in --unit=*) echo "${a#--unit=}" >"$DIAG/unit" ;; esac; done
while [ "$1" != -- ]; do shift; done
shift
exec "$@"`,
		"systemctl": `echo "$*" >>"$DIAG/systemctl"
if [ "$1" = show ]; then printf '%s' "$SHOW"; fi`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DIAG", dir)
	t.Setenv("SHOW", show)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return dir
}

func recorded(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(data)
}

// A run that succeeds streams its output and asks systemd nothing more; its unit is the
// diagnostic's, with a name of its own.
func TestSandboxRuns(t *testing.T) {
	dir := fakeSystemd(t, "")
	var stdout, stderr bytes.Buffer
	exit, err := Sandbox{}.Run(context.Background(), Run{Name: "route-get", Argv: []string{"/bin/sh", "-c", "echo out; echo err >&2"}, Timeout: 10 * time.Second}, &stdout, &stderr)
	if err != nil || exit != (Exit{}) || stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatalf("Run = %+v, %v; stdout %q, stderr %q", exit, err, stdout.String(), stderr.String())
	}
	unit := strings.TrimSpace(recorded(t, dir, "unit"))
	if !regexp.MustCompile(`^chalkos-diagnostic-route-get-[0-9a-f]{12}\.service$`).MatchString(unit) {
		t.Errorf("unit %q", unit)
	}
	if calls := recorded(t, dir, "systemctl"); calls != "" {
		t.Errorf("systemctl ran: %q", calls)
	}
}

// A run that fails is told apart by its unit's result, which systemd keeps until the unit is
// reset: an exit status, the timeout, a signal, or systemd-run's own failure.
func TestSandboxReportsHowARunEnded(t *testing.T) {
	for name, tc := range map[string]struct {
		show string
		want Exit
	}{
		"an exit status":        {"Result=exit-code\nExecMainStatus=3\n", Exit{Code: 3}},
		"its timeout":           {"Result=timeout\nExecMainStatus=0\n", Exit{Stopped: StoppedTimeout}},
		"a signal":              {"Result=signal\nExecMainStatus=9\n", Exit{Stopped: "signal"}},
		"systemd-run's failure": {"Result=success\nExecMainStatus=0\n", Exit{Code: 3}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := fakeSystemd(t, tc.show)
			exit, err := Sandbox{}.Run(context.Background(), Run{Name: "ping", Argv: []string{"/bin/sh", "-c", "exit 3"}, Timeout: 10 * time.Second}, io.Discard, io.Discard)
			if err != nil || exit != tc.want {
				t.Fatalf("Run = %+v, %v; want %+v", exit, err, tc.want)
			}
			unit := strings.TrimSpace(recorded(t, dir, "unit"))
			want := "show --property=Result --property=ExecMainStatus " + unit + "\nreset-failed " + unit + "\n"
			if got := recorded(t, dir, "systemctl"); got != want {
				t.Errorf("systemctl calls %q, want %q", got, want)
			}
		})
	}
}

// When the call ends, the unit is stopped and reset, and the run returns the context's error.
func TestSandboxStopsTheUnitWhenTheCallEnds(t *testing.T) {
	dir := fakeSystemd(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := Sandbox{}.Run(ctx, Run{Name: "ping", Argv: []string{"/bin/sh", "-c", "exec sleep 30"}, Timeout: time.Minute}, io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	unit := strings.TrimSpace(recorded(t, dir, "unit"))
	if got, want := recorded(t, dir, "systemctl"), "stop "+unit+"\nreset-failed "+unit+"\n"; got != want {
		t.Errorf("systemctl calls %q, want %q", got, want)
	}
}

// The unit runs under a dynamic user, on a read-only system with a private /tmp, without new
// privileges, with the capabilities, groups and time limit of the run alone.
func TestSandboxArgs(t *testing.T) {
	for _, p := range []string{"--property=DynamicUser=yes", "--property=ProtectSystem=strict", "--property=PrivateTmp=yes", "--property=NoNewPrivileges=yes"} {
		if !slices.Contains(sandbox, p) {
			t.Errorf("the sandbox lacks %s", p)
		}
	}
	got := sandboxArgs("chalkos-diagnostic-ping-0123456789ab.service", Run{
		Argv:         []string{"/nix/store/x-iputils/bin/ping", "-c", "4", "192.0.2.1"},
		Capabilities: []string{"CAP_NET_RAW"},
		Groups:       []string{"bird", "wheel"},
		Timeout:      20 * time.Second,
	})
	want := append(append([]string{"--pipe", "--wait", "--quiet", "--expand-environment=no", "--unit=chalkos-diagnostic-ping-0123456789ab.service"}, sandbox...),
		"--property=CapabilityBoundingSet=CAP_NET_RAW",
		"--property=AmbientCapabilities=CAP_NET_RAW",
		"--property=RuntimeMaxSec=20000ms",
		"--property=SupplementaryGroups=bird wheel",
		"--", "/nix/store/x-iputils/bin/ping", "-c", "4", "192.0.2.1")
	if !slices.Equal(got, want) {
		t.Errorf("sandboxArgs =\n%q\nwant\n%q", got, want)
	}
	none := sandboxArgs("u.service", Run{Argv: []string{"/nix/store/x/bin/ip"}, Timeout: time.Second})
	if !slices.Contains(none, "--property=CapabilityBoundingSet=") || !slices.Contains(none, "--property=AmbientCapabilities=") {
		t.Errorf("a run without capabilities keeps some: %q", none)
	}
	if slices.ContainsFunc(none, func(a string) bool { return strings.HasPrefix(a, "--property=SupplementaryGroups") }) {
		t.Errorf("a run without groups gets some: %q", none)
	}
}

// A run without a time limit is refused before systemd-run starts.
func TestSandboxRefusesARunWithoutATimeout(t *testing.T) {
	dir := fakeSystemd(t, "")
	if _, err := (Sandbox{}).Run(context.Background(), Run{Name: "ping", Argv: []string{"/bin/true"}}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("err = %v, want a refusal of the timeout", err)
	}
	if unit := recorded(t, dir, "unit"); unit != "" {
		t.Errorf("systemd-run ran %q", unit)
	}
}
