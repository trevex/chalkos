// Package install turns a node in maintenance mode into an installed node: in place, when the
// role image already runs from the boot disk, or from installer media, which first writes the
// role image to a target disk. Every step can be repeated, so an interrupted install can run
// again; the node counts as installed once STATE holds the installed marker, written last.
package install

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

const (
	// typeESP is the GPT partition type of an EFI system partition.
	typeESP         = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	stateLabel      = "state"
	installedMarker = "installed"
	stateMapperName = "state"
	contentLUKS     = "crypto_LUKS"
	contentExt4     = "ext4"
)

// Installed reports whether STATE, mounted at stateDir, holds the installed marker. Only a
// marker that does not exist means not installed; any other error is returned, so callers
// never take an unreadable STATE for an empty one.
func Installed(stateDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(stateDir, installedMarker))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("check whether the node is installed: %w", err)
	}
}

// Request is what both flows install.
type Request struct {
	// Identity is the node's identity as delivered (JSON); STATE keeps it as it is.
	Identity []byte
	// Section is the identity's storage section.
	Section storage.Section
	// Kubernetes is the identity's Kubernetes section; nil on a node of a role without Kubernetes.
	Kubernetes *manifest.KubernetesIdentity
	// NodeCertificate, the node certificate followed by the node CA's, and NodeKey are what chalkd
	// serves once the node is installed; CA holds the OS CAs, which issue the client certificates
	// it accepts and the node CA. All are PEM.
	NodeCertificate, NodeKey, CA []byte
	// FallbackSecret is enrolled as the second keyslot of every encrypted volume.
	FallbackSecret string
	// KubernetesShare is the node's share of the Kubernetes secrets (JSON); empty on a node of a
	// role without Kubernetes.
	KubernetesShare []byte
}

func (r Request) validate() error {
	if len(r.Identity) == 0 {
		return errors.New("no identity")
	}
	if _, ok := r.Section.Disks[storage.SystemDisk]; !ok {
		return errors.New("the identity's storage section has no system disk")
	}
	if err := r.Section.Validate(); err != nil {
		return err
	}
	switch r.Section.Encryption {
	case storage.EncryptionTPM2, storage.EncryptionNone:
	default:
		return fmt.Errorf("the identity's storage section has the unknown encryption policy %q", r.Section.Encryption)
	}
	if r.Section.Fallback != storage.FallbackNone && r.FallbackSecret == "" {
		return fmt.Errorf("the node's fallback is %s, but no fallback secret was sent", r.Section.Fallback)
	}
	if _, err := pki.ValidateOSCABundle(string(r.CA)); err != nil {
		return fmt.Errorf("CA certificate: %w", err)
	}
	// A node whose certificate does not chain through the node CA to the OS CA could not be
	// reached once installed. The node's clock may not be set yet, so dates are not checked.
	if _, err := r.nodeFile(); err != nil {
		return err
	}
	if len(r.KubernetesShare) > 0 {
		share, err := kpki.ParseShare(r.KubernetesShare)
		if err != nil {
			return err
		}
		var nodeName string
		if r.Kubernetes != nil {
			nodeName = r.Kubernetes.NodeName
		}
		// A worker share names the node its kubelet certificate is for; a node must never run a
		// kubelet certificate issued for another node.
		if err := share.ValidateFor(nodeName); err != nil {
			return err
		}
		if share.NodeCA != nil {
			if err := pki.ValidateNodeCA(*share.NodeCA, string(r.CA)); err != nil {
				return fmt.Errorf("the share's node CA: %w", err)
			}
		}
	}
	return nil
}

// nodeFile is the file chalkd loads its node certificate from.
func (r Request) nodeFile() ([]byte, error) {
	cred, err := pki.VerifyNode(string(r.NodeCertificate), string(r.NodeKey), string(r.CA), time.Time{})
	if err != nil {
		return nil, err
	}
	return pki.NodeFile(cred, string(r.CA))
}

// Installer holds what installing works on; tests point it at temporary directories and a
// fake runner.
type Installer struct {
	Run  node.Runner
	Host storage.Host
	// StateDir is where the target's STATE is mounted while installing.
	StateDir string
	// BootDisk is udev's link to the disk the running image booted from.
	BootDisk string
	// Definitions holds the running image's repart definitions of the system region.
	Definitions string
	// WorkDir holds definitions and status while installing.
	WorkDir string
	// MountInfo is the mount table of chalkd's mount namespace.
	MountInfo string
	// OpenDisk opens a disk for writing the image, waiting for its lock until ctx is done.
	OpenDisk func(ctx context.Context, path string) (Disk, error)
	// Loader is the boot loader's path on the ESP, for the UEFI boot entry.
	Loader string
	Now    func() time.Time
}

