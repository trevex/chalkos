package chalkd

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

// cryptsetupPath only names the generator's units here; systemd runs the generator itself.
const cryptsetupPath = "systemd-cryptsetup"

// delivered is an identity as it arrives, with what chalkd reads from it.
type delivered struct {
	data       []byte
	section    storage.Section
	kubernetes *identity.Kubernetes
}

func parseIdentity(data string) (delivered, error) {
	var id identity.Identity
	if err := json.Unmarshal([]byte(data), &id); err != nil {
		return delivered{}, failed(connect.CodeInvalidArgument, "parse the identity: %v", err)
	}
	if _, ok := id.Storage.Disks[storage.SystemDisk]; !ok {
		return delivered{}, failed(connect.CodeInvalidArgument, "the identity's storage section has no system disk")
	}
	if err := id.Storage.Validate(); err != nil {
		return delivered{}, failed(connect.CodeInvalidArgument, "%v", err)
	}
	// The config loader would refuse it at every boot.
	if err := id.Time.Check(); err != nil {
		return delivered{}, failed(connect.CodeInvalidArgument, "the identity's %v", err)
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

// kubernetesShare validates a delivered share for the node the identity names, a node CA in it
// against the OS CAs osCA, and returns it as STATE keeps it, in its canonical encoding; nil when
// none was delivered. cluster is the one the config of the same request gives, nil without
// config, when the node's cluster file is read instead.
func (s *Server) kubernetesShare(d delivered, data []byte, osCA string, cluster *k8s.Cluster) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if s.Kubernetes == nil {
		return nil, failed(connect.CodeInvalidArgument, "the node's image has no Kubernetes, so it takes no Kubernetes share")
	}
	share, err := kpki.ParseShare(data)
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "%v", err)
	}
	if cluster == nil {
		c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster)
		if err != nil {
			return nil, failed(connect.CodeInvalidArgument, "the node has no Kubernetes cluster: %v", err)
		}
		cluster = &c
	}
	c := cluster
	if share.Kind != c.Kind {
		return nil, failed(connect.CodeInvalidArgument, "the share is for a %s node, but the node's Node document gives it the type %s", share.Kind, c.Kind)
	}
	var nodeName string
	if d.kubernetes != nil {
		nodeName = d.kubernetes.NodeName
	}
	// A node must never run its kubelet with a certificate issued for another node.
	if err := share.ValidateFor(nodeName); err != nil {
		return nil, failed(connect.CodeInvalidArgument, "%v", err)
	}
	// Certificates of another node CA would never verify against the node's OS CA.
	if share.NodeCA != nil {
		if err := pki.ValidateNodeCA(*share.NodeCA, osCA); err != nil {
			return nil, failed(connect.CodeInvalidArgument, "the share's node CA: %v", err)
		}
	}
	canonical, err := share.Encode()
	if err != nil {
		return nil, failed(connect.CodeInternal, "encode the Kubernetes share: %v", err)
	}
	return canonical, nil
}

// elsewhere adds to err, a refusal of config whose storage section is section, the images whose
// config slots name a volume the machine records and section leaves out, when the running
// image's config never named it either: the config of one of them added it, with an upgrade or
// apply-config --staged, and naming the volume keeps it. The slot of the image writing, the one
// being written, is not named: its config is the one being replaced. The caller holds s.mu.
func (s *Server) elsewhere(err error, section storage.Section, writing []byte) error {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err
	}
	recorded, _, rerr := s.recorded()
	if rerr != nil {
		return err
	}
	_, running, found, rerr := s.runningSlot()
	if rerr != nil || !found {
		return err
	}
	ran, rerr := parseIdentity(string(running.Identity))
	if rerr != nil {
		return err
	}
	var notes []string
	for _, name := range sortedNames(recorded.Volumes) {
		if name == storage.VarVolume || hasVolume(section, name) || hasVolume(ran.section, name) {
			continue
		}
		if others := s.slotsNaming(name, writing); len(others) > 0 {
			notes = append(notes, fmt.Sprintf("the config of the image %s names volume %s", strings.Join(others, " and "), name))
		}
	}
	if len(notes) == 0 {
		return err
	}
	return failed(ce.Code(), "%s; %s", ce.Message(), strings.Join(notes, "; "))
}

