package chalkd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

// cryptsetupPath only names the generator's units here; systemd runs the generator itself.
const cryptsetupPath = "systemd-cryptsetup"

// delivered is an identity as it arrives, with what chalkd reads from it.
type delivered struct {
	data    []byte
	section storage.Section
}

func parseIdentity(data string) (delivered, error) {
	var id struct {
		Storage storage.Section `json:"storage"`
	}
	if err := json.Unmarshal([]byte(data), &id); err != nil {
		return delivered{}, failed(connect.CodeInvalidArgument, "parse the identity: %v", err)
	}
	if _, ok := id.Storage.Disks[storage.SystemDisk]; !ok {
		return delivered{}, failed(connect.CodeInvalidArgument, "the identity's storage section has no system disk")
	}
	return delivered{data: []byte(data), section: id.Storage}, nil
}

// classify compares the delivered section with the recorded one and the pinned disks.
func (s *Server) classify(recorded, section storage.Section, pins storage.Pins) []storage.Change {
	changes := storage.Classify(recorded, section)
	return append(changes, storage.ClassifyDisks(section, pins, func(ref storage.Ref) (storage.Identity, error) {
		disk, err := s.Host.Resolve(ref)
		return disk.Identity, err
	})...)
}

func (s *Server) ApplyIdentity(ctx context.Context, req *connect.Request[nodev1.ApplyIdentityRequest]) (*connect.Response[nodev1.ApplyIdentityResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := parseIdentity(req.Msg.Identity)
	if err != nil {
		return nil, err
	}
	recorded, pins, err := s.recorded()
	if err != nil {
		return nil, err
	}
	changes := s.classify(recorded, d.section, pins)
	var destructive []string
	for _, c := range changes {
		if c.Class == storage.Destructive {
			destructive = append(destructive, fmt.Sprintf("%s; reset it with chalkctl storage reset <node> %s", c, c.Volume))
		}
	}
	if len(destructive) > 0 {
		return nil, failed(connect.CodeFailedPrecondition, "the identity changes storage destructively: %s", strings.Join(destructive, "; "))
	}
	if len(changes) > 0 {
		if err := s.applyStorage(ctx, recorded, d.section, req.Msg.FallbackSecret, changes); err != nil {
			return nil, err
		}
	}
	restarted, err := s.applyIdentity(ctx, d.data)
	if err != nil {
		return nil, err
	}
	resp := &nodev1.ApplyIdentityResponse{RestartedUnits: restarted}
	for _, c := range changes {
		resp.Changes = append(resp.Changes, &nodev1.StorageChange{Volume: c.Volume, Reason: c.Reason})
	}
	return connect.NewResponse(resp), nil
}

// applyStorage applies additive changes: repart creates and grows partitions, new encrypted
// volumes get the fallback keyslot, and the generator's units for new volumes start. Volumes
// that moved to another mount point are unmounted first.
func (s *Server) applyStorage(ctx context.Context, recorded, section storage.Section, secret string, changes []storage.Change) error {
	added := map[string]bool{}
	for name, v := range section.Volumes {
		if _, ok := recorded.Volumes[name]; !ok {
			added[name] = true
			if v.Encryption == storage.EncryptionTPM2 && section.Fallback != storage.FallbackNone && secret == "" {
				return failed(connect.CodeInvalidArgument, "volume %s is encrypted and the node's fallback is %s, but no fallback secret was sent", name, section.Fallback)
			}
		}
	}
	st := s.storage()
	old, err := node.Generate(st.StorageDir(), cryptsetupPath)
	if err != nil {
		return failed(connect.CodeInternal, "%v", err)
	}
	var moved []string
	for _, c := range changes {
		if _, ok := recorded.Volumes[c.Volume]; ok && recorded.Volumes[c.Volume].MountPoint != section.Volumes[c.Volume].MountPoint {
			moved = append(moved, c.Volume)
		}
	}
	for _, name := range moved {
		if err := s.stopUnits(ctx, old.Of(name)); err != nil {
			return err
		}
	}

	status := storage.Status{Installed: true, Disks: map[string]storage.DiskStatus{}}
	pins, err := st.Apply(ctx, section, &status)
	if err != nil {
		return failed(connect.CodeFailedPrecondition, "%v", err)
	}
	var problems []string
	for _, name := range sortedNames(status.Disks) {
		if e := status.Disks[name].Error; e != "" {
			problems = append(problems, fmt.Sprintf("disk %s: %s", name, e))
		}
	}
	if len(problems) > 0 {
		return failed(connect.CodeFailedPrecondition, "%s", strings.Join(problems, "; "))
	}
	if section.Fallback != storage.FallbackNone {
		devices := map[string]string{}
		for name := range added {
			v := section.Volumes[name]
			if v.Encryption != storage.EncryptionTPM2 {
				continue
			}
			if v.Disk == storage.SystemDisk {
				devices[name] = filepath.Join(s.Paths.BootPartitions, v.Label)
			} else {
				devices[name] = "/dev/disk/by-partuuid/" + pins.Disks[v.Disk].Partitions[name]
			}
		}
		if err := install.Enroll(ctx, s.Run, devices, secret); err != nil {
			return failed(connect.CodeFailedPrecondition, "%v", err)
		}
	}
	data, err := json.Marshal(section)
	if err != nil {
		return err
	}
	if err := install.WriteFile(filepath.Join(st.StorageDir(), "storage.json"), data, 0o600); err != nil {
		return failed(connect.CodeInternal, "record the storage section: %v", err)
	}
	if _, err := s.Run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return failed(connect.CodeInternal, "%v", err)
	}
	units, err := node.Generate(st.StorageDir(), cryptsetupPath)
	if err != nil {
		return failed(connect.CodeInternal, "%v", err)
	}
	var start []string
	for _, name := range append(sortedNames(added), moved...) {
		start = append(start, units.Of(name)...)
	}
	if len(start) > 0 {
		if _, err := s.Run.Run(ctx, "systemctl", append([]string{"start"}, start...)...); err != nil {
			return failed(connect.CodeFailedPrecondition, "start the units of new volumes: %v", err)
		}
	}
	return nil
}

