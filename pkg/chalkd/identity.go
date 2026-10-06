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
	"sort"
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
	if err := id.Storage.Validate(); err != nil {
		return delivered{}, failed(connect.CodeInvalidArgument, "%v", err)
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
		if err := s.verifyFallback(ctx, recorded, d.section, pins, req.Msg.FallbackSecret); err != nil {
			return nil, err
		}
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
	if err := checkFallback(recorded, section, secret); err != nil {
		return err
	}
	added := map[string]bool{}
	for name := range section.Volumes {
		if _, ok := recorded.Volumes[name]; !ok {
			added[name] = true
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

// checkFallback refuses before anything changes when a volume the section adds to recorded is
// encrypted and the fallback secret it would be enrolled with is missing.
func checkFallback(recorded, section storage.Section, secret string) error {
	if secret != "" {
		return nil
	}
	if name := enrolled(recorded, section); name != "" {
		return failed(connect.CodeInvalidArgument, "volume %s is encrypted and the node's fallback is %s, but no fallback secret was sent", name, section.Fallback)
	}
	return nil
}

// enrolled returns the first volume the section adds to recorded that gets the fallback
// keyslot, or "" when there is none.
func enrolled(recorded, section storage.Section) string {
	if section.Fallback == storage.FallbackNone {
		return ""
	}
	for _, name := range sortedNames(section.Volumes) {
		if _, ok := recorded.Volumes[name]; !ok && section.Volumes[name].Encryption == storage.EncryptionTPM2 {
			return name
		}
	}
	return ""
}

// verifyFallback refuses before anything changes when the fallback secret about to be enrolled
// on the volumes the section adds to kept differs from the node's existing one, so a mistyped
// password cannot leave a volume whose fallback differs from the others'. The secret is checked
// against the password keyslot of STATE, else VAR, else another encrypted volume of kept; a
// node with no such keyslot has nothing to check against.
func (s *Server) verifyFallback(ctx context.Context, kept, section storage.Section, pins storage.Pins, secret string) error {
	if err := checkFallback(kept, section, secret); err != nil {
		return err
	}
	if enrolled(kept, section) == "" || kept.Fallback == storage.FallbackNone {
		return nil
	}
	for _, dev := range s.encryptedDevices(kept, pins) {
		dump, err := s.Run.Run(ctx, "cryptsetup", "luksDump", "--dump-json-metadata", dev)
		if err != nil {
			return failed(connect.CodeFailedPrecondition, "read the keyslots of %s to check the fallback secret: %v", dev, err)
		}
		slots, err := passwordSlots(dump)
		if err != nil {
			return failed(connect.CodeFailedPrecondition, "read the keyslots of %s to check the fallback secret: %v", dev, err)
		}
		if len(slots) == 0 {
			continue
		}
		for _, slot := range slots {
			// The secret goes on standard input: arguments are visible to every process, and
			// cryptsetup takes a key file from stdin as it is, without trimming a newline.
			_, err := s.Run.RunWithInput(ctx, []byte(secret), "cryptsetup", "open", "--test-passphrase", "--key-slot="+slot, "--key-file=-", dev)
			var te *node.ToolError
			switch {
			case err == nil:
				return nil
			case errors.As(err, &te) && te.Code == 2:
				// cryptsetup's status for a passphrase no keyslot accepts.
				continue
			default:
				return failed(connect.CodeFailedPrecondition, "check the fallback secret against %s: %v", dev, err)
			}
		}
		return failed(connect.CodeInvalidArgument, "the fallback secret does not match the node's existing fallback keyslot")
	}
	return nil
}

// encryptedDevices lists the LUKS devices of kept that carry the fallback keyslot, in the order
// the fallback secret is checked against them.
func (s *Server) encryptedDevices(kept storage.Section, pins storage.Pins) []string {
	var devices []string
	if kept.Encryption == storage.EncryptionTPM2 {
		devices = append(devices, filepath.Join(s.Paths.BootPartitions, "state"))
	}
	names := []string{storage.VarVolume}
	for _, name := range sortedNames(kept.Volumes) {
		if name != storage.VarVolume {
			names = append(names, name)
		}
	}
	for _, name := range names {
		v, ok := kept.Volumes[name]
		if !ok || v.Encryption != storage.EncryptionTPM2 {
			continue
		}
		if v.Disk == storage.SystemDisk {
			devices = append(devices, filepath.Join(s.Paths.BootPartitions, v.Label))
		} else if uuid := pins.Disks[v.Disk].Partitions[name]; uuid != "" {
			devices = append(devices, "/dev/disk/by-partuuid/"+uuid)
		}
	}
	return devices
}

// passwordSlots returns the keyslots of a LUKS2 header that no token refers to, in numeric
// order. systemd-cryptenroll counts them as password slots: the fallback keyslot is one, the
// TPM2 keyslot has its token.
func passwordSlots(dump []byte) ([]string, error) {
	var header struct {
		Keyslots map[string]json.RawMessage `json:"keyslots"`
		Tokens   map[string]struct {
			Keyslots []string `json:"keyslots"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(dump, &header); err != nil {
		return nil, fmt.Errorf("parse the LUKS2 header: %w", err)
	}
	tokened := map[string]bool{}
	for _, t := range header.Tokens {
		for _, slot := range t.Keyslots {
			tokened[slot] = true
		}
	}
	var slots []int
	for slot := range header.Keyslots {
		n, err := strconv.Atoi(slot)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("the LUKS2 header has the keyslot %q", slot)
		}
		if !tokened[slot] {
			slots = append(slots, n)
		}
	}
	sort.Ints(slots)
	var out []string
	for _, n := range slots {
		out = append(out, strconv.Itoa(n))
	}
	return out, nil
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
	nv, ok := d.section.Volumes[name]
	if !ok {
		return nil, failed(connect.CodeFailedPrecondition, "the identity no longer has volume %s; a reset recreates a volume, it does not remove one", name)
	}
	if nv.Disk != v.Disk {
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
	// Once deleted, the volume is created again like a new one.
	without := recorded
	without.Volumes = map[string]storage.Volume{}
	for n, vol := range recorded.Volumes {
		if n != name {
			without.Volumes[n] = vol
		}
	}
	// Every volume created after the deletion needs the secret, so check it before deleting.
	if err := s.verifyFallback(ctx, without, d.section, pins, req.Msg.FallbackSecret); err != nil {
		return nil, err
	}

	if err := s.deleteVolume(ctx, name, v, pins); err != nil {
		return nil, err
	}
	changes := storage.Classify(without, d.section)
	if err := s.applyStorage(ctx, without, d.section, req.Msg.FallbackSecret, changes); err != nil {
		return nil, err
	}
	log.Printf("volume %s reset", name)
	return connect.NewResponse(&nodev1.ResetVolumeResponse{}), nil
}

// deleteVolume stops a volume's units, wipes its partition and deletes it, after checking the
// partition lies on the disk the volume's disk name is pinned to and carries the PARTUUID
// recorded for the volume. The link is resolved once; the commands act on the partition that
// was checked.
func (s *Server) deleteVolume(ctx context.Context, name string, v storage.Volume, pins storage.Pins) error {
	st := s.storage()
	units, err := node.Generate(st.StorageDir(), cryptsetupPath)
	if err != nil {
		return failed(connect.CodeInternal, "%v", err)
	}
	pin, ok := pins.Disks[v.Disk]
	uuid := strings.ToLower(pin.Partitions[name])
	if !ok || uuid == "" {
		return failed(connect.CodeFailedPrecondition, "volume %s has no recorded partition", name)
	}
	var link, want string
	if v.Disk == storage.SystemDisk {
		link = filepath.Join(s.Paths.BootPartitions, v.Label)
		boot, err := s.Host.ResolvePath(s.Paths.BootDisk)
		if err != nil {
			return failed(connect.CodeFailedPrecondition, "find the boot disk: %v", err)
		}
		want = boot.Name
	} else {
		link = "/dev/disk/by-partuuid/" + uuid
		disk, found, err := s.Host.Find(pin.Identity)
		if err != nil || !found {
			return failed(connect.CodeFailedPrecondition, "the pinned disk of volume %s is missing", name)
		}
		want = disk.Name
	}
	disk, part, err := s.Host.PartitionOf(link)
	if err != nil {
		return failed(connect.CodeFailedPrecondition, "find the partition of volume %s: %v", name, err)
	}
	if disk != want {
		return failed(connect.CodeFailedPrecondition, "the partition of volume %s is on %s, not on its pinned disk %s; refusing to touch it", name, disk, want)
	}
	if part.UUID != uuid {
		return failed(connect.CodeFailedPrecondition, "%s has the PARTUUID %q, not the %s recorded for volume %s; refusing to touch it", part.Device, part.UUID, uuid, name)
	}
	if err := s.stopUnits(ctx, reverse(units.Of(name))); err != nil {
		return err
	}
	if _, err := s.Run.Run(ctx, "wipefs", "--all", part.Device); err != nil {
		return failed(connect.CodeInternal, "wipe volume %s: %v", name, err)
	}
	if _, err := s.Run.Run(ctx, "sfdisk", "--delete", "/dev/"+disk, strconv.Itoa(part.Number)); err != nil {
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
