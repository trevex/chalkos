package chalkd

import (
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/contract"
)

// shortDir is a temporary directory whose path leaves room for a socket's name: a socket's path
// is at most 107 bytes, which t.TempDir's names of long tests pass.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "chalkd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// serveConsole serves s's console socket and returns a client of it.
func serveConsole(t *testing.T, s *Server) *client.Conn {
	t.Helper()
	ln, err := ListenConsole(filepath.Join(shortDir(t), "run"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.ConsoleHandler()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	c := client.DialSocket(ln.Addr().String())
	t.Cleanup(c.Close)
	return c
}

// TestListenConsole narrows chalkd's run directory and the socket to root and replaces a socket
// a chalkd before left behind.
func TestListenConsole(t *testing.T) {
	dir := filepath.Join(shortDir(t), "run")
	write(t, filepath.Join(dir, ConsoleSocket), "left behind")
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := ListenConsole(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("run directory %v, %v; want 0700", fi.Mode(), err)
	}
	fi, err := os.Stat(filepath.Join(dir, ConsoleSocket))
	if err != nil || fi.Mode().Type() != fs.ModeSocket || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket %v, %v; want a socket of 0600", fi.Mode(), err)
	}
}

// with is codes with more added.
func with(codes, more map[string]connect.Code) map[string]connect.Code {
	all := maps.Clone(codes)
	maps.Copy(all, more)
	return all
}

// TestConsoleAuthorisation serves the API on the console's socket with the console role: what
// each procedure's permission grants the console, its actions only while the image allows them,
// and nothing else. The modes and the layers are checked first, as for every client.
func TestConsoleAuthorisation(t *testing.T) {
	reads := map[string]connect.Code{
		"Info": 0, "Disks": 0, "Status": 0, "Logs": 0, "Diagnostics": 0, "RunDiagnostic": connect.CodeNotFound,
		"Contract": connect.CodePermissionDenied, "ApplyConfig": connect.CodePermissionDenied, "GetConfig": connect.CodePermissionDenied,
		"ResetVolume": connect.CodePermissionDenied, "ConfirmConfig": connect.CodePermissionDenied, "Upgrade": connect.CodePermissionDenied,
		"Install": connect.CodeFailedPrecondition, "EnrollSecureBoot": connect.CodeFailedPrecondition, "SecureBootDrivers": connect.CodeFailedPrecondition,
		"KubernetesStatus": connect.CodeUnimplemented, "RouterStatus": connect.CodeUnimplemented,
	}
	for _, tc := range []struct {
		name    string
		mode    nodev1.Mode
		actions bool
		codes   map[string]connect.Code
	}{
		// An empty rollback is refused after authorisation.
		{"normal with actions", normal, true, with(reads, map[string]connect.Code{
			"Reboot": 0, "Shutdown": 0, "RollbackImage": connect.CodeInvalidArgument, "RollbackConfig": connect.CodeInvalidArgument})},
		{"normal without actions", normal, false, with(reads, map[string]connect.Code{
			"Reboot": connect.CodePermissionDenied, "Shutdown": connect.CodePermissionDenied,
			"RollbackImage": connect.CodePermissionDenied, "RollbackConfig": connect.CodePermissionDenied})},
		{"maintenance", maintenance, true, map[string]connect.Code{
			"Info": 0, "Disks": 0, "Logs": 0, "Diagnostics": 0, "Reboot": 0, "Shutdown": 0,
			"Status": connect.CodeFailedPrecondition, "RollbackImage": connect.CodeFailedPrecondition,
			"Contract": connect.CodePermissionDenied, "Install": connect.CodePermissionDenied, "SecureBootDrivers": connect.CodePermissionDenied}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, tc.mode, vda)
			s.ConsoleActions = tc.actions
			writeIdentity(t, s, "{}")
			write(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), "{}")
			conn := serveConsole(t, s)
			for procedure, want := range tc.codes {
				if got := call(conn, procedure); got != want {
					t.Errorf("%s: code %v, want %v", procedure, got, want)
				}
			}
		})
	}
}

// TestConsoleRunsDiagnosticsOfOperators lets the console run the diagnostics of readers and
// operators, such as the router's leases, and refuses those of admins.
func TestConsoleRunsDiagnosticsOfOperators(t *testing.T) {
	f := &fakeDiagnose{}
	s, _ := newTestServer(t, normal, vda)
	writeContract(t, s, contract.DiagnosticsFile, strings.TrimSuffix(testDiagnostics, "}")+`,
  "wipe": {"description": "Needs an admin.", "command": ["/nix/store/x-coreutils/bin/true"], "role": "admin", "timeout": "10s"}}`)
	s.Diagnose = f
	conn := serveConsole(t, s)
	address := map[string]string{"address": "192.0.2.1"}
	for _, tc := range []struct {
		name string
		args map[string]string
		want connect.Code
	}{{"route-get", address, 0}, {"ping", address, 0}, {"wipe", nil, connect.CodePermissionDenied}} {
		var got connect.Code
		if _, _, _, err := runDiagnostic(conn, tc.name, tc.args); err != nil {
			got = connect.CodeOf(err)
		}
		if got != tc.want {
			t.Errorf("%s: code %v, want %v", tc.name, got, tc.want)
		}
	}
	var ran []string
	for _, r := range f.recorded() {
		ran = append(ran, r.Name)
	}
	if !slices.Equal(ran, []string{"route-get", "ping"}) {
		t.Errorf("the console ran %v, want route-get and ping", ran)
	}
}
