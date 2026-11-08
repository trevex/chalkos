package router

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/internal/poll"
	routing "github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/router/services"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/systemctl"
)

// How long chalkd router services waits for the devices of all the instances it runs, and then
// for all the instances it started, and how often it looks again. Tests shorten them.
var (
	deviceWait   = 30 * time.Second
	startWait    = 30 * time.Second
	pollInterval = 200 * time.Millisecond
)

// perVRF is a service of the router whose instances chalkd router services runs, one per VRF of
// the configs its output renders into dir. Its fields are what the services differ in.
type perVRF struct {
	dir   string
	units []services.Service
	// vrfsOnly runs the VRFs' instances alone: the default VRF's runs by an effect of its own.
	vrfsOnly bool
	// stopFirst stops the instances without a config before the others start, so that a link
	// that moved to another VRF is free before the other VRF's instance starts on it; otherwise
	// they stop last.
	stopFirst bool
	// restart restarts a running instance rather than reloading it, for a service that reads its
	// config at start alone. Otherwise only an instance bound to a device its VRF no longer has
	// restarts.
	restart bool
	// links reads the links an instance's config serves, which the instance needs before it
	// starts or reloads; nil for a service that needs its VRF's device alone.
	links func(config []byte) ([]string, error)
	// after are the units started, without waiting for them, once the instances run.
	after []string
}

// perVRFServices are the services chalkd router services runs, by the name it takes.
func (r *side) perVRFServices() map[string]perVRF {
	return map[string]perVRF{
		// Kea refuses a config that names a link that does not exist, and stops when a reload
		// does. Its DHCPv6 servers' delegated prefixes are routed by chalkos-dhcp6-routes, which
		// follows the configs.
		"kea": {
			dir:       r.KeaDir,
			units:     []services.Service{services.Kea4, services.Kea6},
			stopFirst: true,
			links:     routing.ParseKeaInterfaces,
			after:     []string{"chalkos-dhcp6-routes.service"},
		},
		// unbound.service runs the default VRF's resolver.
		"unbound": {
			dir:      r.ResolverDir,
			units:    []services.Service{services.Unbound},
			vrfsOnly: true,
		},
		// dnsmasq reads its relays at start alone.
		"relay": {
			dir:       r.RelayDir,
			units:     []services.Service{services.Relay},
			stopFirst: true,
			restart:   true,
		},
	}
}

// instance is the instance of a service of a VRF whose config an output rendered.
type instance struct {
	svc  services.Service
	vrf  string
	unit string
}

