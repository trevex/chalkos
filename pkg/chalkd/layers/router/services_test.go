package router

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd/layer/layertest"
	"github.com/trevex/chalkos/pkg/router/services"
	"github.com/trevex/chalkos/pkg/tool"
)

// fakeSystemd answers systemctl as chalkd router services asks it, over units whose state and
// substate it keeps. A unit started or restarted takes the state of next, active (running)
// without, and a VRF's instance records the index of its VRF's device in its runtime directory,
// as its ExecStartPre= does. A stopped unit that had not failed is inactive, and reset-failed
// forgets a failed or inactive unit. A call that fail holds, its arguments joined by spaces, fails.
type fakeSystemd struct {
	mu    sync.Mutex
	r     *side
	state map[string][2]string
	next  map[string][2]string
	fail  map[string]bool
	calls []string
	index int
}

func newFakeSystemd(t *testing.T) (*fakeSystemd, *side) {
	t.Helper()
	p, _ := routerNode(t)
	f := &fakeSystemd{r: p.r, state: map[string][2]string{}, next: map[string][2]string{}, fail: map[string]bool{}}
	p.r.run = f
	deviceWait, startWait, pollInterval = time.Second, 500*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { deviceWait, startWait, pollInterval = 30*time.Second, 30*time.Second, 200*time.Millisecond })
	return f, p.r
}

func (f *fakeSystemd) Exec(_ context.Context, c tool.Cmd) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.Name != "systemctl" {
		return nil, errors.New("no program " + c.Name)
	}
	call := strings.Join(c.Args, " ")
	f.calls = append(f.calls, call)
	if f.fail[call] {
		return nil, errors.New("exit status 1")
	}
	args := slices.DeleteFunc(slices.Clone(c.Args), func(a string) bool { return strings.HasPrefix(a, "--") && !strings.HasPrefix(a, "--property") })
	verb, unit := args[0], args[len(args)-1]
	st, ok := f.state[unit]
	if !ok {
		st = [2]string{"inactive", "dead"}
	}
	switch verb {
	case "is-active":
		if st[0] != "active" {
			return []byte(st[0] + "\n"), errors.New("exit status 3")
		}
		return []byte("active\n"), nil
	case "show":
		return []byte("SubState=" + st[1] + "\n"), nil
	case "start", "restart":
		next, ok := f.next[unit]
		if !ok {
			next = [2]string{"active", "running"}
		}
		f.state[unit] = next
		f.record(unit)
	case "reload":
		if st[0] != "active" {
			return nil, errors.New("exit status 1")
		}
	case "stop":
		if st[0] != "failed" {
			f.state[unit] = [2]string{"inactive", "dead"}
		}
	case "reset-failed":
		if st[0] == "failed" || st[0] == "inactive" {
			delete(f.state, unit)
		}
	case "list-units":
		var out strings.Builder
		for _, u := range slices.Sorted(func(yield func(string) bool) {
			for u := range f.state {
				if !yield(u) {
					return
				}
			}
		}) {
			for _, p := range args[1:] {
				if ok, _ := path.Match(p, u); ok {
					out.WriteString(u + " loaded " + f.state[u][0] + " " + f.state[u][1] + " Unit\n")
					break
				}
			}
		}
		return []byte(out.String()), nil
	default:
		return nil, errors.New("no verb " + verb)
	}
	return nil, nil
}

// record writes the index record of the VRF's instance unit, whose VRF has a device.
func (f *fakeSystemd) record(unit string) {
	entries, _ := os.ReadDir(f.r.NetDir)
	for _, svc := range []services.Service{services.Kea4, services.Kea6, services.Unbound, services.Relay} {
		for _, e := range entries {
			if svc.Unit(e.Name()) != unit || svc.IndexRecord(e.Name()) == "" {
				continue
			}
			index, _ := os.ReadFile(filepath.Join(f.r.NetDir, e.Name(), "ifindex"))
			record := filepath.Join(f.r.RunDir, svc.IndexRecord(e.Name()))
			_ = os.MkdirAll(filepath.Dir(record), 0o755)
			_ = os.WriteFile(record, index, 0o644)
		}
	}
}

