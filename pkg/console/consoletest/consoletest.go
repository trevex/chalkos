// Package consoletest is the console's test node: a fake Client, the nodes the tests show, and
// helpers that run the console on a terminal of 80 columns and 25 lines and compare its screen.
package consoletest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"
	"google.golang.org/protobuf/types/known/timestamppb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/console"
)

// Client is a node for the console's tests. A test sets its fields before the console starts,
// and changes them while it runs through Set.
type Client struct {
	mu sync.Mutex

	InfoResp   *nodev1.InfoResponse
	InfoErr    error
	StatusResp *nodev1.StatusResponse
	StatusErr  error
	DisksResp  *nodev1.DisksResponse
	DisksErr   error
	// Diagnostics are the outputs of diagnostics by Key(name, args); a diagnostic without one
	// fails NotFound, as one the image lacks.
	Diagnostics map[string]string
	// DiagnosticErrs fail diagnostics by Key(name, args).
	DiagnosticErrs map[string]error
	// Lines are the journal Logs streams; LogsErr fails it.
	Lines   []string
	LogsErr error
	// ActionErr fails the actions. ActionBlock, when set, holds each action until it is closed.
	ActionErr   error
	ActionBlock chan struct{}

	calls []string
	// streams counts the Logs streams that run.
	streams int
}

var _ console.Client = (*Client)(nil)

// Key names a diagnostic's run: its name, then its arguments as key=value in key order.
func Key(name string, args map[string]string) string {
	k := name
	for _, a := range slices.Sorted(maps.Keys(args)) {
		k += " " + a + "=" + args[a]
	}
	return k
}

// Set changes c while the console runs.
func (c *Client) Set(f func(c *Client)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c)
}

// Calls are the actions called so far, such as "Reboot" or "RollbackConfig v1".
func (c *Client) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

func (c *Client) Info(context.Context) (*nodev1.InfoResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.InfoResp, c.InfoErr
}

func (c *Client) Status(context.Context) (*nodev1.StatusResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.StatusResp, c.StatusErr
}

func (c *Client) Disks(context.Context) (*nodev1.DisksResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.DisksResp, c.DisksErr
}

func (c *Client) Diagnostic(_ context.Context, name string, args map[string]string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := Key(name, args)
	if err := c.DiagnosticErrs[k]; err != nil {
		return nil, err
	}
	out, ok := c.Diagnostics[k]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("the image declares no diagnostic %q", name))
	}
	return []byte(out), nil
}

// Logs streams Lines, filtered to those that name unit, then waits for ctx.
func (c *Client) Logs(ctx context.Context, unit string, line func(string)) error {
	c.mu.Lock()
	lines, err := slices.Clone(c.Lines), c.LogsErr
	c.mu.Unlock()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.streams++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.streams--
		c.mu.Unlock()
	}()
	for _, l := range lines {
		if unit == "" || strings.Contains(l, " "+strings.TrimSuffix(unit, ".service")+"[") {
			line(l)
		}
	}
	<-ctx.Done()
	return nil
}

// Streams is how many Logs streams run.
func (c *Client) Streams() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams
}

func (c *Client) act(call string) error {
	c.mu.Lock()
	c.calls = append(c.calls, call)
	block, err := c.ActionBlock, c.ActionErr
	c.mu.Unlock()
	if block != nil {
		<-block
	}
	return err
}

func (c *Client) Reboot(context.Context) error   { return c.act("Reboot") }
func (c *Client) Shutdown(context.Context) error { return c.act("Shutdown") }

func (c *Client) RollbackImage(_ context.Context, root []byte) (*nodev1.RollbackImageResponse, error) {
	if err := c.act(fmt.Sprintf("RollbackImage %x", root)); err != nil {
		return nil, err
	}
	return &nodev1.RollbackImageResponse{Rebooting: true}, nil
}

func (c *Client) RollbackConfig(_ context.Context, version string) (*nodev1.RollbackConfigResponse, error) {
	if err := c.act("RollbackConfig " + version); err != nil {
		return nil, err
	}
	return &nodev1.RollbackConfigResponse{RestartedUnits: []string{"chrony.service"}}, nil
}

// hash is a root hash of 32 bytes, the two bytes of b repeated.
func hash(b0, b1 byte) []byte { return bytes.Repeat([]byte{b0, b1}, 16) }

const gib = 1 << 30

// The disks of the fixtures' nodes.
func disks(usage bool) *nodev1.DisksResponse {
	sys := &nodev1.Disk{
		Name: "vda", Device: "/dev/vda", Model: "QEMU HARDDISK", Size: 16 * gib, Serial: "chalk-sys", Type: "hdd",
		Usage: "boot",
		Partitions: []*nodev1.Partition{
			{Device: "/dev/vda1", Size: 512 << 20, Label: "esp", Content: "vfat"},
			{Device: "/dev/vda2", Size: 2 * gib, Label: "store-a", Content: "erofs"},
			{Device: "/dev/vda3", Size: 2 * gib, Label: "store-b"},
			{Device: "/dev/vda4", Size: 256 << 20, Label: "state", Content: "crypto_LUKS"},
			{Device: "/dev/vda5", Size: 10 * gib, Label: "var", Content: "crypto_LUKS"},
		},
	}
	data := &nodev1.Disk{Name: "vdb", Device: "/dev/vdb", Model: "QEMU HARDDISK", Size: 8 * gib, Serial: "chalk-data", Type: "hdd"}
	if usage {
		data.Usage = "disk data"
	}
	return &nodev1.DisksResponse{Disks: []*nodev1.Disk{sys, data}}
}

