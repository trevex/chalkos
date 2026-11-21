package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/tool"
)

// statusOf is the error of a tool's failure as the command's own exit status: the tool said why
// on standard error already.
func statusOf(err error) error {
	if te, ok := errors.AsType[*tool.Error](err); ok {
		return cli.Status(te.Code)
	}
	return err
}

// ntpClients prints the NTP clients chronyd answered, as chronyc -n clients prints them, an empty
// line, and its counts of requests, as chronyc serverstats prints them, asking the chronyd of the
// node's loopback addresses with the chronyc of path chronyc. A failure of the first does not stop
// the second, whose exit status the command takes.
func (p *Plugin) ntpClients(ctx context.Context, chronyc string, stdout io.Writer) error {
	out, _ := tool.Run(ctx, p.run, chronyc, "-h", "127.0.0.1,::1", "-n", "clients")
	if _, err := stdout.Write(append(out, '\n')); err != nil {
		return err
	}
	out, err := tool.Run(ctx, p.run, chronyc, "-h", "127.0.0.1,::1", "serverstats")
	if _, werr := stdout.Write(out); werr != nil {
		return werr
	}
	return statusOf(err)
}

// nat64Sessions prints the TCP, UDP and ICMP sessions of the NAT64 instance, as jool session
// display prints them, with the jool of path jool, which asks the network namespace it runs in.
// It stops at the first protocol jool fails, with jool's exit status.
func (p *Plugin) nat64Sessions(ctx context.Context, jool, instance string, stdout io.Writer) error {
	for _, protocol := range []string{"--tcp", "--udp", "--icmp"} {
		out, err := tool.Run(ctx, p.run, jool, "-i", instance, "session", "display", "--numeric", protocol)
		if _, werr := stdout.Write(out); werr != nil {
			return werr
		}
		if err != nil {
			return statusOf(err)
		}
	}
	return nil
}

// keaAskTimeout bounds each question to a DHCP server, and leasesBudget the questions to all of
// them, so that dhcp-leases ends within its diagnostic's timeout.
const (
	keaAskTimeout = 2 * time.Second
	leasesBudget  = 24 * time.Second
)

// dhcpLeases prints the leases of the DHCP servers of every VRF, a line each of tab-separated
// fields after a header: the VRF, subnet, address or delegated prefix, MAC address, host name and
// end of each, of the subnet subnet and MAC address mac, or all when empty. It asks each server
// on its control socket in keaDir for its subnets and leases. A server that does not answer a
// question with JSON within askTimeout, or whose answers hold no subnets and leases, is named on
// stderr, and the others still listed. Servers still unasked at the deadline are named as well.
func dhcpLeases(keaDir, subnet, mac string, askTimeout time.Duration, deadline time.Time, stdout, stderr io.Writer) error {
	if _, err := io.WriteString(stdout, "VRF\tSUBNET\tADDRESS\tMAC\tHOSTNAME\tENDS\n"); err != nil {
		return err
	}
	var sockets []string
	for _, family := range []string{"4", "6"} {
		sockets = append(sockets, filepath.Join(keaDir, "dhcp"+family+".sock"))
		vrfs, _ := filepath.Glob(filepath.Join(keaDir, "dhcp"+family+"-*.sock"))
		sockets = append(sockets, vrfs...)
	}
	for _, socket := range sockets {
		if !isSocket(socket) {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(socket), ".sock")
		family := name[len("dhcp") : len("dhcp")+1]
		vrf := strings.TrimPrefix(name[len("dhcp")+1:], "-")
		if vrf == "" {
			vrf = "default"
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(stderr, "the DHCPv%s server of VRF %s was not asked: the others took too long\n", family, vrf)
			continue
		}
		config, err := askKea(socket, `{"command": "config-get"}`, askTimeout)
		var leases map[string]any
		if err == nil {
			leases, err = askKea(socket, `{"command": "lease`+family+`-get-all"}`, askTimeout)
		}
		if err != nil {
			fmt.Fprintf(stderr, "the DHCPv%s server of VRF %s does not answer\n", family, vrf)
			continue
		}
		if err := printLeases(stdout, vrf, config, leases, subnet, mac); err != nil {
			fmt.Fprintf(stderr, "the DHCPv%s server of VRF %s answered without its subnets or leases\n", family, vrf)
		}
	}
	return nil
}