// slotsNaming lists, by the start of their root hashes, the images other than the running one and
// writing whose config slots name the volume.
func (s *Server) slotsNaming(name string, writing []byte) []string {
	runningHash, err := s.runningHash()
	if err != nil {
		return nil
	}
	hashes, err := store.Slots(s.Loader.Slots)
	if err != nil {
		return nil
	}
	var others []string
	for _, h := range hashes {
		if bytes.Equal(h, runningHash) || bytes.Equal(h, writing) {
			continue
		}
		held, found, err := store.Read(store.Slot(s.Loader.Slots, h))
		if err != nil || !found {
			continue
		}
		if d, err := parseIdentity(string(held.Identity)); err == nil && hasVolume(d.section, name) {
			others = append(others, fmt.Sprintf("%x", h[:6]))
		}
	}
	return others
}

func hasVolume(section storage.Section, name string) bool {
	_, ok := section.Volumes[name]
	return ok
}

// applyRequest is an ApplyConfig request once its config is translated: the identity the node
// runs from now on, empty to keep its own; the config set it keeps and the schemas that set was
// checked against; and the rest of the request.
type applyRequest struct {
	identity string
	next     store.Stored
	schemas  *schema.Schemas
	msg      *nodev1.ApplyConfigRequest
}

func (s *Server) ApplyConfig(ctx context.Context, req *connect.Request[nodev1.ApplyConfigRequest]) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// An upgrade writes its image's slot, checked against the machine, before the image is
	// staged, so a machine change in between would escape checkStaged. TryLock never waits: the
	// upgrade holds upgrading while it takes s.mu.
	if req.Msg.Config != "" {
		if !s.upgrading.TryLock() {
			return nil, failed(connect.CodeAborted, "an upgrade is running, which writes the node's config too; apply the config once it ended")
		}
		defer s.upgrading.Unlock()
	}
	if req.Msg.Staged {
		return s.applyStaged(ctx, req.Msg)
	}
	r := applyRequest{msg: req.Msg}
	if req.Msg.Config != "" {
		name, err := s.name()
		if err != nil {
			return nil, err
		}
		t, schemas, err := s.runningTarget()
		if err != nil {
			return nil, err
		}
		c, err := configure(name, req.Msg.Config, schemas, t)
		if err != nil {
			return nil, err
		}
		r.identity, r.next, r.schemas = string(c.Identity), c, schemas
	}
	return s.apply(ctx, r)
}