// runServices runs the instances of the service s of the rendered configs: it starts those that
// do not run once their devices exist, reloads or restarts those that run and stops those
// without a config; with stop, it stops all of them. systemd binds a VRF's instance's sockets to
// the index the VRF's device had when the instance started, which the instance records: an
// instance bound to a device that a VRF made anew replaced restarts on the new one. A unit that
// requires a device that never comes waits for it until the device's job times out, so the run
// waits deviceWait at most for the devices and startWait more for the instances it started, and
// fails naming those that are not running then; a start still queued goes on once the device
// appears.
func (r *side) runServices(ctx context.Context, s perVRF, stop bool) error {
	var failed []string
	var want []instance
	wanted := map[string]bool{}
	if !stop {
		for _, svc := range s.units {
			for _, vrf := range renderedVRFs(s.dir, svc) {
				if s.vrfsOnly && vrf == routing.DefaultVRF {
					continue
				}
				in := instance{svc: svc, vrf: vrf, unit: svc.Unit(vrf)}
				want = append(want, in)
				wanted[in.unit] = true
			}
		}
	}
	stopOthers := func() {
		var patterns []string
		for _, svc := range s.units {
			if !s.vrfsOnly {
				patterns = append(patterns, svc.Unit(routing.DefaultVRF))
			}
			patterns = append(patterns, svc.Name+"@*.service")
		}
		units, err := systemctl.Units(ctx, r.run, patterns...)
		if err != nil {
			failed = append(failed, fmt.Sprintf("list the units: %v", err))
		}
		for _, u := range units {
			if wanted[u] {
				continue
			}
			if _, err := tool.Run(ctx, r.run, "systemctl", "stop", u); err != nil {
				failed = append(failed, "stop "+u)
				continue
			}
			r.resetFailed(ctx, u)
		}
	}
	if s.stopFirst {
		stopOthers()
	}
	deadline := time.Now().Add(deviceWait)
	var started []string
	for _, in := range want {
		if problem := r.awaitDevices(ctx, s, in, deadline); problem != "" {
			failed = append(failed, "run "+in.unit+", "+problem)
			continue
		}
		verb := "reload"
		if _, err := systemctl.IsActive(ctx, r.run, in.unit); err != nil {
			verb = "start"
		} else if s.restart {
			verb = "restart"
		} else if record := in.svc.IndexRecord(in.vrf); record != "" && r.staleDevice(in.vrf, filepath.Join(r.RunDir, record)) != "" {
			verb = "restart"
		}
		if verb == "reload" {
			if _, err := tool.Run(ctx, r.run, "systemctl", "reload", in.unit); err != nil {
				failed = append(failed, "reload "+in.unit)
			}
			continue
		}
		r.resetFailed(ctx, in.unit)
		if _, err := tool.Run(ctx, r.run, "systemctl", verb, "--no-block", in.unit); err != nil {
			failed = append(failed, verb+" "+in.unit)
			continue
		}
		started = append(started, in.unit)
	}
	deadline = time.Now().Add(startWait)
	for _, u := range started {
		// Each unit is asked at least once, also after the wait for another used up the deadline.
		var state string
		_ = poll.Until(ctx, "", time.Until(deadline), pollInterval, func() error {
			state, _ = systemctl.IsActive(ctx, r.run, u)
			if state == "active" || state == "failed" {
				return nil
			}
			return errors.New(state)
		})
		if state != "active" {
			sub := "unknown"
			if props, err := systemctl.Show(ctx, r.run, []string{u}, "SubState"); err == nil && len(props) == 1 {
				sub = props[0]["SubState"]
			}
			failed = append(failed, fmt.Sprintf("start %s, which is %s (%s)", u, cmp.Or(state, "unknown"), sub))
		}
	}
	if !s.stopFirst {
		stopOthers()
	}
	if !stop {
		for _, u := range s.after {
			if _, err := tool.Run(ctx, r.run, "systemctl", "start", "--no-block", u); err != nil {
				failed = append(failed, "start "+u)
			}
		}
	}
	errs := make([]error, len(failed))
	for i, f := range failed {
		errs[i] = errors.New("failed to " + f)
	}
	return errors.Join(errs...)
}

// resetFailed forgets that unit failed, so that it may start again, and that a unit stopped
// failed, so that it leaves systemd's list. A unit that did not fail stays as it is.
func (r *side) resetFailed(ctx context.Context, unit string) {
	_, _ = r.run.Exec(ctx, tool.Cmd{Name: "systemctl", Args: []string{"reset-failed", unit}, Quiet: true})
}

// awaitDevices waits until deadline at most for the devices the instance in needs: its VRF's and
// the links its config serves. It says why the instance cannot run, or is "" once they exist.
func (r *side) awaitDevices(ctx context.Context, s perVRF, in instance, deadline time.Time) string {
	var devices []string
	if s.links != nil {
		file := filepath.Join(s.dir, in.svc.File(in.vrf))
		data, err := os.ReadFile(file)
		if err == nil {
			devices, err = s.links(data)
		}
		if err != nil {
			return fmt.Sprintf("whose config %s cannot be read: %v", file, err)
		}
	}
	if in.vrf != routing.DefaultVRF {
		devices = append(devices, in.vrf)
	}
	var missing []string
	_ = poll.Until(ctx, "", time.Until(deadline), pollInterval, func() error {
		missing = missing[:0]
		for _, d := range devices {
			if _, err := os.Stat(filepath.Join(r.NetDir, d)); err != nil {
				missing = append(missing, d)
			}
		}
		if len(missing) > 0 {
			return errors.New("missing devices")
		}
		return nil
	})
	if len(missing) > 0 {
		return "whose devices " + strings.Join(missing, ", ") + " do not exist"
	}
	return ""
}
