package units

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/internal/testwait"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// apply-links replays the add events of the devices with a bus parent alone, after udev read its
// rules again.
func TestApplyLinks(t *testing.T) {
	sys := t.TempDir()
	writeFile(t, filepath.Join(sys, "enp1s0", "device", "vendor"), "0x1af4\n")
	writeFile(t, filepath.Join(sys, "enp2s0", "device", "vendor"), "0x1af4\n")
	writeFile(t, filepath.Join(sys, "lo", "type"), "772\n")
	writeFile(t, filepath.Join(sys, "flannel.1", "type"), "1\n")
	r := &tooltest.Runner{}
	if err := ApplyLinks(context.Background(), r, sys); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"udevadm control --reload",
		"udevadm trigger --action=add --settle " + filepath.Join(sys, "enp1s0") + " " + filepath.Join(sys, "enp2s0"),
	}
	if !slices.Equal(r.Calls, want) {
		t.Errorf("calls = %q, want %q", r.Calls, want)
	}
}

// Without a physical device, apply-links replays no event: udevadm trigger with no device would
// replay every device's.
func TestApplyLinksWithoutDevices(t *testing.T) {
	sys := t.TempDir()
	writeFile(t, filepath.Join(sys, "lo", "type"), "772\n")
	r := &tooltest.Runner{}
	if err := ApplyLinks(context.Background(), r, sys); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Calls, []string{"udevadm control --reload"}) {
		t.Errorf("calls = %q", r.Calls)
	}
}

// A udev that does not read its rules fails the unit before any event.
func TestApplyLinksFails(t *testing.T) {
	r := &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "udevadm control", Err: errors.New("no udevd")}}}
	if err := ApplyLinks(context.Background(), r, t.TempDir()); err == nil {
		t.Fatal("a failed reload did not fail")
	}
	if len(r.Calls) != 1 {
		t.Errorf("calls = %q", r.Calls)
	}
}

// chronyNode is a node's /run/chalkos and networkd's link state with two links that announced
// time servers, and the sources file chrony-dhcp writes.
func chronyNode(t *testing.T) (runDir, links, sources string) {
	t.Helper()
	root := t.TempDir()
	runDir = filepath.Join(root, "run", "chalkos")
	links = filepath.Join(root, "netif", "links")
	writeFile(t, filepath.Join(links, "2"), "ADMIN_STATE=configured\nNTP=10.0.0.1 ntp.example\n")
	writeFile(t, filepath.Join(links, "3"), "NTP=10.0.0.1 10.0.0.2\n")
	return runDir, links, filepath.Join(root, "chrony-dhcp", "dhcp.sources")
}

// timeSync writes the node's TimeSync below runDir, as chalkd writes it.
func timeSync(t *testing.T, runDir, spec string) {
	t.Helper()
	writeFile(t, filepath.Join(runDir, "config", "time.chalkos.dev", "TimeSync", "timesync.json"),
		`{"apiVersion": "time.chalkos.dev/v1alpha1", "kind": "TimeSync", "metadata": {"name": "timesync"}, "spec": `+spec+`}`)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The servers DHCP announced are chrony's, each once, when the TimeSync takes them, and chrony
// reads its sources again.
func TestChronyDHCP(t *testing.T) {
	runDir, links, sources := chronyNode(t)
	timeSync(t, runDir, `{"servers": [], "dhcp": true}`)
	r := &tooltest.Runner{}
	var stderr bytes.Buffer
	if err := ChronyDHCP(context.Background(), r, runDir, links, sources, &stderr); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, sources), "server 10.0.0.1 iburst\nserver 10.0.0.2 iburst\nserver ntp.example iburst\n"; got != want {
		t.Errorf("sources = %q, want %q", got, want)
	}
	if !slices.Equal(r.Calls, []string{"chronyc reload sources"}) {
		t.Errorf("calls = %q", r.Calls)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q", stderr.String())
	}
	if left, _ := os.ReadDir(filepath.Dir(sources)); len(left) != 1 {
		t.Errorf("files left beside the sources: %v", left)
	}
}

// A TimeSync that does not take them, no TimeSync, or one that cannot be read leaves chrony no
// servers from DHCP; the last says why.
func TestChronyDHCPWithoutServers(t *testing.T) {
	for name, c := range map[string]struct {
		spec, file string
		says       bool
	}{
		"dhcp false":  {spec: `{"servers": [], "dhcp": false}`},
		"dhcp unset":  {spec: `{"servers": []}`},
		"no TimeSync": {},
		"unreadable":  {file: "{", says: true},
	} {
		t.Run(name, func(t *testing.T) {
			runDir, links, sources := chronyNode(t)
			writeFile(t, sources, "server 10.0.0.9 iburst\n")
			switch {
			case c.spec != "":
				timeSync(t, runDir, c.spec)
			case c.file != "":
				writeFile(t, filepath.Join(runDir, "config", "time.chalkos.dev", "TimeSync", "timesync.json"), c.file)
			}
			r := &tooltest.Runner{}
			var stderr bytes.Buffer
			if err := ChronyDHCP(context.Background(), r, runDir, links, sources, &stderr); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, sources); got != "" {
				t.Errorf("sources = %q, want none", got)
			}
			if said := strings.Contains(stderr.String(), "timesync"); said != c.says {
				t.Errorf("stderr = %q", stderr.String())
			}
			if !slices.Equal(r.Calls, []string{"chronyc reload sources"}) {
				t.Errorf("calls = %q", r.Calls)
			}
		})
	}
}