func (s *Server) stopUnits(ctx context.Context, units []string) error {
	if len(units) == 0 {
		return nil
	}
	if _, err := s.Run.Run(ctx, "systemctl", append([]string{"stop"}, units...)...); err != nil {
		return failed(connect.CodeFailedPrecondition, "stop %s: %v", strings.Join(units, ", "), err)
	}
	return nil
}

// applyIdentity records the identity on STATE, applies it, and restarts the units that read
// keys whose values changed.
func (s *Server) applyIdentity(ctx context.Context, data []byte) ([]string, error) {
	path := filepath.Join(s.Paths.StateDir, "identity.json")
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	if err := install.WriteFile(path, data, 0o600); err != nil {
		return nil, failed(connect.CodeInternal, "record the identity: %v", err)
	}
	if err := s.Identity.Apply(data); err != nil {
		return nil, failed(connect.CodeInternal, "apply the identity: %v", err)
	}
	if _, err := s.Run.Run(ctx, "networkctl", "reload"); err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	consumers, err := identity.ReadConsumers(s.Identity.Consumers)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	units, err := identity.Restarts(consumers, old, data)
	if err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	for _, unit := range units {
		if _, err := s.Run.Run(ctx, "systemctl", "try-restart", unit); err != nil {
			return nil, failed(connect.CodeInternal, "restart %s: %v", unit, err)
		}
	}
	return units, nil
}