// addresses is the output of the addresses diagnostic: lo, enp1s0 with an IPv4, a global IPv6
// and a link-local address, and enp2s0 down without addresses.
const addresses = `[{"ifindex":1,"ifname":"lo","flags":["LOOPBACK","UP","LOWER_UP"],"mtu":65536,"operstate":"UNKNOWN","address":"00:00:00:00:00:00","addr_info":[{"family":"inet","local":"127.0.0.1","prefixlen":8,"scope":"host"},{"family":"inet6","local":"::1","prefixlen":128,"scope":"host"}]},` +
	`{"ifindex":2,"ifname":"enp1s0","flags":["BROADCAST","MULTICAST","UP","LOWER_UP"],"mtu":1500,"operstate":"UP","address":"52:54:00:12:34:56","addr_info":[{"family":"inet","local":"192.168.1.50","prefixlen":24,"scope":"global"},{"family":"inet6","local":"2001:db8::50","prefixlen":64,"scope":"global"},{"family":"inet6","local":"fe80::5054:ff:fe12:3456","prefixlen":64,"scope":"link"}]},` +
	`{"ifindex":3,"ifname":"enp2s0","flags":["BROADCAST","MULTICAST"],"mtu":1500,"operstate":"DOWN","address":"52:54:00:ab:cd:ef","addr_info":[]}]`

// Lines is the journal of the fixtures' nodes, with one error.
var Lines = []string{
	"2026-11-13T11:52:01+00:00 node1 systemd[1]: Started chalkd.service.",
	"2026-11-13T11:52:02+00:00 node1 chronyd[580]: chronyd version 4.6 starting (+CMDMON +NTP +REFCLOCK +RTC)",
	"2026-11-13T11:52:02+00:00 node1 chalkd[612]: serving the node API on :50000",
	"2026-11-13T11:52:08+00:00 node1 chronyd[580]: Selected source 192.168.1.1",
	"2026-11-13T11:52:09+00:00 node1 systemd[1]: Reached target Multi-User System.",
	"2026-11-13T11:53:15+00:00 node1 chalkd[612]: apply: output networkd failed: render networkd: exit status 1: unknown link enp9s0 in 10-uplink.network",
	"2026-11-13T11:54:30+00:00 node1 chronyd[580]: System clock wrong by 0.000123 seconds",
	"2026-11-13T11:55:00+00:00 node1 chalkd[612]: config 3fa9c1e2b7d0 delivered",
}

