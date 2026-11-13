package console

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// journal follows the journal of the boot, or of unit, as chalkd's Logs does, for the time chalkd
// does not answer.
func journal(ctx context.Context, unit string, line func(string)) error {
	args := []string{"--boot", "--follow", "--no-pager", "--output=short-iso", fmt.Sprintf("--lines=%d", replayLines)}
	if unit != "" {
		args = append(args, "--unit="+unit)
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line(sc.Text())
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// activeUnits lists the active services, which the logs' filter offers.
func activeUnits(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "list-units", "--type=service", "--state=active", "--plain", "--no-legend").Output()
	if err != nil {
		return nil, err
	}
	var units []string
	for line := range strings.Lines(string(out)) {
		if f := strings.Fields(line); len(f) > 0 {
			units = append(units, f[0])
		}
	}
	return units, nil
}

// uptime is how long the machine has run, from /proc/uptime.
func uptime() (time.Duration, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0, fmt.Errorf("/proc/uptime holds %q", data)
	}
	s, err := strconv.ParseFloat(f[0], 64)
	return time.Duration(s * float64(time.Second)), err
}

// resolvedServers is where systemd-resolved lists the DNS servers it asks.
const resolvedServers = "/run/systemd/resolve/resolv.conf"

// dnsServers lists the DNS servers the node asks, as systemd-resolved lists them.
func dnsServers() ([]string, error) {
	data, err := os.ReadFile(resolvedServers)
	if err != nil {
		return nil, err
	}
	var servers []string
	for line := range strings.Lines(string(data)) {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "nameserver" {
			servers = append(servers, f[1])
		}
	}
	return servers, nil
}

// askPassword is where systemd's passphrase prompts wait for an answer, each as an ask.* file.
const askPassword = "/run/systemd/ask-password"

// passphrasePrompt reports whether a passphrase prompt waits for an answer.
func passphrasePrompt() bool {
	asks, _ := filepath.Glob(filepath.Join(askPassword, "ask.*"))
	return len(asks) > 0
}
