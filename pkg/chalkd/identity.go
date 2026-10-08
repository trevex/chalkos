package chalkd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

// cryptsetupPath only names the generator's units here; systemd runs the generator itself.
const cryptsetupPath = "systemd-cryptsetup"

// delivered is an identity as it arrives, with what chalkd reads from it.
type delivered struct {
	data       []byte
	section    storage.Section
	kubernetes *manifest.KubernetesIdentity
}

func parseIdentity(data string) (delivered, error) {
	var id manifest.Identity
	if err := json.Unmarshal([]byte(data), &id); err != nil {
		return delivered{}, failed(connect.CodeInvalidArgument, "parse the identity: %v", err)
	}
	if _, ok := id.Storage.Disks[storage.SystemDisk]; !ok {
		return delivered{}, failed(connect.CodeInvalidArgument, "the identity's storage section has no system disk")
	}
	if err := id.Storage.Validate(); err != nil {
		return delivered{}, failed(connect.CodeInvalidArgument, "%v", err)
	}
	// A node that cannot read how to pick its address would run no kubelet.
	if k := id.Kubernetes; k != nil {
		if err := k.CheckNodeIPs(); err != nil {
			return delivered{}, failed(connect.CodeInvalidArgument, "the identity's %v", err)
		}
		if _, err := nodeip.ParseFilter(k.ValidSubnets); err != nil {
			return delivered{}, failed(connect.CodeInvalidArgument, "%v", err)
		}
	}
	return delivered{data: []byte(data), section: id.Storage, kubernetes: id.Kubernetes}, nil
}

