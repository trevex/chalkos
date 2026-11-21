package router

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// ntp-clients prints chronyc's clients, an empty line and its server statistics, also when the
// first fails, and exits as the second does.
func TestNTPClients(t *testing.T) {
	p, r := routerNode(t)
	r.Rules = append(r.Rules,
		tooltest.Rule{Prefix: "/bin/chronyc -h 127.0.0.1,::1 -n clients", Out: "Hostname NTP\n", Err: &tool.Error{Code: 1}},
		tooltest.Rule{Prefix: "/bin/chronyc -h 127.0.0.1,::1 serverstats", Out: "NTP packets received : 3\n", Err: &tool.Error{Code: 2}},
	)
	var out bytes.Buffer
	if err := p.ntpClients(context.Background(), "/bin/chronyc", &out); err != cli.Status(2) {
		t.Errorf("error %v, want status 2", err)
	}
	if got := out.String(); got != "Hostname NTP\n\nNTP packets received : 3\n" {
		t.Errorf("printed %q", got)
	}
}

// nat64-sessions prints the sessions of each protocol and stops at the first jool fails, with its
// status.
func TestNAT64Sessions(t *testing.T) {
	p, r := routerNode(t)
	r.Rules = append(r.Rules,
		tooltest.Rule{Prefix: "jool -i edge session display --numeric --tcp", Out: "TCP\n"},
		tooltest.Rule{Prefix: "jool -i edge session display --numeric --udp", Out: "UDP\n", Err: &tool.Error{Code: 3}},
	)
	var out bytes.Buffer
	if err := p.nat64Sessions(context.Background(), "jool", "edge", &out); err != cli.Status(3) {
		t.Errorf("error %v, want status 3", err)
	}
	if got := out.String(); got != "TCP\nUDP\n" {
		t.Errorf("printed %q", got)
	}
	if len(r.Calls) != 2 {
		t.Errorf("calls = %q", r.Calls)
	}
}

// kea serves Kea's control socket at path, answering each command from answers by its name; a
// command without an answer is answered with nothing.
func kea(t *testing.T, path string, answers map[string]string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				data, _ := io.ReadAll(conn)
				for command, answer := range answers {
					if strings.Contains(string(data), `"`+command+`"`) {
						io.WriteString(conn, answer)
					}
				}
			}()
		}
	}()
}

// dhcp-leases lists the leases of each server, by VRF, family and socket name, filtered by subnet
// and MAC address, and names the servers that do not answer or answer without subnets.
func TestDHCPLeases(t *testing.T) {
	dir, err := os.MkdirTemp("", "kea")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	kea(t, filepath.Join(dir, "dhcp4.sock"), map[string]string{
		"config-get": `{"result": 0, "arguments": {"Dhcp4": {"subnet4": [{"id": 1, "user-context": {"name": "office"}}, {"id": 2}]}}}`,
		"lease4-get-all": `{"result": 0, "arguments": {"leases": [
			{"ip-address": "192.0.2.10", "hw-address": "52:54:00:12:34:56", "hostname": "printer", "subnet-id": 1, "cltt": 1800000000, "valid-lft": 3600},
			{"ip-address": "192.0.2.11", "hw-address": "", "hostname": "a\tb", "subnet-id": 2, "cltt": 1800000000, "valid-lft": 60},
			{"ip-address": "192.0.2.12", "subnet-id": 9, "cltt": 1800000000, "valid-lft": 60}]}}`,
	})
	kea(t, filepath.Join(dir, "dhcp6-access.sock"), map[string]string{
		"config-get":     `{"arguments": {"Dhcp6": {"subnet6": [{"id": 5, "user-context": {"name": "customers"}}]}}}`,
		"lease6-get-all": `{"arguments": {"leases": [{"type": "IA_PD", "ip-address": "2001:db8:100::", "prefix-len": 56, "subnet-id": 5, "cltt": 1800000000, "valid-lft": 600}]}}`,
	})
	kea(t, filepath.Join(dir, "dhcp4-ghost.sock"), nil)
	kea(t, filepath.Join(dir, "dhcp6.sock"), map[string]string{"config-get": `{"arguments": {}}`, "lease6-get-all": `{"arguments": {"leases": []}}`})
	touch(t, filepath.Join(dir, "dhcp4-file.sock"), "")

	list := func(subnet, mac string, deadline time.Time) (string, string) {
		var out, errs bytes.Buffer
		if err := dhcpLeases(dir, subnet, mac, time.Second, deadline, &out, &errs); err != nil {
			t.Fatal(err)
		}
		return out.String(), errs.String()
	}
	later := time.Now().Add(time.Minute)
	out, errs := list("", "", later)
	want := "VRF\tSUBNET\tADDRESS\tMAC\tHOSTNAME\tENDS\n" +
		"default\toffice\t192.0.2.10\t52:54:00:12:34:56\tprinter\t2027-01-15T09:00:00Z\n" +
		"default\t-\t192.0.2.11\t-\ta\\tb\t2027-01-15T08:01:00Z\n" +
		"default\t-\t192.0.2.12\t-\t-\t2027-01-15T08:01:00Z\n" +
		"access\tcustomers\t2001:db8:100::/56\t-\t-\t2027-01-15T08:10:00Z\n"
	if out != want {
		t.Errorf("printed\n%s\nwant\n%s", out, want)
	}
	if wantErrs := "the DHCPv4 server of VRF ghost does not answer\nthe DHCPv6 server of VRF default answered without its subnets or leases\n"; errs != wantErrs {
		t.Errorf("said %q, want %q", errs, wantErrs)
	}
	if out, _ := list("office", "52:54:00:12:34:56", later); !strings.HasSuffix(out, "ENDS\ndefault\toffice\t192.0.2.10\t52:54:00:12:34:56\tprinter\t2027-01-15T09:00:00Z\n") {
		t.Errorf("printed %q for the printer", out)
	}
	if out, _ := list("guests", "", later); strings.Count(out, "\n") != 1 {
		t.Errorf("printed %q for a subnet without leases", out)
	}
	out, errs = list("", "", time.Now())
	if strings.Count(out, "\n") != 1 || !slices.Contains(strings.Split(errs, "\n"), "the DHCPv6 server of VRF access was not asked: the others took too long") {
		t.Errorf("past the deadline printed %q, said %q", out, errs)
	}
}