// device makes the device name anew, as networkd does, with the next index. Tests call it while
// a run goes on, too.
func (f *fakeSystemd) device(t *testing.T, name string) {
	t.Helper()
	f.mu.Lock()
	f.index++
	index := strconv.Itoa(f.index)
	f.mu.Unlock()
	dir := filepath.Join(f.r.NetDir, name)
	err := os.RemoveAll(dir)
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "ifindex"), []byte(index+"\n"), 0o644)
	}
	if err != nil {
		t.Error(err)
	}
}

// run runs chalkd router services name, with stop when stop, forgetting the calls of runs
// before.
func (f *fakeSystemd) run(name string, stop bool) error {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	return f.r.runServices(context.Background(), f.r.perVRFServices()[name], stop)
}

func (f *fakeSystemd) called(t *testing.T, call string) {
	t.Helper()
	if !slices.Contains(f.calls, call) {
		t.Errorf("no call systemctl %s in %q", call, f.calls)
	}
}

func (f *fakeSystemd) notCalled(t *testing.T, call string) {
	t.Helper()
	if slices.Contains(f.calls, call) {
		t.Errorf("called systemctl %s", call)
	}
}

// before fails unless systemctl a came before b.
func (f *fakeSystemd) before(t *testing.T, a, b string) {
	t.Helper()
	i, j := slices.Index(f.calls, a), slices.Index(f.calls, b)
	if i < 0 || j < 0 || i > j {
		t.Errorf("systemctl %s did not come before %s in %q", a, b, f.calls)
	}
}

func (f *fakeSystemd) running(t *testing.T, unit string) {
	t.Helper()
	if s := f.state[unit][0]; s != "active" {
		t.Errorf("%s is %q", unit, s)
	}
}

// onDevice fails unless the VRF's instance of svc recorded the VRF's device's index.
func (f *fakeSystemd) onDevice(t *testing.T, svc services.Service, vrf string) {
	t.Helper()
	if stale := f.r.staleDevice(vrf, filepath.Join(f.r.RunDir, svc.IndexRecord(vrf))); stale != "" {
		t.Errorf("%s: %s", svc.Unit(vrf), stale)
	}
}