// A chronyd that does not take the reload, such as one that does not run yet, fails nothing: it
// reads the sources when it starts.
func TestChronyDHCPReloadFails(t *testing.T) {
	runDir, links, sources := chronyNode(t)
	r := &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "chronyc", Err: errors.New("506 Cannot talk to daemon")}}}
	if err := ChronyDHCP(context.Background(), r, runDir, links, sources, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

// chrony's effect reloads the sources of a chronyd that runs alone.
func TestReloadSources(t *testing.T) {
	r := &tooltest.Runner{}
	if err := ReloadSources(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Calls, []string{"systemctl is-active chronyd.service", "chronyc reload sources"}) {
		t.Errorf("calls = %q", r.Calls)
	}

	r = &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "systemctl is-active", Out: "inactive\n", Err: &tool.Error{Code: 3}}}}
	if err := ReloadSources(context.Background(), r); err != nil {
		t.Fatalf("the effect failed for a chronyd that does not run: %v", err)
	}
	if len(r.Calls) != 1 {
		t.Errorf("reloaded the sources of a chronyd that does not run: %q", r.Calls)
	}

	r = &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "chronyc", Err: errors.New("506 Cannot talk to daemon")}}}
	if err := ReloadSources(context.Background(), r); err == nil {
		t.Error("a reload a running chronyd refused did not fail")
	}
}

// tcpConnect runs TCPConnect with dial and returns its stdout, stderr and error.
func tcpConnect(dial dialer, address, port string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	err := TCPConnect(context.Background(), dial, address, port, connectTimeout, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// tcp-connect says it connected to a port that listens, and why it did not to one that refuses.
func TestTCPConnect(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	stdout, stderr, err := tcpConnect(dial, "127.0.0.1", port)
	if err != nil || stdout != "connected to 127.0.0.1 port "+port+"\n" || stderr != "" {
		t.Errorf("connect to a listening port: %v, %q, %q", err, stdout, stderr)
	}
	l.Close()

	stdout, stderr, err = tcpConnect(dial, "127.0.0.1", port)
	if err != cli.Status(1) || stdout != "" || stderr != "no connection to 127.0.0.1 port "+port+": connection refused\n" {
		t.Errorf("connect to a closed port: %v, %q, %q", err, stdout, stderr)
	}
}

// An IPv6 address goes in brackets before the port.
func TestTCPConnectIPv6(t *testing.T) {
	var got string
	refuse := func(_ context.Context, _, address string) (net.Conn, error) {
		got = address
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	}
	if _, _, err := tcpConnect(refuse, "2001:db8::1", "443"); err != cli.Status(1) || got != "[2001:db8::1]:443" {
		t.Errorf("dialled %q: %v", got, err)
	}
}

// A connect that gets no answer within the timeout exits with 124, as coreutils' timeout does.
func TestTCPConnectTimeout(t *testing.T) {
	testwait.Bubble(t, func(t *testing.T) {
		silent := func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		start := time.Now()
		stdout, stderr, err := tcpConnect(silent, "192.0.2.10", "443")
		if err != cli.Status(124) || stdout != "" || stderr != "no connection to 192.0.2.10 port 443 within 5 seconds\n" {
			t.Errorf("connect without an answer: %v, %q, %q", err, stdout, stderr)
		}
		if waited := time.Since(start); waited != connectTimeout {
			t.Errorf("waited %v, want %v", waited, connectTimeout)
		}
	})
}

// frr-credentials fills the rendered config in, and FRR's fallback while there is none.
func TestFRRCredentials(t *testing.T) {
	dir := t.TempDir()
	rendered, fallback, out := filepath.Join(dir, "rendered.conf"), filepath.Join(dir, "fallback.conf"), filepath.Join(dir, "frr.conf")
	writeFile(t, fallback, "frr defaults traditional\n")
	if err := FRRCredentials(rendered, fallback, out, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); string(got) != "frr defaults traditional\n" {
		t.Errorf("without a rendered config, frr.conf = %q, want the fallback", got)
	}
	writeFile(t, rendered, "router bgp 65001\n")
	if err := FRRCredentials(rendered, fallback, out, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); string(got) != "router bgp 65001\n" {
		t.Errorf("with a rendered config, frr.conf = %q, want the rendered one", got)
	}
}