func (s *Server) ResetVolume(ctx context.Context, req *connect.Request[nodev1.ResetVolumeRequest]) (*connect.Response[nodev1.ResetVolumeResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := req.Msg.Volume
	d, err := parseIdentity(req.Msg.Identity)
	if err != nil {
		return nil, err
	}
	recorded, pins, err := s.recorded()
	if err != nil {
		return nil, err
	}
	v, ok := recorded.Volumes[name]
	if !ok {
		return nil, failed(connect.CodeNotFound, "the node has no volume %s", name)
	}
	if name == storage.VarVolume {
		return nil, failed(connect.CodeFailedPrecondition, "VAR holds the running node's data and cannot be reset while it runs")
	}
	if nv, ok := d.section.Volumes[name]; ok && nv.Disk != v.Disk {
		return nil, failed(connect.CodeFailedPrecondition, "volume %s moves from disk %s to %s; a reset recreates a volume on its own disk only", name, v.Disk, nv.Disk)
	}
	var others []string
	for _, c := range s.classify(recorded, d.section, pins) {
		if c.Class == storage.Destructive && c.Volume != name {
			others = append(others, c.String())
		}
	}
	if len(others) > 0 {
		return nil, failed(connect.CodeFailedPrecondition, "the identity changes other volumes destructively too: %s", strings.Join(others, "; "))
	}
	if nv, ok := d.section.Volumes[name]; ok && nv.Encryption == storage.EncryptionTPM2 && d.section.Fallback != storage.FallbackNone && req.Msg.FallbackSecret == "" {
		return nil, failed(connect.CodeInvalidArgument, "volume %s is encrypted and the node's fallback is %s, but no fallback secret was sent", name, d.section.Fallback)
	}

	if err := s.deleteVolume(ctx, name, v, pins); err != nil {
		return nil, err
	}
	// The volume is gone, so it is created again like a new one.
	without := recorded
	without.Volumes = map[string]storage.Volume{}
	for n, vol := range recorded.Volumes {
		if n != name {
			without.Volumes[n] = vol
		}
	}
	changes := storage.Classify(without, d.section)
	if err := s.applyStorage(ctx, without, d.section, req.Msg.FallbackSecret, changes); err != nil {
		return nil, err
	}
	log.Printf("volume %s reset", name)
	return connect.NewResponse(&nodev1.ResetVolumeResponse{}), nil
}

// deleteVolume stops a volume's units, wipes its partition and deletes it, after checking the
// partition lies on the disk the volume's disk name is pinned to.
func (s *Server) deleteVolume(ctx context.Context, name string, v storage.Volume, pins storage.Pins) error {
	st := s.storage()
	units, err := node.Generate(st.StorageDir(), cryptsetupPath)
	if err != nil {
		return failed(connect.CodeInternal, "%v", err)
	}
	pin, ok := pins.Disks[v.Disk]
	uuid := pin.Partitions[name]
	if !ok || uuid == "" {
		return failed(connect.CodeFailedPrecondition, "volume %s has no recorded partition", name)
	}
	var dev, want string
	if v.Disk == storage.SystemDisk {
		dev = filepath.Join(s.Paths.BootPartitions, v.Label)
		boot, err := s.Host.ResolvePath(s.Paths.BootDisk)
		if err != nil {
			return failed(connect.CodeFailedPrecondition, "find the boot disk: %v", err)
		}
		want = boot.Name
	} else {
		dev = "/dev/disk/by-partuuid/" + uuid
		disk, found, err := s.Host.Find(pin.Identity)
		if err != nil || !found {
			return failed(connect.CodeFailedPrecondition, "the pinned disk of volume %s is missing", name)
		}
		want = disk.Name
	}
	disk, number, err := s.Host.PartitionOf(dev)
	if err != nil {
		return failed(connect.CodeFailedPrecondition, "find the partition of volume %s: %v", name, err)
	}
	if disk != want {
		return failed(connect.CodeFailedPrecondition, "the partition of volume %s is on %s, not on its pinned disk %s; refusing to touch it", name, disk, want)
	}
	if err := s.stopUnits(ctx, reverse(units.Of(name))); err != nil {
		return err
	}
	if _, err := s.Run.Run(ctx, "wipefs", "--all", dev); err != nil {
		return failed(connect.CodeInternal, "wipe volume %s: %v", name, err)
	}
	if _, err := s.Run.Run(ctx, "sfdisk", "--delete", "/dev/"+disk, strconv.Itoa(number)); err != nil {
		return failed(connect.CodeInternal, "delete the partition of volume %s: %v", name, err)
	}
	delete(pin.Partitions, name)
	pins.Disks[v.Disk] = pin
	if err := storage.WritePins(filepath.Join(st.StorageDir(), "disks.json"), pins); err != nil {
		return failed(connect.CodeInternal, "%v", err)
	}
	return nil
}

func reverse(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}