func touch(t *testing.T, file, content string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// failedWith fails unless err names each of the failures want and no other.
func failedWith(t *testing.T, err error, want ...string) {
	t.Helper()
	var got []string
	if err != nil {
		got = strings.Split(err.Error(), "\n")
	}
	for i := range want {
		want[i] = "failed to " + want[i]
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("failures = %q, want %q", got, want)
	}
}

// The resolvers of the VRFs start once their devices exist, VRF names escaped, the default VRF's
// left to unbound.service, and reload on the next run. A VRF made anew, one whose device is gone
// and appears during the run, and a resolver that recorded no index restart. Resolvers without a
// config, a running and a failed one, stop and are reset after the others ran. A VRF without a
// device and a resolver that does not come up fail the run, named with the substate, and stop
// stops them all.
func TestServicesUnbound(t *testing.T) {
	f, r := newFakeSystemd(t)
	for _, vrf := range []string{"guest", "tenant", "dmz-a"} {
		f.device(t, vrf)
		touch(t, filepath.Join(r.ResolverDir, services.Unbound.File(vrf)), "")
	}
	touch(t, filepath.Join(r.ResolverDir, "unbound.conf"), "")
	if err := f.run("unbound", false); err != nil {
		t.Fatal(err)
	}
	for _, vrf := range []string{"guest", "tenant", "dmz-a"} {
		u := services.Unbound.Unit(vrf)
		f.called(t, "reset-failed "+u)
		f.called(t, "start --no-block "+u)
		f.running(t, u)
		f.onDevice(t, services.Unbound, vrf)
	}
	f.called(t, `start --no-block unbound@dmz\x2da.service`)
	f.notCalled(t, "start --no-block unbound.service")
	f.called(t, "list-units --all --plain --no-legend --no-pager unbound@*.service")

	if err := f.run("unbound", false); err != nil {
		t.Fatal(err)
	}
	for _, vrf := range []string{"guest", "tenant", "dmz-a"} {
		f.called(t, "reload "+services.Unbound.Unit(vrf))
		f.notCalled(t, "restart --no-block "+services.Unbound.Unit(vrf))
	}

	// chalkos-vrf removed tenant and dmz-a: networkd made tenant anew before the run, and makes
	// dmz-a during it.
	f.device(t, "tenant")
	if err := os.RemoveAll(filepath.Join(r.NetDir, "dmz-a")); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.device(t, "dmz-a")
	}()
	if err := f.run("unbound", false); err != nil {
		t.Fatal(err)
	}
	for _, vrf := range []string{"tenant", "dmz-a"} {
		f.called(t, "restart --no-block "+services.Unbound.Unit(vrf))
		f.notCalled(t, "reload "+services.Unbound.Unit(vrf))
		f.onDevice(t, services.Unbound, vrf)
	}
	f.called(t, "reload unbound@guest.service")

	if err := os.Remove(filepath.Join(r.RunDir, services.Unbound.IndexRecord("guest"))); err != nil {
		t.Fatal(err)
	}
	if err := f.run("unbound", false); err != nil {
		t.Fatal(err)
	}
	f.called(t, "restart --no-block unbound@guest.service")
	f.onDevice(t, services.Unbound, "guest")

	f.state["unbound@old.service"] = [2]string{"active", "running"}
	f.state["unbound@stale.service"] = [2]string{"failed", "failed"}
	if err := f.run("unbound", false); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"unbound@old.service", "unbound@stale.service"} {
		f.called(t, "stop "+u)
		f.called(t, "reset-failed "+u)
		if _, ok := f.state[u]; ok {
			t.Errorf("%s is still listed", u)
		}
		f.before(t, "reload unbound@guest.service", "stop "+u)
	}
	f.notCalled(t, "stop unbound@guest.service")

	// gone has no device, stuck's resolver keeps restarting, and late's device appears during
	// the wait.
	for _, vrf := range []string{"gone", "stuck", "late"} {
		touch(t, filepath.Join(r.ResolverDir, services.Unbound.File(vrf)), "")
	}
	f.device(t, "stuck")
	f.next["unbound@stuck.service"] = [2]string{"activating", "auto-restart"}
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.device(t, "late")
	}()
	failedWith(t, f.run("unbound", false),
		"run unbound@gone.service, whose devices gone do not exist",
		"start unbound@stuck.service, which is activating (auto-restart)")
	f.notCalled(t, "start --no-block unbound@gone.service")
	for _, vrf := range []string{"guest", "tenant", "dmz-a", "late"} {
		f.running(t, services.Unbound.Unit(vrf))
	}

	if err := f.run("unbound", true); err != nil {
		t.Fatal(err)
	}
	for _, vrf := range []string{"guest", "tenant", "dmz-a", "late", "stuck"} {
		f.called(t, "stop "+services.Unbound.Unit(vrf))
	}
}

