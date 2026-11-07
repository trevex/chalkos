package chalkd

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/sys/unix"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/tool"
)

func (s *Server) Status(ctx context.Context, req *connect.Request[nodev1.StatusRequest]) (*connect.Response[nodev1.StatusResponse], error) {
	resp := &nodev1.StatusResponse{Platform: readOSRelease(s.Paths.OSRelease)["CHALKOS_PLATFORM"]}
	dir, err := s.Loader.Slot()
	if err != nil {
		return nil, failed(connect.CodeInternal, "find the running image's config slot: %v", err)
	}
	if resp.ConfigVersion, err = slot.Version(dir); err != nil {
		return nil, failed(connect.CodeInternal, "read the config's version: %v", err)
	}
	if resp.PreviousConfigVersion, err = slot.Version(slot.Previous(dir)); err != nil {
		return nil, failed(connect.CodeInternal, "read the previous config's version: %v", err)
	}
	// A write interrupted between the previous link and the slot's leaves both naming one set,
	// which is no config to roll back to.
	if resp.PreviousConfigVersion == resp.ConfigVersion {
		resp.PreviousConfigVersion = ""
	}
	resp.Trial, resp.TrialError = s.trialStatus()

	var status storage.Status
	if raw, err := os.ReadFile(s.Paths.StorageStatus); err == nil {
		if err := json.Unmarshal(raw, &status); err != nil {
			return nil, failed(connect.CodeInternal, "parse %s: %v", s.Paths.StorageStatus, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	for _, name := range slices.Sorted(maps.Keys(status.Disks)) {
		d := status.Disks[name]
		resp.Disks = append(resp.Disks, &nodev1.DiskStatus{Name: name, Device: d.Device, Error: d.Error})
	}

	section, pins, err := s.recorded()
	if err != nil {
		return nil, err
	}
	mounts, err := mountPoints(s.Paths.MountInfo)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	for _, name := range slices.Sorted(maps.Keys(section.Volumes)) {
		v := section.Volumes[name]
		resp.Volumes = append(resp.Volumes, &nodev1.VolumeStatus{
			Name:       name,
			Disk:       v.Disk,
			MountPoint: v.MountPoint,
			Present:    pins.Disks[v.Disk].Partitions[name] != "",
			Mounted:    v.MountPoint != "" && mounts[v.MountPoint],
		})
	}

	out, err := tool.Run(ctx, s.Run, "systemctl", "list-units", "--state=failed", "--plain", "--no-legend", "--no-pager")
	if err != nil {
		return nil, failed(connect.CodeInternal, "list failed units: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			resp.FailedUnits = append(resp.FailedUnits, fields[0])
		}
	}
	if resp.Outputs, err = s.outputStatus(); err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	now := time.Now()
	resp.Certificates = s.certificates(now)
	resp.Trust = s.trust()
	for _, h := range layer.Active[layer.Certificates](s.Plugins) {
		certs, trust := h.Certificates(ctx, now)
		resp.Certificates = append(resp.Certificates, certs...)
		resp.Trust = append(resp.Trust, trust...)
	}
	resp.Time = s.timeStatus(ctx)
	resp.Boot = s.bootStatus()
	if req.Msg.GetCheckHealth() {
		resp.Boot.Health = s.bootHealth(ctx)
	}
	if resp.StagedConfigVersion, err = s.stagedVersion(resp.Boot); err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if resp.PendingConfig, err = s.pendingConfig(); err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	return connect.NewResponse(resp), nil
}

func mountPoints(mountInfo string) (map[string]bool, error) {
	data, err := os.ReadFile(mountInfo)
	if err != nil {
		return nil, err
	}
	points := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 4 {
			points[fields[4]] = true
		}
	}
	return points, nil
}

// outputStatus is each output of the running image with the hash of the files the node serves
// and why its last render or check failed, as the renders recorded them.
func (s *Server) outputStatus() ([]*nodev1.OutputStatus, error) {
	defs, err := outputs.Read(s.Loader.Outputs, s.Loader.Render.Root)
	if err != nil {
		return nil, fmt.Errorf("the image's outputs: %w", err)
	}
	recorded, err := outputs.ReadStatus(s.Loader.Render.Root)
	if err != nil {
		// The next render replaces it.
		log.Printf("the outputs' status: %v", err)
	}
	var list []*nodev1.OutputStatus
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		list = append(list, &nodev1.OutputStatus{Name: name, Hash: recorded[name].Hash, Error: recorded[name].Error})
	}
	return list, nil
}

// timeStatus reads chrony's tracking state.
func (s *Server) timeStatus(ctx context.Context) *nodev1.TimeStatus {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// chronyc fails on every request while chronyd is down: its error goes into the status, not
	// once more into chalkd's log.
	out, err := s.Run.Exec(ctx, tool.Cmd{Name: "chronyc", Args: []string{"-n", "-c", "tracking"}, Quiet: true})
	if err != nil {
		return &nodev1.TimeStatus{Error: err.Error()}
	}
	st, err := parseTracking(out)
	if err != nil {
		return &nodev1.TimeStatus{Error: err.Error()}
	}
	return st
}

// parseTracking reads chronyc's tracking report in its CSV form: reference ID, source, stratum,
// reference time, the system clock's offset (positive when it is slow), ... and the leap status
// as field 13. Later fields, which a newer chrony may add, are ignored.
func parseTracking(out []byte) (*nodev1.TimeStatus, error) {
	records, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil || len(records) != 1 || len(records[0]) < 14 {
		return nil, fmt.Errorf("chronyc tracking printed %q", strings.TrimSpace(string(out)))
	}
	r := records[0]
	stratum, err := strconv.Atoi(r[2])
	if err != nil {
		return nil, fmt.Errorf("chronyc tracking: stratum %q", r[2])
	}
	slow, err := strconv.ParseFloat(r[4], 64)
	if err != nil {
		return nil, fmt.Errorf("chronyc tracking: offset %q", r[4])
	}
	st := &nodev1.TimeStatus{
		Synchronised:  r[13] != "Not synchronised" && stratum > 0 && stratum < 16,
		OffsetSeconds: -slow,
	}
	if st.Synchronised {
		st.Source = r[1]
	}
	return st, nil
}

// bootHealth is the health of the boot the node runs, as Healthy finds it now with the units the
// image ignores, and whether the boot settled. A boot the loader counts is blessed once the boot's
// health check finds the same; an uncounted boot, of an entry blessed before, is checked by
// nothing else.
func (s *Server) bootHealth(ctx context.Context) *nodev1.BootHealth {
	h := &nodev1.BootHealth{Healthy: true, Settled: s.bootSettled(ctx)}
	if s.Healthy == nil {
		return h
	}
	if err := s.Healthy(ctx, nil); err != nil {
		h.Healthy, h.Error = false, err.Error()
	}
	return h
}

// bootSettled reports whether systemd finished starting the boot, the system running or degraded,
// and the image's health timeout passed since: the layers, such as Kubernetes's API server, come
// up after the units that start them, within the time a counted boot has to become healthy. A
// state that cannot be read counts as not settled, so a client keeps waiting until its timeout.
func (s *Server) bootSettled(ctx context.Context) bool {
	out, err := tool.Run(ctx, s.Run, "systemctl", "show", "--property=SystemState", "--property=FinishTimestampMonotonic")
	if err != nil {
		log.Printf("read whether the boot settled: %v", err)
		return false
	}
	var state string
	var finished time.Duration
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "SystemState":
			state = value
		case "FinishTimestampMonotonic":
			us, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				log.Printf("read when the boot finished starting: %v", err)
				return false
			}
			finished = time.Duration(us) * time.Microsecond
		}
	}
	if state != "running" && state != "degraded" || finished == 0 {
		return false
	}
	monotonic := s.Monotonic
	if monotonic == nil {
		monotonic = clockMonotonic
	}
	now, err := monotonic()
	if err != nil {
		log.Printf("read the monotonic clock: %v", err)
		return false
	}
	return now-finished >= s.HealthTimeout
}

// clockMonotonic reads the kernel's CLOCK_MONOTONIC.
func clockMonotonic() (time.Duration, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0, err
	}
	return time.Duration(ts.Nano()), nil
}
