package units

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/systemctl"
)

// ChronyDHCP writes the time servers DHCP announced, from networkd's state of each link below
// linksDir, to chrony's sources file, one "server <host> iburst" line each, when the node's
// TimeSync below runDir sets dhcp, and has chronyd read its sources again. Without a TimeSync, or
// with one that does not set dhcp, the file is empty. A config it cannot read takes no servers
// from DHCP either, and the error goes to stderr. A chronyd that does not take the reload, such as
// one that does not run yet, reads the file when it starts, so the reload's failure fails nothing.
func ChronyDHCP(ctx context.Context, r tool.Runner, runDir, linksDir, file string, stderr io.Writer) error {
	dhcp, err := timeSyncDHCP(runDir)
	if err != nil {
		fmt.Fprintf(stderr, "chalkd chrony-dhcp: %v; taking no time servers from DHCP\n", err)
	}
	var servers []string
	if dhcp {
		if servers, err = dhcpServers(linksDir); err != nil {
			return err
		}
	}
	var b bytes.Buffer
	for _, s := range servers {
		fmt.Fprintf(&b, "server %s iburst\n", s)
	}
	if err := atomicfile.Replace(file, b.Bytes(), 0o644); err != nil {
		return err
	}
	_, _ = tool.Run(ctx, r, "chronyc", "reload", "sources")
	return nil
}

// timeSyncDHCP tells whether the node's TimeSync below runDir sets dhcp; false without one.
func timeSyncDHCP(runDir string) (bool, error) {
	docs, err := slot.ReadRuntime(runDir)
	if err != nil {
		return false, err
	}
	d, ok := config.Documents(docs).Single(config.TimeAPI, "TimeSync")
	if !ok {
		return false, nil
	}
	var spec struct {
		DHCP bool `json:"dhcp"`
	}
	if err := d.DecodeSpec(&spec); err != nil {
		return false, fmt.Errorf("%s: %w", d, err)
	}
	return spec.DHCP, nil
}

// dhcpServers returns the time servers of the NTP= lines of networkd's state files below
// linksDir, sorted and each once; none when networkd wrote no state.
func dhcpServers(linksDir string) ([]string, error) {
	entries, err := os.ReadDir(linksDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var servers []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(linksDir, e.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			// networkd removed the link's state since the listing.
			continue
		}
		if err != nil {
			return nil, err
		}
		lines := bufio.NewScanner(bytes.NewReader(data))
		for lines.Scan() {
			if v, ok := strings.CutPrefix(lines.Text(), "NTP="); ok {
				servers = append(servers, strings.Fields(v)...)
			}
		}
	}
	slices.Sort(servers)
	return slices.Compact(servers), nil
}

// ReloadSources has chronyd read its sources again while it runs. chronyd reads them when it
// starts, so one that does not run needs no reload.
func ReloadSources(ctx context.Context, r tool.Runner) error {
	if _, err := systemctl.IsActive(ctx, r, "chronyd.service"); err != nil {
		return nil
	}
	_, err := tool.Run(ctx, r, "chronyc", "reload", "sources")
	return err
}