// A relay runs for the default VRF's config and each VRF's once the VRF's device exists, and
// restarts on each run, as dnsmasq reads its relays at start alone. Relays without a config stop
// and are reset before the others start, and a VRF without a device and a relay that does not
// come up fail the run.
func TestServicesRelay(t *testing.T) {
	f, r := newFakeSystemd(t)
	if err := f.run("relay", false); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "start") }) {
		t.Errorf("a run without configs started a relay: %q", f.calls)
	}

	for _, vrf := range []string{"default", "guest", "dmz-a"} {
		if vrf != "default" {
			f.device(t, vrf)
		}
		touch(t, filepath.Join(r.RelayDir, services.Relay.File(vrf)), "")
	}
	if err := f.run("relay", false); err != nil {
		t.Fatal(err)
	}
	for _, vrf := range []string{"default", "guest", "dmz-a"} {
		f.called(t, "start --no-block "+services.Relay.Unit(vrf))
		f.running(t, services.Relay.Unit(vrf))
	}
	f.called(t, "list-units --all --plain --no-legend --no-pager dnsmasq-relay.service dnsmasq-relay@*.service")

	if err := f.run("relay", false); err != nil {
		t.Fatal(err)
	}
	for _, vrf := range []string{"default", "guest", "dmz-a"} {
		f.called(t, "restart --no-block "+services.Relay.Unit(vrf))
		f.notCalled(t, "reload "+services.Relay.Unit(vrf))
	}

	if err := os.Remove(filepath.Join(r.RelayDir, services.Relay.File("dmz-a"))); err != nil {
		t.Fatal(err)
	}
	f.state["dnsmasq-relay@stale.service"] = [2]string{"failed", "failed"}
	touch(t, filepath.Join(r.RelayDir, services.Relay.File("gone")), "")
	f.device(t, "stuck")
	touch(t, filepath.Join(r.RelayDir, services.Relay.File("stuck")), "")
	f.next["dnsmasq-relay@stuck.service"] = [2]string{"activating", "auto-restart"}
	failedWith(t, f.run("relay", false),
		"run dnsmasq-relay@gone.service, whose devices gone do not exist",
		"start dnsmasq-relay@stuck.service, which is activating (auto-restart)")
	for _, u := range []string{`dnsmasq-relay@dmz\x2da.service`, "dnsmasq-relay@stale.service"} {
		f.called(t, "stop "+u)
		f.called(t, "reset-failed "+u)
		f.before(t, "stop "+u, "restart --no-block dnsmasq-relay@guest.service")
	}
	f.notCalled(t, "start --no-block dnsmasq-relay@gone.service")
	f.running(t, "dnsmasq-relay@guest.service")

	if err := f.run("relay", true); err != nil {
		t.Fatal(err)
	}
	for _, vrf := range []string{"default", "guest", "stuck"} {
		f.called(t, "stop "+services.Relay.Unit(vrf))
	}
}

// writeKea4 writes the DHCPv4 config of the VRF's server of the links.
func writeKea4(t *testing.T, r *side, vrf string, links ...string) {
	t.Helper()
	touch(t, filepath.Join(r.KeaDir, services.Kea4.File(vrf)), `{"Dhcp4": {"interfaces-config": {"interfaces": ["`+strings.Join(links, `", "`)+`"]}}}`)
}

