package chalkd

import (
	"cmp"
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

	"golang.org/x/sys/unix"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/hostops"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/systemctl"
)

// statusTimeout bounds each command a status runs, such as systemctl, so a node whose systemd does
// not answer still answers with the other sections.
const statusTimeout = 5 * time.Second

// Status reads the node's status section by section. A section that cannot be read is left empty
// and named in the response's errors, with why; the others answer all the same.
func (s *Server) Status(ctx context.Context, req *nodev1.StatusRequest) (*nodev1.StatusResponse, error) {
	resp := &nodev1.StatusResponse{Platform: readOSRelease(s.Paths.OSRelease)["CHALKOS_PLATFORM"]}
	failed := func(section string, err error) bool {
		if err == nil {
			return false
		}
		log.Printf("status: %s: %v", section, err)
		resp.Errors = append(resp.Errors, &nodev1.SectionError{Section: section, Message: err.Error()})
		return true
	}
	if dir, version, err := s.runningVersion(); !failed("config", err) {
		resp.ConfigVersion = version
		resp.ConfigActivation = s.activationStatus(dir, version, s.bootID())
		resp.PreviousConfigVersion, err = slot.PreviousVersion(dir)
		failed("previous config", err)
	}
	resp.Trial, resp.TrialError = s.trialStatus()

	var err error
	resp.Disks, err = s.diskStatus()
	failed("disks", err)
	resp.Volumes, err = s.volumeStatus()
	failed("volumes", err)

	uctx, cancel := context.WithTimeout(ctx, statusTimeout)
	resp.FailedUnits, err = systemctl.FailedUnits(uctx, s.Run)
	if errors.Is(uctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("systemctl did not answer within %v", statusTimeout)
	}
	cancel()
	failed("failed units", err)
	resp.Outputs = s.outputStatus()
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
	if req.GetCheckHealth() {
		resp.Boot.Health = s.bootHealth(ctx)
	}
	resp.StagedConfigVersion, err = s.stagedVersion(resp.Boot)
	failed("staged config", err)
	resp.PendingConfig, err = s.pendingConfig()
	failed("pending config", err)
	return resp, nil
}

// diskStatus is the disks of the node's storage as the boot's storage setup recorded them in
// storage-status.json; none without the file.
func (s *Server) diskStatus() ([]*nodev1.DiskStatus, error) {
	var status storage.Status
	raw, err := os.ReadFile(s.Paths.StorageStatus)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.Paths.StorageStatus, err)
	}
	var disks []*nodev1.DiskStatus
	for _, name := range slices.Sorted(maps.Keys(status.Disks)) {
		d := status.Disks[name]
		disks = append(disks, &nodev1.DiskStatus{Name: name, Device: d.Device, Error: d.Error})
	}
	return disks, nil
}

// volumeStatus is the volumes STATE records, whether they are present and mounted, and the use of
// those mounted.
func (s *Server) volumeStatus() ([]*nodev1.VolumeStatus, error) {
	section, pins, err := s.recorded()
	if err != nil {
		return nil, err
	}
	mounts, err := hostops.MountPoints(s.Paths.MountInfo)
	if err != nil {
		return nil, err
	}
	var volumes []*nodev1.VolumeStatus
	for _, name := range slices.Sorted(maps.Keys(section.Volumes)) {
		v := section.Volumes[name]
		vs := &nodev1.VolumeStatus{
			Name:       name,
			Disk:       v.Disk,
			MountPoint: v.MountPoint,
			Present:    pins.Disks[v.Disk].Partitions[name] != "",
			Mounted:    v.MountPoint != "" && mounts[v.MountPoint],
			Encryption: cmp.Or(v.Encryption, storage.EncryptionNone),
		}
		if vs.Mounted {
			// A file system that cannot be read reports no use; the volume's state says the rest.
			if size, used, err := s.statfs(v.MountPoint); err == nil {
				vs.SizeBytes, vs.UsedBytes = size, used
			} else {
				log.Printf("read the use of %s: %v", v.MountPoint, err)
			}
		}
		volumes = append(volumes, vs)
	}
	return volumes, nil
}

// outputStatus is each output of the running image with the hash of the files the node serves
// and why its last render or check failed, as the renders recorded them.
func (s *Server) outputStatus() []*nodev1.OutputStatus {
	defs := s.Loader.Contract.Outputs
	recorded, err := outputs.ReadStatus(s.Loader.Render.Root)
	if err != nil {
		// The next render replaces it.
		log.Printf("the outputs' status: %v", err)
	}
	var list []*nodev1.OutputStatus
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		list = append(list, &nodev1.OutputStatus{Name: name, Hash: recorded[name].Hash, Error: recorded[name].Error})
	}
	return list
}

// timeStatus reads chrony's tracking state.
func (s *Server) timeStatus(ctx context.Context) *nodev1.TimeStatus {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
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
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	shown, err := systemctl.Show(ctx, s.Run, nil, "SystemState", "FinishTimestampMonotonic")
	if err != nil {
		log.Printf("read whether the boot settled: %v", err)
		return false
	}
	if len(shown) != 1 || shown[0]["SystemState"] != "running" && shown[0]["SystemState"] != "degraded" {
		return false
	}
	us, err := strconv.ParseInt(shown[0]["FinishTimestampMonotonic"], 10, 64)
	if err != nil {
		log.Printf("read when the boot finished starting: %v", err)
		return false
	}
	finished := time.Duration(us) * time.Microsecond
	if finished == 0 {
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

// statfs reads the size and use of the file system mounted at path.
func (s *Server) statfs(path string) (size, used uint64, err error) {
	if s.Statfs != nil {
		return s.Statfs(path)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	block := uint64(st.Frsize)
	return st.Blocks * block, (st.Blocks - st.Bfree) * block, nil
}