// Default installs on the running node: in place at /state, from media at /run/chalkd/target.
func Default(inPlace bool) *Installer {
	stateDir := "/state"
	if !inPlace {
		stateDir = "/run/chalkd/target"
	}
	return &Installer{
		Run:         node.ExecRunner{},
		Host:        storage.DefaultHost(),
		StateDir:    stateDir,
		BootDisk:    "/dev/disk/chalk-boot-disk",
		Definitions: "/etc/chalkos/repart.d",
		WorkDir:     "/run/chalkd/install",
		MountInfo:   "/proc/self/mountinfo",
		OpenDisk:    openExclusive,
		Loader:      bootLoader(runtime.GOARCH),
		Now:         time.Now,
	}
}

// bootLoader is where the image's systemd-boot sits on the ESP: the removable-media path of
// the architecture, or empty for an architecture chalkos does not build for.
func bootLoader(goarch string) string {
	switch goarch {
	case "amd64":
		return `\EFI\BOOT\BOOTX64.EFI`
	case "arm64":
		return `\EFI\BOOT\BOOTAA64.EFI`
	}
	return ""
}

// InPlace installs the node on the disk it booted from, where the role image already runs.
func (i *Installer) InPlace(ctx context.Context, req Request) error {
	if err := req.validate(); err != nil {
		return err
	}
	if installed, err := Installed(i.StateDir); err != nil {
		return err
	} else if installed {
		return errors.New("the node is installed already")
	}
	boot, err := i.Host.ResolvePath(i.BootDisk)
	if err != nil {
		return fmt.Errorf("find the disk the node booted from: %w", err)
	}
	ref := req.Section.Disks[storage.SystemDisk].Ref
	system, err := i.Host.Resolve(ref)
	if err != nil {
		return fmt.Errorf("resolve the system disk %s: %w", ref, err)
	}
	if system.Name != boot.Name {
		return fmt.Errorf("the identity places the system on %s, but the node runs from %s; install it with the installer instead", system, boot)
	}
	defs, err := readDefinitions(i.Definitions)
	if err != nil {
		return err
	}
	if err := i.closeState(ctx); err != nil {
		return err
	}
	return i.installOn(ctx, boot, defs, req)
}

// closeState unmounts STATE from StateDir and closes its LUKS device: the STATE the node booted
// with, so it can be recreated, or the target's, so the target disk is free again. A STATE that
// is not mounted or not open is fine.
func (i *Installer) closeState(ctx context.Context) error {
	mounted, err := isMounted(i.MountInfo, i.StateDir)
	if err != nil {
		return err
	}
	if mounted {
		_, err := i.Run.Run(ctx, "umount", i.StateDir)
		var te *node.ToolError
		if err != nil && !(errors.As(err, &te) && strings.Contains(te.Stderr, "not mounted")) {
			return fmt.Errorf("unmount STATE: %w", err)
		}
	}
	if _, err := os.Stat(filepath.Join(i.Host.DevRoot, "mapper", stateMapperName)); err == nil {
		if _, err := i.Run.Run(ctx, "systemd-cryptsetup", "detach", stateMapperName); err != nil {
			return fmt.Errorf("close STATE: %w", err)
		}
	}
	return nil
}