// apply delivers an ApplyConfig request whose config is translated; the caller holds s.mu.
func (s *Server) apply(ctx context.Context, r applyRequest) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	m := r.msg
	// Without config the node keeps its own, which the share is checked against.
	keep := r.identity == ""
	if keep && len(m.KubernetesShare) == 0 && len(m.NodeCertificate) == 0 && len(m.Trust) == 0 {
		return nil, failed(connect.CodeInvalidArgument, "the request delivers no config, share, node certificate or OS CAs")
	}
	// A node certificate is checked against the OS CAs the node trusts when it arrives.
	if len(m.Trust) > 0 && len(m.NodeCertificate) > 0 {
		return nil, failed(connect.CodeInvalidArgument, "deliver the OS CAs and a node certificate one after the other")
	}
	osCA, err := s.trustFor(ctx, m.Trust)
	if err != nil {
		return nil, err
	}
	slot, prev, found, err := s.runningSlot()
	if err != nil {
		return nil, err
	}
	identity := r.identity
	if keep {
		if !found {
			return nil, failed(connect.CodeFailedPrecondition, "STATE holds no config for the running image, so the node has no identity to keep; deliver config with chalkctl apply-config")
		}
		identity = string(prev.Identity)
	}
	d, err := parseIdentity(identity)
	if err != nil {
		return nil, err
	}
	// The rules and the consumers' rules see the node by the name its node certificate gives.
	name, err := s.name()
	if err != nil {
		return nil, err
	}
	// The cluster the config gives, checked before anything changes; nil on an image without
	// Kubernetes or without new config.
	var cluster *k8s.Cluster
	// The delivery's facts, loaded once for the running slot's check and the staged slot's.
	var delivered *facts.Loader
	if !keep {
		if s.Kubernetes != nil {
			if cluster, err = s.Loader.Cluster(r.next.Documents); err != nil {
				return nil, failed(connect.CodeInvalidArgument, "the config: %v", err)
			}
		}
		// The image's rules compare the config with the running slot's and the storage STATE
		// records, and read the node's facts, which the delivery loads once for this check and the
		// staged slot's, before anything changes: a missing or mistyped fallback secret is refused
		// before the node trusts the delivered OS CAs.
		set, err := compileRules(r.schemas, true)
		if err != nil {
			return nil, err
		}
		if delivered, err = s.factsFor(ctx, r.next.Documents, prev.Documents, m.FallbackSecret); err != nil {
			return nil, err
		}
		if err := s.checkRules(set, name, r.next.Documents, prev.Documents, delivered); err != nil {
			return nil, s.elsewhere(err, d.section, nil)
		}
	}
	share, err := s.kubernetesShare(d, m.KubernetesShare, osCA, cluster)
	if err != nil {
		return nil, err
	}
	if err := s.checkNodeCertificate(m.NodeCertificate, m.NodeKey); err != nil {
		return nil, err
	}
	// The changes the config makes to the machine's storage, which the staged image boots onto
	// too, are checked against that image's config before the node trusts new OS CAs or changes
	// anything else, so a refused request changes nothing.
	var mc machineChange
	if !keep {
		if mc, err = s.machineChanges(d.section); err != nil {
			return nil, err
		}
		if len(mc.changes) > 0 {
			if err := s.checkStaged(r.schemas, name, r.next.Documents, d.section, delivered); err != nil {
				return nil, err
			}
		}
	}
	// The consumers' rules compare the delivery with the running slot, and the facts as they are
	// once it is applied with the facts as they were, which load before anything changes.
	next := prev.Documents
	if !keep {
		next = r.next.Documents
	}
	fx, err := s.effectsOf(ctx, name, next, prev.Documents, r.schemas, m.FallbackSecret)
	if err != nil {
		return nil, err
	}
	if len(m.Trust) > 0 {
		if share == nil {
			// A control plane keeps renewing node certificates with the node CA it holds.
			if err := s.checkKeptNodeCA(osCA); err != nil {
				s.refused(fx)
				return nil, err
			}
		}
		if err := s.Certificate.ReplaceTrust(osCA, peerOf(ctx)...); err != nil {
			if isRefusedTrust(err) {
				s.refused(fx)
				return nil, failed(connect.CodeInvalidArgument, "%v", err)
			}
			return nil, failed(connect.CodeInternal, "%v", err)
		}
		log.Printf("trusting %s from now on", count(strings.Count(osCA, "-----BEGIN CERTIFICATE-----"), "OS CA"))
	}
	var changes []storage.Change
	if !keep {
		changes = mc.changes
		if err := s.applyMachine(ctx, mc, m.FallbackSecret); err != nil {
			return nil, err
		}
		if err := s.applyIdentity(d.data); err != nil {
			return nil, err
		}
		if cluster != nil {
			if err := s.Loader.WriteCluster(cluster); err != nil {
				s.failedPartWay(ctx, fx, m.FallbackSecret)
				return nil, failed(connect.CodeInternal, "write the Kubernetes cluster: %v", err)
			}
		}
	}
	if len(m.NodeCertificate) > 0 {
		if err := s.Certificate.Replace(string(m.NodeCertificate), string(m.NodeKey), time.Now()); err != nil {
			s.failedPartWay(ctx, fx, m.FallbackSecret)
			return nil, failed(connect.CodeInvalidArgument, "%v", err)
		}
		log.Printf("serving a delivered node certificate; it expires %s", s.Certificate.Current().Leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if share != nil {
		if err := install.WriteShare(s.Paths.StateDir, share); err != nil {
			s.failedPartWay(ctx, fx, m.FallbackSecret)
			return nil, failed(connect.CodeInternal, "record the Kubernetes share: %v", err)
		}
	}
	// The slot comes last, documents and identity in one rename: until it is written, STATE holds
	// the identity and the set the node ran before, which a boot applies whole, and a retry applies
	// the identity and the consumers' config again. Until a delivery is applied whole, its
	// consumers' rules compare with the baseline effectsOf recorded, so a retry restarts what they
	// ask for whatever the failed attempt wrote. storage.json, written before, is the machine's
	// rather than an image's: applyStorage records the section once its additive changes are made,
	// so a retry finds them recorded and makes none twice.
	if !keep {
		r.next.Identity = d.data
		if err := store.Write(slot, r.next); err != nil {
			s.failedPartWay(ctx, fx, m.FallbackSecret)
			return nil, failed(connect.CodeInternal, "store the config: %v", err)
		}
	}
	restarted, warnings, err := s.consume(ctx, fx, !keep, m.FallbackSecret)
	if err != nil {
		return nil, err
	}
	// Deliveries without config, as a rotation's, renew nothing: forced renewals would
	// restart the control plane's pods on top of the restarts the rotation waits for. They come
	// after the loops started again, which would end a renewal under way.
	if s.RenewOnApplyConfig && !keep {
		s.forceRenewals()
	}
	resp := &nodev1.ApplyConfigResponse{RestartedUnits: restarted, Warnings: warnings}
	for _, c := range changes {
		resp.Changes = append(resp.Changes, &nodev1.StorageChange{Volume: c.Volume, Reason: c.Reason})
	}
	return connect.NewResponse(resp), nil
}

// forceRenewals renews the node certificate and, on a control plane whose loop runs, its
// certificates at once.
func (s *Server) forceRenewals() {
	if s.Renewal != nil {
		log.Print("renewing the node certificate, as this test image does after each config")
		s.Renewal.Force()
	}
	if s.Kubernetes != nil {
		s.Kubernetes.mu.Lock()
		leaves := s.Kubernetes.leaves
		s.Kubernetes.mu.Unlock()
		if leaves != nil {
			log.Print("renewing the control plane's certificates, as this test image does after each config")
			leaves.Force()
		}
	}
}

// machineChange is what a config slot's storage section adds to the machine's: storage.json, the
// storage as applied, and the changes the section makes to it.
type machineChange struct {
	recorded, section storage.Section
	changes           []storage.Change
}

// machineChanges compares a slot's storage section with the machine's, as every write of a slot
// does, whichever image the slot is for: the machine has one storage, which a slot's config may
// grow but not undo. The image's rules refused the changes a node does not make before. A slot
// whose section lacks a volume the machine has, as the slot of an image that ran before an upgrade
// or apply-config --staged added it, is compared so only when it is written again. Every boot sets
// up each volume storage.json records, so that image mounts the volume too, and nothing at boot
// removes one. The caller holds s.mu.
func (s *Server) machineChanges(section storage.Section) (machineChange, error) {
	recorded, _, err := s.recorded()
	if err != nil {
		return machineChange{}, err
	}
	return machineChange{recorded: recorded, section: section, changes: storage.Changes(recorded, section)}, nil
}

// applyMachine makes the additive changes a slot's section makes to the machine's storage, which
// records them in storage.json; secret is the fallback secret new encrypted volumes are enrolled
// with, which the rules checked. The caller holds s.mu.
func (s *Server) applyMachine(ctx context.Context, mc machineChange, secret string) error {
	if len(mc.changes) == 0 {
		return nil
	}
	return s.applyStorage(ctx, mc.recorded, mc.section, secret, mc.changes)
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

func (s *Server) stopUnits(ctx context.Context, units []string) error {
	if len(units) == 0 {
		return nil
	}
	if _, err := s.Run.Run(ctx, "systemctl", append([]string{"stop"}, units...)...); err != nil {
		return failed(connect.CodeFailedPrecondition, "stop %s: %v", strings.Join(units, ", "), err)
	}
	return nil
}

// applyIdentity applies the identity data: the networkd units and the time servers it gives,
// which networkd and chrony read again as their consumers' rules say. The running slot records
// it, with the config it came from, once the delivery is applied.
func (s *Server) applyIdentity(data []byte) error {
	if err := s.Loader.Apply(data); err != nil {
		return failed(connect.CodeInternal, "apply the identity: %v", err)
	}
	return nil
}

func (s *Server) ResetVolume(ctx context.Context, req *connect.Request[nodev1.ResetVolumeRequest]) (*connect.Response[nodev1.ResetVolumeResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := req.Msg
	if m.Config == "" {
		return nil, failed(connect.CodeInvalidArgument, "the request delivers no config, which defines the volume")
	}
	name, err := s.name()
	if err != nil {
		return nil, err
	}
	t, schemas, err := s.runningTarget()
	if err != nil {
		return nil, err
	}
	c, err := configure(name, m.Config, schemas, t)
	if err != nil {
		return nil, err
	}
	if err := s.resetVolume(ctx, name, m.Volume, c, schemas, m.FallbackSecret); err != nil {
		return nil, err
	}
	return connect.NewResponse(&nodev1.ResetVolumeResponse{}), nil
}

// resetVolume wipes the volume name and creates it again as next, the config translated for the
// node called node on the running image, whose schemas are given, defines it; the caller holds
// s.mu. The image's storage rules decide what a reset carries out: once deleted, the volume is
// created again like a new one, so they are checked against the recorded section without it, which
// they must then accept whole. They check the disk the volume lives on and the fallback secret it
// is enrolled with too.
func (s *Server) resetVolume(ctx context.Context, node, name string, next store.Stored, schemas *schema.Schemas, fallback string) error {
	d, err := parseIdentity(string(next.Identity))
	if err != nil {
		return err
	}
	recorded, pins, err := s.recorded()
	if err != nil {
		return err
	}
	v, ok := recorded.Volumes[name]
	if !ok {
		return failed(connect.CodeNotFound, "the node has no volume %s", name)
	}
	if name == storage.VarVolume {
		return failed(connect.CodeFailedPrecondition, "VAR holds the running node's data and cannot be reset while it runs")
	}
	nv, ok := d.section.Volumes[name]
	if !ok {
		return failed(connect.CodeFailedPrecondition, "the config no longer has volume %s; a reset recreates a volume, it does not remove one", name)
	}
	if nv.Disk != v.Disk {
		return failed(connect.CodeFailedPrecondition, "volume %s moves from disk %s to %s; a reset recreates a volume on its own disk only", name, v.Disk, nv.Disk)
	}
	without := recorded
	without.Volumes = map[string]storage.Volume{}
	for n, vol := range recorded.Volumes {
		if n != name {
			without.Volumes[n] = vol
		}
	}
	set, err := compileRules(schemas, true)
	if err != nil {
		return err
	}
	_, running, _, err := s.runningSlot()
	if err != nil {
		return err
	}
	f, err := s.factsFor(ctx, next.Documents, running.Documents, fallback)
	if err != nil {
		return err
	}
	f.Override("recorded", jsonValue(without))
	failures, err := evaluate(set.Of(config.StorageAPI), node, next.Documents, running.Documents, f)
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		return failed(connect.CodeFailedPrecondition, "a reset of volume %s does not carry out the config: %s", name, rules.Refusal(failures, node))
	}

	if err := s.deleteVolume(ctx, name, v, pins); err != nil {
		return err
	}
	if err := s.applyStorage(ctx, without, d.section, fallback, storage.Changes(without, d.section)); err != nil {
		return err
	}
	log.Printf("volume %s reset", name)
	return nil
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
	if _, err := s.Run.Run(ctx, "sfdisk", "--lock", "--delete", "/dev/"+disk, strconv.Itoa(part.Number)); err != nil {
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

// trustFor returns the OS CAs the node is to trust after the request: those delivered, checked
// as NodeCertificate.ReplaceTrust does, or else its own.
func (s *Server) trustFor(ctx context.Context, delivered []byte) (string, error) {
	if s.Certificate == nil {
		if len(delivered) > 0 {
			return "", failed(connect.CodeFailedPrecondition, "the node serves no node certificate the OS CAs would verify")
		}
		return string(s.readOSCA()), nil
	}
	if len(delivered) == 0 {
		return s.Certificate.OSCA(), nil
	}
	if err := s.Certificate.CheckTrust(string(delivered), peerOf(ctx)...); err != nil {
		return "", failed(connect.CodeInvalidArgument, "%v", err)
	}
	return pki.Bundle(string(delivered)), nil
}

// checkKeptNodeCA refuses OS CAs that the node CA a control plane holds does not chain to.
func (s *Server) checkKeptNodeCA(osCA string) error {
	if s.Kubernetes == nil {
		return nil
	}
	share, err := knode.ReadShare(s.Kubernetes.Paths)
	if errors.Is(err, knode.ErrNoShare) {
		return nil
	}
	if err != nil {
		return failed(connect.CodeInternal, "%v", err)
	}
	if share.NodeCA != nil {
		if err := pki.ValidateNodeCA(*share.NodeCA, osCA); err != nil {
			return failed(connect.CodeInvalidArgument, "the OS CAs do not verify the node's node CA, which it renews node certificates with: %v", err)
		}
	}
	return nil
}

// peerOf returns the verified client certificate of the call, if there is one.
func peerOf(ctx context.Context) []*x509.Certificate {
	if peer, ok := ctx.Value(peerKey{}).(*x509.Certificate); ok && peer != nil {
		return []*x509.Certificate{peer}
	}
	return nil
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