// Normal is an installed node, healthy, with a previous image and config.
func Normal() *Client {
	expiry := timestamppb.New(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	return &Client{
		InfoResp: &nodev1.InfoResponse{
			Mode: nodev1.Mode_MODE_NORMAL, Hostname: "node1", NodeName: "n1", Variant: "worker", Platform: "metal",
			RootHash: hash(0x81, 0xbd), Fingerprint: strings.Repeat("ab", 32),
		},
		StatusResp: &nodev1.StatusResponse{
			ConfigVersion:         "3fa9c1e2b7d0" + strings.Repeat("4e5f", 13),
			PreviousConfigVersion: "1b2c3d4e5f60" + strings.Repeat("7a8b", 13),
			Platform:              "metal",
			Boot: &nodev1.BootStatus{
				Running:  &nodev1.BootImage{RootHash: hash(0x81, 0xbd), Generation: 7, Tag: "v1.4.0"},
				Entry:    "chalkos_7.efi",
				Blessed:  true,
				Previous: &nodev1.BootImage{RootHash: hash(0x3f, 0xa9), Generation: 6, Tag: "v1.3.0"},
			},
			Volumes: []*nodev1.VolumeStatus{
				{Name: "var", Disk: "system", MountPoint: "/var", Present: true, Mounted: true, Encryption: "tpm2", SizeBytes: 98 * gib / 10, UsedBytes: 12 * gib / 10},
				{Name: "data", Disk: "data", MountPoint: "/srv/data", Present: true, Encryption: "none"},
			},
			Outputs: []*nodev1.OutputStatus{
				{Name: "chrony", Hash: strings.Repeat("c1", 32)},
				{Name: "hosts", Hash: strings.Repeat("d2", 32)},
				{Name: "networkd", Hash: strings.Repeat("e3", 32)},
				{Name: "nftables", Hash: strings.Repeat("f4", 32)},
				{Name: "resolved", Hash: strings.Repeat("a5", 32)},
				{Name: "sysctl", Hash: strings.Repeat("b6", 32)},
			},
			Certificates: []*nodev1.CertificateStatus{
				{Name: "node", NotAfter: expiry},
				{Name: "OS CA", NotAfter: expiry},
			},
			Trust: []*nodev1.TrustStatus{{Name: "OS CA", Fingerprints: []string{strings.Repeat("cd", 32)}, Issuing: strings.Repeat("cd", 32)}},
			Time:  &nodev1.TimeStatus{Synchronised: true, Source: "192.168.1.1", OffsetSeconds: 0.000123},
		},
		DisksResp: disks(true),
		Diagnostics: map[string]string{
			"addresses": addresses,
			Key("routes", map[string]string{"family": "inet", "table": "main"}): `[{"dst":"default","gateway":"192.168.1.1","dev":"enp1s0","protocol":"dhcp","metric":1024},` +
				`{"dst":"192.168.1.0/24","dev":"enp1s0","protocol":"kernel","prefsrc":"192.168.1.50"}]`,
			Key("routes", map[string]string{"family": "inet6", "table": "main"}): `[{"dst":"default","gateway":"fe80::1","dev":"enp1s0","protocol":"ra","metric":1024}]`,
		},
		Lines: slices.Clone(Lines),
	}
}

// Maintenance is a node in maintenance mode: chalkd serves no status, and of its disks only the
// boot disk has a use.
func Maintenance() *Client {
	return &Client{
		InfoResp: &nodev1.InfoResponse{
			Mode: nodev1.Mode_MODE_MAINTENANCE, Hostname: "nixos", RootHash: hash(0x81, 0xbd),
			Fingerprint: "3b1f" + strings.Repeat("9e2d", 15),
		},
		StatusErr:   connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is in maintenance mode; install it first")),
		DisksResp:   disks(false),
		Diagnostics: map[string]string{"addresses": addresses},
		Lines:       slices.Clone(Lines),
	}
}

// Installer is the installer, which runs in maintenance mode.
func Installer() *Client {
	c := Maintenance()
	c.InfoResp.Installer = true
	c.InfoResp.ImageId = "chalkos-installer"
	return c
}

// Now is the fixed time of the tests.
var Now = time.Date(2026, 11, 13, 15, 4, 5, 0, time.UTC)

// Options are the console's options in tests: a fixed clock and uptime, no ticks, three units, two
// DNS servers, and a journal of its own that fails.
func Options() console.Options {
	return console.Options{
		Interval: time.Hour,
		Now:      func() time.Time { return Now },
		Uptime:   func() (time.Duration, error) { return 3*time.Hour + 12*time.Minute, nil },
		Units: func(context.Context) ([]string, error) {
			return []string{"chalkd.service", "chronyd.service", "systemd-networkd.service"}, nil
		},
		Journal:          func(context.Context, string, func(string)) error { return errors.New("no journal in tests") },
		DNSServers:       func() ([]string, error) { return []string{"192.168.1.1", "2001:db8::1"}, nil },
		PassphrasePrompt: func() bool { return false },
	}
}

// plain sets up lipgloss and the time zone once, before any console runs.
var plain sync.Once

// Start runs the console m on a terminal of 80 columns and 25 lines, with lipgloss writing no
// colours and times in UTC, so the screens compare as text on any machine.
func Start(t *testing.T, m tea.Model) *teatest.TestModel {
	t.Helper()
	plain.Do(func() {
		lipgloss.SetColorProfile(termenv.Ascii)
		time.Local = time.UTC
	})
	return teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 25))
}

// WaitFor waits until the screen has shown each of texts since the last wait.
func WaitFor(t *testing.T, tm *teatest.TestModel, texts ...string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		for _, text := range texts {
			if !bytes.Contains(b, []byte(text)) {
				return false
			}
		}
		return true
	},
		teatest.WithDuration(20*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
}

// Press presses a key, such as "tab", "shift+tab", "f10", "enter", "esc", "pgdown", or runes.
func Press(tm *teatest.TestModel, key string) { tm.Send(keyMsg(key)) }

// keys are the keys Press names.
var keys = map[string]tea.KeyType{
	"tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab, "f10": tea.KeyF10, "enter": tea.KeyEnter,
	"esc": tea.KeyEsc, "up": tea.KeyUp, "down": tea.KeyDown, "pgup": tea.KeyPgUp,
	"pgdown": tea.KeyPgDown, "home": tea.KeyHome, "end": tea.KeyEnd, "backspace": tea.KeyBackspace,
}

func keyMsg(key string) tea.KeyMsg {
	if k, ok := keys[key]; ok {
		return tea.KeyMsg{Type: k}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

// Snapshot ends the console, compares its last screen with testdata/<test name>.golden, and
// returns the screen; go test -update writes the file.
func Snapshot(t *testing.T, tm *teatest.TestModel) string {
	t.Helper()
	if err := tm.Quit(); err != nil {
		t.Fatal(err)
	}
	screen := tm.FinalModel(t, teatest.WithFinalTimeout(20*time.Second)).View()
	golden.RequireEqual(t, []byte(screen+"\n"))
	return screen
}

// Eventually waits until cond holds.
func Eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen", what)
		}
	}
}

// Clock is a clock a test moves on, which Options.Now reads.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock is a clock at Now.
func NewClock() *Clock { return &Clock{now: Now} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock on by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