// installOn runs the steps both flows share on disk, which holds the role image. defs are the
// role image's repart definitions of the system region.
func (i *Installer) installOn(ctx context.Context, disk storage.BlockDisk, defs map[string]string, req Request) error {
	policy := req.Section.Encryption
	if err := i.prepareState(ctx, disk, defs, policy); err != nil {
		return err
	}
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return err
	}
	state, err := table.find(stateLabel, storage.PartitionType(stateLabel))
	if err != nil {
		return err
	}
	st := &node.Storage{
		Run:        i.Run,
		Host:       i.Host,
		StateDir:   i.StateDir,
		BootDisk:   disk.Device,
		StatusFile: filepath.Join(i.WorkDir, "storage-status.json"),
	}
	if err := st.Open(ctx, stateMapperName, state.Node, i.StateDir, false, policy == storage.EncryptionTPM2); err != nil {
		return err
	}

	section, err := json.Marshal(req.Section)
	if err != nil {
		return err
	}
	if err := WriteFile(filepath.Join(st.StorageDir(), "storage.json"), section, 0o600); err != nil {
		return err
	}
	status := storage.Status{Disks: map[string]storage.DiskStatus{}}
	pins, err := st.Apply(ctx, req.Section, &status)
	if err != nil {
		return err
	}
	if err := diskErrors(status); err != nil {
		return err
	}

	if req.Section.Fallback != storage.FallbackNone {
		table, err := i.readTable(ctx, disk.Device)
		if err != nil {
			return err
		}
		devices, err := encryptedDevices(req.Section, table, pins, state.Node)
		if err != nil {
			return err
		}
		if err := Enroll(ctx, i.Run, devices, req.FallbackSecret); err != nil {
			return err
		}
	}

	// chalkd/ holds the node's private key.
	chalkdDir := filepath.Join(i.StateDir, "chalkd")
	if err := os.MkdirAll(chalkdDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(chalkdDir, 0o700); err != nil {
		return err
	}
	nodeFile, err := req.nodeFile()
	if err != nil {
		return err
	}
	files := []struct {
		path string
		data []byte
		perm fs.FileMode
	}{
		{"identity.json", req.Identity, 0o600},
		// One file, so the chain and the key are only ever replaced together.
		{"chalkd/node.pem", nodeFile, 0o600},
		{"chalkd/ca.crt", req.CA, 0o644},
	}
	for _, f := range files {
		if err := WriteFile(filepath.Join(i.StateDir, f.path), f.data, f.perm); err != nil {
			return err
		}
	}
	if len(req.KubernetesShare) > 0 {
		// validate already parsed and checked the share; encode it again so STATE holds the
		// canonical form, never whatever bytes the request happened to carry.
		share, err := kpki.ParseShare(req.KubernetesShare)
		if err != nil {
			return err
		}
		canonical, err := share.Encode()
		if err != nil {
			return err
		}
		if err := WriteShare(i.StateDir, canonical); err != nil {
			return err
		}
	}
	if err := i.randomizeESP(ctx, disk); err != nil {
		return err
	}
	return WriteFile(filepath.Join(i.StateDir, installedMarker), []byte(i.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// prepareState makes the disk's STATE match the node's encryption policy. A STATE that does
// not match is empty, as the node is not installed yet, so it is deleted and created again.
func (i *Installer) prepareState(ctx context.Context, disk storage.BlockDisk, defs map[string]string, policy string) error {
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return err
	}
	want := contentExt4
	if policy == storage.EncryptionTPM2 {
		want = contentLUKS
	}
	if state, err := table.find(stateLabel, storage.PartitionType(stateLabel)); err == nil {
		found, err := content(ctx, i.Run, state.Node)
		if err != nil {
			return err
		}
		if found == want {
			return nil
		}
		log.Printf("STATE holds %q, but the node's encryption policy is %s; creating it again", found, policy)
		if _, err := i.Run.Run(ctx, "sfdisk", "--delete", disk.Device, strconv.Itoa(state.Number)); err != nil {
			return fmt.Errorf("delete STATE: %w", err)
		}
	} else if !errors.Is(err, errNotFound) {
		return err
	}

	dir := filepath.Join(i.WorkDir, "system")
	if err := writeDefinitions(dir, defs, policy); err != nil {
		return err
	}
	if _, err := i.Run.Run(ctx, "systemd-repart", "--dry-run=no", "--definitions="+dir, "--tpm2-pcrs=7", disk.Device); err != nil {
		return fmt.Errorf("create the system region on %s: %w", disk.Device, err)
	}
	return nil
}

// content returns what blkid finds on a device, such as crypto_LUKS or ext4; empty when it finds
// nothing.
func content(ctx context.Context, run node.Runner, dev string) (string, error) {
	if _, err := run.Run(ctx, "udevadm", "settle", "--timeout=30"); err != nil {
		return "", err
	}
	out, err := run.Run(ctx, "blkid", "-p", "-o", "value", "-s", "TYPE", dev)
	var te *node.ToolError
	if errors.As(err, &te) && te.Code == 2 && strings.TrimSpace(te.Stderr) == "" {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("probe %s: %w", dev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// encryptedDevices lists the LUKS devices that get the fallback keyslot: STATE and every
// encrypted volume, by volume name.
func encryptedDevices(section storage.Section, table partitionTable, pins storage.Pins, state string) (map[string]string, error) {
	devices := map[string]string{}
	if section.Encryption == storage.EncryptionTPM2 {
		devices[stateLabel] = state
	}
	for name, v := range section.Volumes {
		if v.Encryption != storage.EncryptionTPM2 {
			continue
		}
		if v.Disk == storage.SystemDisk {
			p, err := table.find(v.Label, storage.PartitionType(v.Label))
			if err != nil {
				return nil, fmt.Errorf("volume %s: %w", name, err)
			}
			devices[name] = p.Node
			continue
		}
		uuid := pins.Disks[v.Disk].Partitions[name]
		if uuid == "" {
			return nil, fmt.Errorf("volume %s was not created", name)
		}
		devices[name] = storage.PartUUIDPath(uuid)
	}
	return devices, nil
}

// Enroll adds the fallback keyslot to each LUKS device, by volume name, unlocking it with the TPM. The secret only
// travels in the environment; replacing earlier password slots keeps a repeated install from
// piling up keyslots, and TPM2 slots are never wiped.
func Enroll(ctx context.Context, run node.Runner, devices map[string]string, secret string) error {
	names := make([]string, 0, len(devices))
	for name := range devices {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		dev := devices[name]
		found, err := content(ctx, run, dev)
		if err != nil {
			return err
		}
		if found != contentLUKS {
			return fmt.Errorf("volume %s is meant to be encrypted, but %s holds %q instead of LUKS", name, dev, found)
		}
		if _, err := run.RunWithEnv(ctx, []string{"NEWPASSWORD=" + secret}, "systemd-cryptenroll", "--unlock-tpm2-device=auto", "--password", "--wipe-slot=password", dev); err != nil {
			return fmt.Errorf("enroll the fallback key on %s: %w", name, err)
		}
	}
	return nil
}

// randomizeESP gives the ESP a random partition UUID: systemd-boot reports it as the boot
// partition, and no other disk carrying the same image may match it.
func (i *Installer) randomizeESP(ctx context.Context, disk storage.BlockDisk) error {
	table, err := i.readTable(ctx, disk.Device)
	if err != nil {
		return err
	}
	esp, err := table.findType(typeESP)
	if err != nil {
		return err
	}
	if _, err := i.Run.Run(ctx, "sfdisk", "--part-uuid", disk.Device, strconv.Itoa(esp.Number), newUUID()); err != nil {
		return fmt.Errorf("set the ESP's partition UUID: %w", err)
	}
	return nil
}

func diskErrors(status storage.Status) error {
	var problems []string
	for _, name := range sortedKeys(status.Disks) {
		if e := status.Disks[name].Error; e != "" {
			problems = append(problems, fmt.Sprintf("disk %s: %s", name, e))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// readDefinitions reads the *.conf files of a repart.d directory.
func readDefinitions(dir string) (map[string]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.conf"))
	if err != nil {
		return nil, err
	}
	defs := map[string]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		defs[filepath.Base(p)] = string(data)
	}
	return defs, nil
}

// writeDefinitions writes the system region's definitions to dir with STATE encrypted as the
// policy says. The image's definition always encrypts STATE.
func writeDefinitions(dir string, defs map[string]string, policy string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	foundState := false
	for name, text := range defs {
		if strings.ContainsRune(name, '/') || !strings.HasSuffix(name, ".conf") {
			return fmt.Errorf("invalid definition file name %q", name)
		}
		if definesLabel(text, stateLabel) {
			foundState = true
			text = withEncryption(text, policy)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			return err
		}
	}
	if !foundState {
		return errors.New("the system region's definitions have no STATE partition")
	}
	return nil
}

func definesLabel(text, label string) bool {
	for _, line := range strings.Split(text, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "Label" && strings.TrimSpace(v) == label {
			return true
		}
	}
	return false
}

func withEncryption(text, policy string) string {
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if k, _, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "Encrypt" {
			continue
		}
		lines = append(lines, line)
	}
	if policy == storage.EncryptionTPM2 {
		lines = append(lines, "Encrypt=tpm2")
	}
	return strings.Join(lines, "\n") + "\n"
}

// WriteFile replaces a file atomically and durably, creating its directory.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// WriteShare records the node's Kubernetes share on STATE, in a directory only root can enter.
func WriteShare(stateDir string, share []byte) error {
	dir := filepath.Join(stateDir, "kubernetes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	return WriteFile(filepath.Join(dir, "share.json"), share, 0o600)
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isMounted reports whether a file system is mounted at path, per a mountinfo file.
func isMounted(mountInfo, path string) (bool, error) {
	data, err := os.ReadFile(mountInfo)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 && fields[4] == path {
			return true, nil
		}
	}
	return false, nil
}
