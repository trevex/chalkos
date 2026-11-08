package router

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	// vrfsOnly runs the VRFs' instances alone: unbound.service runs the default VRF's resolver.
	vrfsOnly bool
	// stopFirst stops the instances without a config before the others start, so that a link
	// that moved to another VRF is free before the other VRF's instance starts on it; otherwise
	// they stop last.
	stopFirst bool
	// restart restarts a running instance rather than reloading it, for a service that reads its
	// config at start alone. Without it, a running instance reloads, and restarts only when its
	// VRF's device was made anew.
	restart bool
	// links reads the links an instance's config serves, which the instance needs before it
	// starts or reloads; nil for a service that needs its VRF's device alone.
	links func(config []byte) ([]string, error)
	// then is a unit started, without waiting for it, once the instances run; "" for none.
	then string
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
			then:      "chalkos-dhcp6-routes.service",
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
// appears. The error joins one error per failure.
func (r *side) runServices(ctx context.Context, s perVRF, stop bool) error {
	var want []instance
	if !stop {
		want = wanted(s)
	}
	var errs []error
	if s.stopFirst {
		errs = append(errs, r.stopUnwanted(ctx, s, want)...)
	}
	deadline := time.Now().Add(deviceWait)
	var started []string
	for _, in := range want {
		ok, err := r.runInstance(ctx, s, in, deadline)
		if err != nil {
			errs = append(errs, err)
		}
		if ok {
			started = append(started, in.unit)
		}
	}
	errs = append(errs, r.awaitStarted(ctx, started)...)
	if !s.stopFirst {
		errs = append(errs, r.stopUnwanted(ctx, s, want)...)
	}
	if !stop && s.then != "" {
		if _, err := tool.Run(ctx, r.run, "systemctl", "start", "--no-block", s.then); err != nil {
			errs = append(errs, fmt.Errorf("failed to start %s", s.then))
		}
	}
	return errors.Join(errs...)
}

// wanted are the instances of s whose configs its output rendered, by service, then the default
// VRF's first and then by VRF.
func wanted(s perVRF) []instance {
	var want []instance
	for _, svc := range s.units {
		for _, vrf := range renderedVRFs(s.dir, svc) {
			if s.vrfsOnly && vrf == routing.DefaultVRF {
				continue
			}
			want = append(want, instance{svc: svc, vrf: vrf, unit: svc.Unit(vrf)})
		}
	}
	return want
}

// stopUnwanted stops and resets the units of s that systemd holds and that are no instance of
// want.
func (r *side) stopUnwanted(ctx context.Context, s perVRF, want []instance) []error {
	var patterns []string
	for _, svc := range s.units {
		if !s.vrfsOnly {
			patterns = append(patterns, svc.Unit(routing.DefaultVRF))
		}
		patterns = append(patterns, svc.Name+"@*.service")
	}
	units, err := systemctl.Units(ctx, r.run, patterns...)
	if err != nil {
		return []error{fmt.Errorf("failed to list the units: %w", err)}
	}
	var errs []error
	for _, u := range units {
		if slices.ContainsFunc(want, func(in instance) bool { return in.unit == u }) {
			continue
		}
		if _, err := tool.Run(ctx, r.run, "systemctl", "stop", u); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop %s", u))
			continue
		}
		r.resetFailed(ctx, u)
	}
	return errs
}

// runInstance waits until deadline at most for the devices of the instance in, then starts it
// when it does not run, restarts it when s restarts running instances or it is bound to a device
// its VRF no longer has, and reloads it otherwise. started reports a start or restart that
// systemd queued, which awaitStarted waits for.
func (r *side) runInstance(ctx context.Context, s perVRF, in instance, deadline time.Time) (started bool, err error) {
	if problem := r.awaitDevices(ctx, s, in, deadline); problem != "" {
		return false, fmt.Errorf("failed to run %s, %s", in.unit, problem)
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
			return false, fmt.Errorf("failed to reload %s", in.unit)
		}
		return false, nil
	}
	r.resetFailed(ctx, in.unit)
	if _, err := tool.Run(ctx, r.run, "systemctl", verb, "--no-block", in.unit); err != nil {
		return false, fmt.Errorf("failed to %s %s", verb, in.unit)
	}
	return true, nil
}

// awaitStarted waits startWait at most for the units started to be active or failed, asking each
// at least once, also after the wait for another used up the time, and fails naming those that
// are not active then, with their substate.
func (r *side) awaitStarted(ctx context.Context, started []string) []error {
	deadline := time.Now().Add(startWait)
	var errs []error
	for _, u := range started {
		var state string
		_ = poll.Until(ctx, "", time.Until(deadline), pollInterval, func() error {
			state, _ = systemctl.IsActive(ctx, r.run, u)
			if state == "active" || state == "failed" {
				return nil
			}
			return errors.New(state)
		})
		if state == "active" {
			continue
		}
		sub := "unknown"
		if props, err := systemctl.Show(ctx, r.run, []string{u}, "SubState"); err == nil && len(props) == 1 {
			sub = props[0]["SubState"]
		}
		errs = append(errs, fmt.Errorf("failed to start %s, which is %s (%s)", u, cmp.Or(state, "unknown"), sub))
	}
	return errs
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