// A DHCP server runs for each rendered config once the config's links and its VRF's device
// exist, the default VRF's too, and reloads on the next run. A VRF's server restarts when its
// device was made anew or it recorded none. Servers without a config stop and are reset before
// the others start, chalkos-dhcp6-routes starts after the servers, and a server whose links do
// not appear, whose config cannot be read or that does not come up fails the run without a
// reload or start.
func TestServicesKea(t *testing.T) {
	f, r := newFakeSystemd(t)
	for _, d := range []string{"lan0", "vlan20", "vlan30", "guest", "dmz-a"} {
		f.device(t, d)
	}
	if err := f.run("kea", false); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "start --no-block kea") }) {
		t.Errorf("a run without configs started a server: %q", f.calls)
	}
	f.called(t, "start --no-block chalkos-dhcp6-routes.service")
	f.called(t, "list-units --all --plain --no-legend --no-pager kea-dhcp4.service kea-dhcp4@*.service kea-dhcp6.service kea-dhcp6@*.service")

	writeKea4(t, r, "default", "lan0")
	writeKea4(t, r, "guest", "vlan20")
	writeKea4(t, r, "dmz-a", "vlan30")
	if err := f.run("kea", false); err != nil {
		t.Fatal(err)
	}
	for _, vrf := range []string{"default", "guest", "dmz-a"} {
		f.called(t, "reset-failed "+services.Kea4.Unit(vrf))
		f.called(t, "start --no-block "+services.Kea4.Unit(vrf))
		f.running(t, services.Kea4.Unit(vrf))
	}
	f.onDevice(t, services.Kea4, "guest")
	f.onDevice(t, services.Kea4, "dmz-a")

	if err := f.run("kea", false); err != nil {
		t.Fatal(err)
	}
	for _, vrf := range []string{"default", "guest", "dmz-a"} {
		f.called(t, "reload "+services.Kea4.Unit(vrf))
		f.notCalled(t, "restart --no-block "+services.Kea4.Unit(vrf))
	}

	// chalkos-vrf removed guest and networkd made it anew; vlan30 comes back during the run.
	f.device(t, "guest")
	if err := os.RemoveAll(filepath.Join(r.NetDir, "vlan30")); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.device(t, "vlan30")
	}()
	if err := f.run("kea", false); err != nil {
		t.Fatal(err)
	}
	f.called(t, "restart --no-block kea-dhcp4@guest.service")
	f.notCalled(t, "reload kea-dhcp4@guest.service")
	f.onDevice(t, services.Kea4, "guest")
	f.called(t, `reload kea-dhcp4@dmz\x2da.service`)
	f.called(t, "reload kea-dhcp4.service")

	if err := os.Remove(filepath.Join(r.RunDir, services.Kea4.IndexRecord("dmz-a"))); err != nil {
		t.Fatal(err)
	}
	f.device(t, "vlan31")
	touch(t, filepath.Join(r.KeaDir, services.Kea6.File("dmz-a")), `{"Dhcp6": {"interfaces-config": {"interfaces": ["vlan31"]}}}`)
	if err := f.run("kea", false); err != nil {
		t.Fatal(err)
	}
	f.called(t, `restart --no-block kea-dhcp4@dmz\x2da.service`)
	f.onDevice(t, services.Kea4, "dmz-a")
	f.onDevice(t, services.Kea6, "dmz-a")
	f.before(t, `start --no-block kea-dhcp6@dmz\x2da.service`, "start --no-block chalkos-dhcp6-routes.service")

	// Servers without a config stop before a new server starts, as when a subnet moves from
	// old's VRF to new's.
	f.state["kea-dhcp4@old.service"] = [2]string{"active", "running"}
	f.state["kea-dhcp4@stale.service"] = [2]string{"failed", "failed"}
	f.device(t, "new")
	f.device(t, "vlan70")
	writeKea4(t, r, "new", "vlan70")
	if err := f.run("kea", false); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"kea-dhcp4@old.service", "kea-dhcp4@stale.service"} {
		f.called(t, "stop "+u)
		f.called(t, "reset-failed "+u)
		f.before(t, "stop "+u, "start --no-block kea-dhcp4@new.service")
	}
	f.running(t, "kea-dhcp4@new.service")

	// The default VRF's link is gone, gone's VRF has no device, broken's config is no JSON,
	// stuck's server keeps restarting, and late's link appears during the wait.
	if err := os.RemoveAll(filepath.Join(r.NetDir, "lan0")); err != nil {
		t.Fatal(err)
	}
	f.device(t, "vlan40")
	writeKea4(t, r, "gone", "vlan40")
	f.device(t, "broken")
	touch(t, filepath.Join(r.KeaDir, services.Kea4.File("broken")), "{")
	f.device(t, "stuck")
	f.device(t, "vlan50")
	writeKea4(t, r, "stuck", "vlan50")
	f.next["kea-dhcp4@stuck.service"] = [2]string{"activating", "auto-restart"}
	f.device(t, "late")
	writeKea4(t, r, "late", "vlan60")
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.device(t, "vlan60")
	}()
	failedWith(t, f.run("kea", false),
		"run kea-dhcp4.service, whose devices lan0 do not exist",
		"run kea-dhcp4@gone.service, whose devices gone do not exist",
		"run kea-dhcp4@broken.service, whose config "+filepath.Join(r.KeaDir, "kea-dhcp4-broken.json")+" cannot be read: read Kea's config: unexpected end of JSON input",
		"start kea-dhcp4@stuck.service, which is activating (auto-restart)")
	f.notCalled(t, "reload kea-dhcp4.service")
	f.notCalled(t, "start --no-block kea-dhcp4@gone.service")
	f.notCalled(t, "start --no-block kea-dhcp4@broken.service")
	f.running(t, "kea-dhcp4.service")
	f.running(t, "kea-dhcp4@late.service")

	if err := f.run("kea", true); err != nil {
		t.Fatal(err)
	}
	for _, vrf := range []string{"default", "guest", "dmz-a", "late", "stuck"} {
		f.called(t, "stop "+services.Kea4.Unit(vrf))
	}
	f.called(t, `stop kea-dhcp6@dmz\x2da.service`)
	f.notCalled(t, "start --no-block chalkos-dhcp6-routes.service")
}