// kubernetesShare validates a delivered share for the node the identity names and returns it as
// STATE keeps it, in its canonical encoding; nil when none was delivered.
func (s *Server) kubernetesShare(d delivered, data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if s.Kubernetes == nil {
		return nil, failed(connect.CodeInvalidArgument, "the node's role has no Kubernetes, so it takes no Kubernetes share")
	}
	share, err := kpki.ParseShare(data)
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "%v", err)
	}
	c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster)
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "the node's image has no Kubernetes: %v", err)
	}
	if share.Kind != c.Kind {
		return nil, failed(connect.CodeInvalidArgument, "the share is for a %s node, but the node's image is for %s nodes", share.Kind, c.Kind)
	}
	var nodeName string
	if d.kubernetes != nil {
		nodeName = d.kubernetes.NodeName
	}
	// A node must never run its kubelet with a certificate issued for another node.
	if err := share.ValidateFor(nodeName); err != nil {
		return nil, failed(connect.CodeInvalidArgument, "%v", err)
	}
	if share.NodeCA != nil {
		osCA, err := os.ReadFile(filepath.Join(s.Paths.StateDir, "chalkd", CAFile))
		if err != nil {
			return nil, failed(connect.CodeInternal, "read the OS CA: %v", err)
		}
		// Certificates of another node CA would never verify against the node's OS CA.
		if err := pki.ValidateNodeCA(*share.NodeCA, pki.CertKey{Certificate: string(osCA)}); err != nil {
			return nil, failed(connect.CodeInvalidArgument, "the share's node CA: %v", err)
		}
	}
	canonical, err := share.Encode()
	if err != nil {
		return nil, failed(connect.CodeInternal, "encode the Kubernetes share: %v", err)
	}
	return canonical, nil
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
	m := req.Msg
	// Without an identity the node keeps its own, which the share is checked against.
	keep := m.Identity == ""
	if keep && len(m.KubernetesShare) == 0 && len(m.NodeCertificate) == 0 {
		return nil, failed(connect.CodeInvalidArgument, "the request delivers no identity, share or node certificate")
	}
	identity := m.Identity
	if keep {
		data, err := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json"))
		if err != nil {
			return nil, failed(connect.CodeInternal, "read the identity: %v", err)
		}
		identity = string(data)
	}
	d, err := parseIdentity(identity)
	if err != nil {
		return nil, err
	}
	share, err := s.kubernetesShare(d, m.KubernetesShare)
	if err != nil {
		return nil, err
	}
	if err := s.checkNodeCertificate(m.NodeCertificate, m.NodeKey); err != nil {
		return nil, err
	}
	var changes []storage.Change
	var restarted []string
	old := d.data
	if !keep {
		recorded, pins, err := s.recorded()
		if err != nil {
			return nil, err
		}
		changes = s.classify(recorded, d.section, pins)
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
			if err := s.verifyFallback(ctx, recorded, d.section, pins, m.FallbackSecret); err != nil {
				return nil, err
			}
			if err := s.applyStorage(ctx, recorded, d.section, m.FallbackSecret, changes); err != nil {
				return nil, err
			}
		}
		if restarted, old, err = s.applyIdentity(ctx, d.data); err != nil {
			return nil, err
		}
	}
	if len(m.NodeCertificate) > 0 {
		if err := s.Certificate.Replace(string(m.NodeCertificate), string(m.NodeKey), time.Now()); err != nil {
			return nil, failed(connect.CodeInvalidArgument, "%v", err)
		}
		log.Printf("serving a delivered node certificate; it expires %s", s.Certificate.Current().Leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if s.Kubernetes != nil {
		units, err := s.applyKubernetes(ctx, old, d.data, share)
		if err != nil {
			return nil, err
		}
		restarted = append(restarted, units...)
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
				devices[name] = storage.PartUUIDPath(pins.Disks[v.Disk].Partitions[name])
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
			devices = append(devices, storage.PartUUIDPath(uuid))
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
// keys whose values changed. It returns them and the identity it replaced.
func (s *Server) applyIdentity(ctx context.Context, data []byte) ([]string, []byte, error) {
	path := filepath.Join(s.Paths.StateDir, "identity.json")
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, failed(connect.CodeInternal, "%v", err)
	}
	if err := install.WriteFile(path, data, 0o600); err != nil {
		return nil, nil, failed(connect.CodeInternal, "record the identity: %v", err)
	}
	if err := s.Identity.Apply(data); err != nil {
		return nil, nil, failed(connect.CodeInternal, "apply the identity: %v", err)
	}
	if _, err := s.Run.Run(ctx, "networkctl", "reload"); err != nil {
		return nil, nil, failed(connect.CodeInternal, "%v", err)
	}
	consumers, err := identity.ReadConsumers(s.Identity.Consumers)
	if err != nil {
		return nil, nil, failed(connect.CodeInternal, "%v", err)
	}
	units, err := identity.Restarts(consumers, old, data)
	if err != nil {
		return nil, nil, failed(connect.CodeInternal, "%v", err)
	}
	for _, unit := range units {
		if _, err := s.Run.Run(ctx, "systemctl", "try-restart", unit); err != nil {
			return nil, nil, failed(connect.CodeInternal, "restart %s: %v", unit, err)
		}
	}
	return units, old, nil
}

// applyKubernetes records a delivered share and, when it or the node's Kubernetes identity
// changed, prepares the node's Kubernetes files again and restarts what reads them. It returns
// the restarted units.
func (s *Server) applyKubernetes(ctx context.Context, old, data, share []byte) ([]string, error) {
	before, errBefore := k8s.ParseNode(old)
	after, errAfter := k8s.ParseNode(data)
	// An identity that does not parse counts as changed, unless it is the same.
	changed := !bytes.Equal(old, data) && (errBefore != nil || errAfter != nil || !reflect.DeepEqual(before, after))
	if share == nil && !changed {
		return nil, nil
	}
	if share != nil {
		nodeCAOnly := !changed && s.onlyNodeCAChanges(share)
		if err := install.WriteShare(s.Paths.StateDir, share); err != nil {
			return nil, failed(connect.CodeInternal, "record the Kubernetes share: %v", err)
		}
		// The node CA is read from the share whenever a node certificate is issued; nothing else
		// uses it, so a new one restarts nothing.
		if nodeCAOnly {
			log.Print("recorded the delivered share, which differs at most in the node CA; nothing restarts")
			return nil, nil
		}
	}
	// The node's address, the firewall's VXLAN rule, the kubelet's credentials and flags and the
	// control plane's certificates come from the share and the identity.
	units := []string{"chalkos-kubernetes.service", "kubelet.service"}
	if _, err := s.Run.Run(ctx, "systemctl", append([]string{"restart"}, units...)...); err != nil {
		if problem, perr := preparation(s.Kubernetes.Paths); perr == nil && problem != "" && problem != preparing {
			return nil, failed(connect.CodeFailedPrecondition, "the node runs no kubelet: %s", problem)
		}
		return nil, failed(connect.CodeInternal, "restart %s: %v", strings.Join(units, " "), err)
	}
	if share != nil {
		s.Kubernetes.Reload()
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
		link = storage.PartUUIDPath(uuid)
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

// checkNodeCertificate refuses a delivered node certificate before anything changes, as
// NodeCertificate.Replace would; none delivered passes.
func (s *Server) checkNodeCertificate(chain, key []byte) error {
	if len(chain) == 0 && len(key) == 0 {
		return nil
	}
	if s.Certificate == nil {
		return failed(connect.CodeFailedPrecondition, "the node serves no node certificate to replace")
	}
	if err := s.Certificate.Check(string(chain), string(key), time.Now()); err != nil {
		return failed(connect.CodeInvalidArgument, "%v", err)
	}
	return nil
}

// onlyNodeCAChanges reports whether a delivered share differs from the node's in the node CA
// alone, as after chalkctl node-ca rotate.
func (s *Server) onlyNodeCAChanges(delivered []byte) bool {
	current, err := knode.ReadShare(s.Kubernetes.Paths)
	if err != nil {
		return false
	}
	next, err := kpki.ParseShare(delivered)
	if err != nil {
		return false
	}
	current.NodeCA, next.NodeCA = nil, nil
	a, errA := current.Encode()
	b, errB := next.Encode()
	return errA == nil && errB == nil && bytes.Equal(a, b)
}
