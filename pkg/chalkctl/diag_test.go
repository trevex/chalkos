package chalkctl

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/diagnostics"
	"github.com/trevex/chalkos/pkg/pki"
)

// testDiagnostics declares route-get, which readers run, and ping, which needs an operator.
const testDiagnostics = `{
  "route-get": {"description": "The route the kernel takes to an address.",
    "command": ["/nix/store/x-iproute2/bin/ip", "-json", "route", "get", "{address}"],
    "parameters": {"address": {"type": "address", "description": "IPv4 or IPv6 address."}},
    "role": "reader", "timeout": "10s"},
  "ping": {"description": "Pings an address.",
    "command": ["/nix/store/x-iputils/bin/ping", "-c", "{count}", "{address}"],
    "parameters": {"address": {"type": "address", "description": "Address to ping."},
      "count": {"type": "int", "description": "Echo requests.", "min": 1, "max": 10, "default": "4"}},
    "role": "operator", "timeout": "20s", "capabilities": ["CAP_NET_RAW"]}}`

// fakeDiagnose records each run and answers it with the output and exit given.
type fakeDiagnose struct {
	mu             sync.Mutex
	runs           []diagnostics.Run
	stdout, stderr string
	exit           diagnostics.Exit
}

func (f *fakeDiagnose) Run(_ context.Context, r diagnostics.Run, stdout, stderr io.Writer) (diagnostics.Exit, error) {
	f.mu.Lock()
	f.runs = append(f.runs, r)
	f.mu.Unlock()
	io.WriteString(stdout, f.stdout)
	io.WriteString(stderr, f.stderr)
	return f.exit, nil
}

func (f *fakeDiagnose) recorded() []diagnostics.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

// diagNode serves n1 in normal mode with testDiagnostics, which f runs, and returns its address.
func diagNode(t *testing.T, ta *testApp, f *fakeDiagnose) string {
	t.Helper()
	current, _ := json.Marshal(ta.identity(t))
	s, _ := installedNode(t, ta, current)
	s.Paths.Diagnostics = filepath.Join(t.TempDir(), "diagnostics.json")
	writeFile(t, s.Paths.Diagnostics, testDiagnostics)
	s.Diagnose = f
	return ta.startNode(t, s)
}

// node diag <node> lists each diagnostic with its role and description, and below it each
// parameter: in brackets when a call may leave it out, with its range, values and default.
func TestNodeDiagLists(t *testing.T) {
	ta := newTestApp(t)
	addr := diagNode(t, ta, &fakeDiagnose{})
	if err := ta.run(context.Background(), ta.args([]string{"node", "diag", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := "DIAGNOSTIC           ROLE      DESCRIPTION\n" +
		"ping                 operator  Pings an address.\n" +
		"  address=<address>            Address to ping.\n" +
		"  [count=<1-10>]               Echo requests. Default: 4.\n" +
		"route-get            reader    The route the kernel takes to an address.\n" +
		"  address=<address>            IPv4 or IPv6 address.\n"
	if got := ta.stdout.String(); got != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", got, want)
	}
}

// node diag <node> <diagnostic> <parameter>=<value>... runs it and prints its standard output and
// error as the node streams them.
func TestNodeDiagRuns(t *testing.T) {
	ta := newTestApp(t)
	f := &fakeDiagnose{stdout: `[{"dst":"2001:db8::1"}]` + "\n", stderr: "a warning\n"}
	addr := diagNode(t, ta, f)
	if err := ta.run(context.Background(), ta.args([]string{"node", "diag", "n1", "route-get", "address=2001:db8::1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if ta.stdout.String() != f.stdout || !strings.Contains(ta.stderr.String(), f.stderr) {
		t.Errorf("stdout %q, stderr %q", ta.stdout.String(), ta.stderr.String())
	}
	want := []diagnostics.Run{{Name: "route-get", Argv: []string{"/nix/store/x-iproute2/bin/ip", "-json", "route", "get", "2001:db8::1"}, Timeout: 10 * time.Second}}
	if got := f.recorded(); !reflect.DeepEqual(got, want) {
		t.Errorf("runs = %+v, want %+v", got, want)
	}
}

// node diag fails when the diagnostic does: with its exit status, or why the node stopped it.
func TestNodeDiagFailsWithTheDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		exit diagnostics.Exit
		want string
	}{
		{diagnostics.Exit{Code: 1}, "ping exited with status 1"},
		{diagnostics.Exit{Stopped: diagnostics.StoppedTimeout}, "ping ran longer than its timeout, and the node stopped it"},
		{diagnostics.Exit{Stopped: diagnostics.StoppedOutputLimit}, "ping printed more than 1 MiB, and the node stopped it there"},
		{diagnostics.Exit{Stopped: "signal"}, "ping ended: signal"},
	} {
		ta := newTestApp(t)
		addr := diagNode(t, ta, &fakeDiagnose{exit: tc.exit})
		err := ta.run(context.Background(), ta.args([]string{"node", "diag", "n1", "ping", "address=192.0.2.1"}, addr))
		if err == nil || err.Error() != tc.want {
			t.Errorf("%+v: err = %v, want %q", tc.exit, err, tc.want)
		}
	}
}

// The node refuses a diagnostic the client's role does not allow, and arguments of the wrong
// type, as chalkctl passes them on unchecked.
func TestNodeDiagRefusals(t *testing.T) {
	ta := newTestApp(t)
	addr := diagNode(t, ta, &fakeDiagnose{})
	reader := ta.writeConfig(t, "alice", pki.RoleReader, 24*time.Hour, time.Now())
	err := ta.run(context.Background(), ta.args([]string{"node", "diag", "n1", "ping", "address=192.0.2.1", "--config", reader}, addr))
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "the diagnostic ping needs the operator role") {
		t.Errorf("ping as a reader: %v", err)
	}
	err = ta.run(context.Background(), ta.args([]string{"node", "diag", "n1", "route-get", "address=example.org"}, addr))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("route-get address=example.org: %v", err)
	}
}