// chalkd router services takes one of its services and, to stop them, stop.
func TestServicesCommand(t *testing.T) {
	p, _ := routerNode(t)
	services := p.Commands()[1]
	if services.Name() != "services" {
		t.Fatalf("the second command is %s", services.Name())
	}
	for _, args := range [][]string{nil, {"bind"}, {"kea", "start"}, {"kea", "stop", "now"}} {
		if !layertest.Refused(services, args) {
			t.Errorf("services %q is no usage error", args)
		}
	}
	for _, args := range [][]string{{"kea"}, {"unbound", "stop"}, {"relay"}} {
		if layertest.Refused(services, args) {
			t.Errorf("services %q is refused", args)
		}
	}
}

// A config of a VRF's DHCP server alone starts no server of the default VRF.
func TestServicesKeaVRFAlone(t *testing.T) {
	f, r := newFakeSystemd(t)
	f.device(t, "guest")
	f.device(t, "vlan20")
	writeKea4(t, r, "guest", "vlan20")
	if err := f.run("kea", false); err != nil {
		t.Fatal(err)
	}
	f.called(t, "start --no-block kea-dhcp4@guest.service")
	f.notCalled(t, "start --no-block kea-dhcp4.service")
	f.notCalled(t, "reload kea-dhcp4.service")
}

// A systemctl call that fails fails the run, naming what failed, and the run goes on with the
// others.
func TestServicesCallsFail(t *testing.T) {
	for _, tc := range []struct {
		call, want string
		running    bool
	}{
		{"list-units --all --plain --no-legend --no-pager kea-dhcp4.service kea-dhcp4@*.service kea-dhcp6.service kea-dhcp6@*.service", "failed to list the units: exit status 1", false},
		{"stop kea-dhcp4@old.service", "failed to stop kea-dhcp4@old.service", false},
		{"reload kea-dhcp4@guest.service", "failed to reload kea-dhcp4@guest.service", true},
		{"start --no-block kea-dhcp4@guest.service", "failed to start kea-dhcp4@guest.service", false},
		{"start --no-block chalkos-dhcp6-routes.service", "failed to start chalkos-dhcp6-routes.service", false},
	} {
		t.Run(tc.call, func(t *testing.T) {
			f, r := newFakeSystemd(t)
			f.device(t, "guest")
			f.device(t, "vlan20")
			writeKea4(t, r, "guest", "vlan20")
			f.state["kea-dhcp4@old.service"] = [2]string{"active", "running"}
			if tc.running {
				f.state["kea-dhcp4@guest.service"] = [2]string{"active", "running"}
				f.record("kea-dhcp4@guest.service")
			}
			f.fail[tc.call] = true
			err := f.run("kea", false)
			if err == nil || err.Error() != tc.want {
				t.Errorf("run = %v, want %s", err, tc.want)
			}
			f.called(t, "start --no-block chalkos-dhcp6-routes.service")
		})
	}
}