// isSocket reports whether path is a socket.
func isSocket(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().Type() == fs.ModeSocket
}

// askKea sends the command to the Kea server of the control socket and returns its answer, a JSON
// object. It ends its side of the connection after the command and reads the answer for timeout
// at most.
func askKea(socket, command string, timeout time.Duration) (map[string]any, error) {
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(conn, command); err != nil {
		return nil, err
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(conn)
	if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		return nil, err
	}
	var answer map[string]any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&answer); err != nil || answer == nil {
		return nil, errors.New("no JSON object")
	}
	return answer, nil
}

// printLeases prints the leases of the server of vrf, of the subnet subnet and MAC address mac or
// all when empty, from its answers to config-get and lease<family>-get-all. It fails when the
// answers hold no subnets or a lease lacks its time, after printing the leases before it.
func printLeases(w io.Writer, vrf string, config, leases map[string]any, subnet, mac string) error {
	args, _ := config["arguments"].(map[string]any)
	var subnets any
	for _, server := range []string{"Dhcp4", "Dhcp6"} {
		if s, _ := args[server].(map[string]any); s != nil {
			subnets = s["subnet"+server[len(server)-1:]]
			if subnets != nil {
				break
			}
		}
	}
	list, ok := subnets.([]any)
	if !ok {
		return errors.New("no subnets")
	}
	names := map[string]any{}
	for _, s := range list {
		m, _ := s.(map[string]any)
		if m == nil {
			return errors.New("a subnet that is no object")
		}
		var name any
		if uc, _ := m["user-context"].(map[string]any); uc != nil {
			name = uc["name"]
		}
		names[text(m["id"])] = name
	}
	largs, _ := leases["arguments"].(map[string]any)
	all, _ := largs["leases"].([]any)
	for _, l := range all {
		lease, _ := l.(map[string]any)
		if lease == nil {
			return errors.New("a lease that is no object")
		}
		name := "-"
		if n, ok := names[text(lease["subnet-id"])]; ok && n != nil {
			name = text(n)
		}
		if (subnet != "" && name != subnet) || (mac != "" && lease["hw-address"] != mac) {
			continue
		}
		address := text(lease["ip-address"])
		if lease["type"] == "IA_PD" {
			address += "/" + text(lease["prefix-len"])
		}
		cltt, err1 := number(lease["cltt"])
		valid, err2 := number(lease["valid-lft"])
		if err1 != nil || err2 != nil {
			return errors.New("a lease without its time")
		}
		fields := []string{vrf, name, address, orDash(lease["hw-address"]), orDash(lease["hostname"]), time.Unix(cltt+valid, 0).UTC().Format("2006-01-02T15:04:05Z")}
		for i, f := range fields {
			fields[i] = tsvEscaper.Replace(f)
		}
		if _, err := fmt.Fprintln(w, strings.Join(fields, "\t")); err != nil {
			return err
		}
	}
	return nil
}

// tsvEscaper escapes a field as jq's @tsv does.
var tsvEscaper = strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\n", "\\n", "\r", "\\r")

// text is a JSON value as jq's string interpolation gives it: a string as it is, null as null,
// and other values as JSON.
func text(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case nil:
		return "null"
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// orDash is a lease's field, or - when it is missing or empty.
func orDash(v any) string {
	if v == nil || v == "" {
		return "-"
	}
	return text(v)
}

// number is a JSON number as a whole number of seconds.
func number(v any) (int64, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, errors.New("no number")
	}
	if i, err := n.Int64(); err == nil {
		return i, nil
	}
	f, err := n.Float64()
	return int64(f), err
}
