package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/tool"
)

// netdev is what a .netdev unit declares of a VRF or WireGuard device: its kind, name and, for a
// VRF, its table.
type netdev struct {
	kind, name, table string
}

// readNetdev reads the [NetDev] section's Kind and Name and the [VRF] section's Table of a
// .netdev unit, the last of each that it sets, and whether it declares a VRF or WireGuard device
// by name. A line whose first character other than a space or tab is # or ; is a comment.
func readNetdev(data []byte) (netdev, bool) {
	trim := func(s string) string { return strings.Trim(s, " \t") }
	var d netdev
	section := ""
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSuffix(line, "\n")
		switch l := strings.TrimLeft(line, " \t"); {
		case strings.HasPrefix(l, "#"), strings.HasPrefix(l, ";"):
			continue
		case strings.HasPrefix(l, "["):
			section = trim(line)
			continue
		}
		// A line without = is its own key and value.
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			value = line
		}
		key, value = trim(key), trim(value)
		switch {
		case section == "[NetDev]" && key == "Kind":
			d.kind = value
		case section == "[NetDev]" && key == "Name":
			d.name = value
		case section == "[VRF]" && key == "Table":
			d.table = value
		}
	}
	return d, (d.kind == "vrf" || d.kind == "wireguard") && d.name != ""
}

// pruneDevices removes each kernel VRF that no .netdev declares with its table, after flushing
// the unreachable defaults networkd put in the table, and each WireGuard device no .netdev
// declares. networkd creates a VRF on reload but neither changes an existing VRF's table nor
// removes a VRF whose unit went, and keeps a WireGuard device whose unit went, with its keys and
// peers, which would keep the tunnel up. networkd takes a unit of a name from the first of
// NetdevDirs that holds one; an empty unit or a link to /dev/null masks those of the later ones.
func (p *Plugin) pruneDevices(ctx context.Context, stderr io.Writer) error {
	vrfs, wireguards, seen := map[string]string{}, map[string]bool{}, map[string]bool{}
	for _, dir := range p.NetdevDirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*.netdev"))
		for _, f := range files {
			if seen[filepath.Base(f)] {
				continue
			}
			seen[filepath.Base(f)] = true
			// A unit networkd cannot read declares nothing here either.
			data, _ := os.ReadFile(f)
			d, ok := readNetdev(data)
			switch {
			case !ok:
			case d.kind == "vrf":
				vrfs[d.name] = d.table
			default:
				wireguards[d.name] = true
			}
		}
	}
	links, err := p.Links()
	if err != nil {
		return err
	}
	var failed []string
	for _, l := range links {
		if l.Kind != "vrf" {
			continue
		}
		table := strconv.FormatUint(uint64(l.Table), 10)
		if want, ok := vrfs[l.Name]; ok && want == table {
			continue
		}
		// The unreachable defaults that networkd installed, of protocol static, would stay in the
		// old table, which a VRF may take later. They go first, and the VRF only once they went,
		// so that a retry finds the VRF and flushes them again.
		for _, args := range [][]string{
			{"-4", "route", "flush", "table", table, "exact", "0.0.0.0/0", "type", "unreachable", "proto", "static"},
			{"-6", "route", "flush", "table", table, "exact", "::/0", "type", "unreachable", "proto", "static"},
			{"link", "delete", l.Name},
		} {
			if _, err := tool.Run(ctx, p.run, "ip", args...); err != nil {
				failed = append(failed, l.Name)
				break
			}
		}
	}
	for _, l := range links {
		if l.Kind != "wireguard" || wireguards[l.Name] {
			continue
		}
		if _, err := tool.Run(ctx, p.run, "ip", "link", "delete", l.Name); err != nil {
			failed = append(failed, l.Name)
		}
	}
	for _, name := range failed {
		fmt.Fprintf(stderr, "chalkd router prune-devices: failed to remove the device %s\n", name)
	}
	if len(failed) > 0 {
		return cli.Status(1)
	}
	return nil
}

// recordDevice keeps the index of the device of the VRF vrf in <runtimeDir>/<service>-<vrf>.ifindex,
// where chalkd router services and chalkd's status read it, and fails while the VRF has no
// device, so that the VRF's instance of a per-VRF service does not start with its sockets bound
// to none.
func (p *Plugin) recordDevice(service, vrf, runtimeDir string) error {
	if runtimeDir == "" {
		return errors.New("RUNTIME_DIRECTORY is not set; run it as a unit's ExecStartPre= with a RuntimeDirectory=")
	}
	data, err := os.ReadFile(filepath.Join(p.NetDir, vrf, "ifindex"))
	if err != nil {
		return err
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return os.WriteFile(filepath.Join(runtimeDir, service+"-"+vrf+".ifindex"), []byte(strings.TrimSpace(line)+"\n"), 0o666)
}

// notReady is the exit status of a readiness probe that asks to be run again.
const notReady = 75

// resolverVRFsReady succeeds when the VRF of each rendered resolver of a VRF exists with the table
// its rendered .netdev gives, and fails with status 75 while one does not.
func (p *Plugin) resolverVRFsReady() error {
	configs, _ := filepath.Glob(filepath.Join(p.ResolverDir, "unbound-*.conf"))
	if len(configs) == 0 {
		return nil
	}
	links, err := p.Links()
	if err != nil {
		return err
	}
	for _, c := range configs {
		vrf := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(c), "unbound-"), ".conf")
		f := filepath.Join(p.NetworkdDir, "00-vrf-"+vrf+".netdev")
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		table := netdevTable(data)
		if table == "" {
			return fmt.Errorf("%s gives the VRF %s no table", f, vrf)
		}
		ready := false
		for _, l := range links {
			ready = ready || (l.Name == vrf && l.Kind == "vrf" && strconv.FormatUint(uint64(l.Table), 10) == table)
		}
		if !ready {
			return cli.Status(notReady)
		}
	}
	return nil
}

// netdevTable is the value of each line Table=<value> of a VRF's .netdev as chalkos-render writes
// it, a line each.
func netdevTable(data []byte) string {
	var tables []string
	for line := range strings.Lines(string(data)) {
		if v, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "Table="); ok {
			v, _, _ = strings.Cut(v, "=")
			tables = append(tables, v)
		}
	}
	return strings.Join(tables, "\n")
}
