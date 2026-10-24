package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/tool"
)

// The facts chalkos-storage reports for the storage kinds' rules, which read the node's disks and
// keyslots: the documents and the recorded section say what the config changes, these facts what
// the machine has.
const (
	FactDisks    = "disks"
	FactFallback = "fallback"
)

// factsInput is what a command fact reads on standard input: the config by apiVersion, kind and
// name, and the inputs the fact declares.
type factsInput struct {
	Config         map[string]map[string]map[string]json.RawMessage `json:"config"`
	FallbackSecret string                                           `json:"fallbackSecret"`
}

// Facts reports the fact which, disks or fallback, as JSON, for the input of a command fact.
func (s *Storage) Facts(ctx context.Context, which string, input []byte) ([]byte, error) {
	var in factsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("read the input: %w", err)
	}
	var v any
	var err error
	switch which {
	case FactDisks:
		var refs map[string]storage.Ref
		if refs, err = volumeDisks(in.Config); err == nil {
			v, err = s.DiskFacts(refs)
		}
	case FactFallback:
		v, err = s.FallbackFacts(ctx, in.FallbackSecret)
	default:
		return nil, fmt.Errorf("no fact %q; the facts are %s and %s", which, FactDisks, FactFallback)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// volumeDisks returns the disk reference of each Volume document with a disk of its own, by the
// volume's name, which names its disk too.
func volumeDisks(config map[string]map[string]map[string]json.RawMessage) (map[string]storage.Ref, error) {
	refs := map[string]storage.Ref{}
	for name, raw := range config["storage.chalkos.dev/v1alpha1"]["Volume"] {
		var doc struct {
			Spec struct {
				Disk *storage.DiskRef `json:"disk"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("Volume %q: %w", name, err)
		}
		if doc.Spec.Disk == nil {
			continue
		}
		ref, err := doc.Spec.Disk.Ref()
		if err != nil {
			return nil, fmt.Errorf("Volume %q: disk: %w", name, err)
		}
		refs[name] = ref
	}
	return refs, nil
}

// DiskFact is what the node finds of the disk a volume has to itself.
type DiskFact struct {
	// Ref is the delivered reference, as messages name it.
	Ref string `json:"ref"`
	// Pinned tells whether the node pinned a disk of the name; Identity is that disk's.
	Pinned   bool   `json:"pinned"`
	Identity string `json:"identity"`
	// Changed tells whether the reference differs from the one the disk was pinned by. Only a
	// changed reference is resolved: Found is the identity of the disk it finds and Same whether
	// that is the pinned disk; Error says why it finds none, and ErrorKind is noDisk when no disk
	// matches, listDisks when the node cannot list its disks, and other otherwise.
	Changed   bool   `json:"changed"`
	Found     string `json:"found"`
	Same      bool   `json:"same"`
	Error     string `json:"error"`
	ErrorKind string `json:"errorKind"`
	// Present tells whether the pinned disk is plugged in; PresentError says why that cannot be
	// told, as when two disks have its identity.
	Present      bool   `json:"present"`
	PresentError string `json:"presentError"`
}

// DiskFacts resolves the volumes' disk references against the host and the disks the node
// pinned, by the volumes' names.
func (s *Storage) DiskFacts(refs map[string]storage.Ref) (map[string]DiskFact, error) {
	pins, err := storage.ReadPins(filepath.Join(s.StorageDir(), "disks.json"))
	if err != nil {
		return nil, err
	}
	out := map[string]DiskFact{}
	for name, ref := range refs {
		f := DiskFact{Ref: ref.String()}
		if pin, ok := pins.Disks[name]; ok {
			f.Pinned, f.Identity = true, pin.Identity.String()
			if pin.Ref != ref {
				f.Changed = true
				d, err := s.Host.Resolve(ref)
				switch {
				case errors.Is(err, storage.ErrNoDisk):
					f.Error, f.ErrorKind = err.Error(), "noDisk"
				case errors.Is(err, storage.ErrListDisks):
					f.Error, f.ErrorKind = err.Error(), "listDisks"
				case err != nil:
					f.Error, f.ErrorKind = err.Error(), "other"
				default:
					f.Found, f.Same = d.Identity.String(), d.Identity.Same(pin.Identity)
				}
			}
			if _, found, err := s.Host.Find(pin.Identity); err != nil {
				f.PresentError = err.Error()
			} else {
				f.Present = found
			}
		}
		out[name] = f
	}
	return out, nil
}

// FallbackFact says whether the fallback secret delivered with the config opens the node's
// fallback keyslot.
type FallbackFact struct {
	Sent bool `json:"sent"`
	// Matches is null when nothing was sent or the node has no fallback keyslot to check against.
	Matches *bool `json:"matches"`
}

// FallbackFacts checks the fallback secret against the password keyslot of STATE, else VAR, else
// another encrypted volume storage.json records, so a mistyped password cannot leave a volume
// whose fallback differs from the others'. A node without such a keyslot, or with no fallback,
// has nothing to check against. It only reads the devices.
func (s *Storage) FallbackFacts(ctx context.Context, secret string) (FallbackFact, error) {
	if secret == "" {
		return FallbackFact{}, nil
	}
	f := FallbackFact{Sent: true}
	dir := s.StorageDir()
	kept, err := storage.ReadSection(filepath.Join(dir, "storage.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return FallbackFact{}, err
	}
	if kept.Fallback == storage.FallbackNone {
		return f, nil
	}
	pins, err := storage.ReadPins(filepath.Join(dir, "disks.json"))
	if err != nil {
		return FallbackFact{}, err
	}
	for _, dev := range s.encryptedDevices(kept, pins) {
		dump, err := s.Run.Run(ctx, "cryptsetup", "luksDump", "--dump-json-metadata", dev)
		if err != nil {
			return FallbackFact{}, fmt.Errorf("read the keyslots of %s to check the fallback secret: %w", dev, err)
		}
		slots, err := passwordSlots(dump)
		if err != nil {
			return FallbackFact{}, fmt.Errorf("read the keyslots of %s to check the fallback secret: %w", dev, err)
		}
		if len(slots) == 0 {
			continue
		}
		matches := false
		f.Matches = &matches
		for _, slot := range slots {
			// The secret goes on standard input: arguments are visible to every process, and
			// cryptsetup takes a key file from stdin as it is, without trimming a newline.
			_, err := s.Run.RunWithInput(ctx, []byte(secret), "cryptsetup", "open", "--test-passphrase", "--key-slot="+slot, "--key-file=-", dev)
			var te *tool.Error
			switch {
			case err == nil:
				matches = true
				return f, nil
			case errors.As(err, &te) && te.Code == 2:
				// cryptsetup's status for a passphrase no keyslot accepts.
				continue
			default:
				return FallbackFact{}, fmt.Errorf("check the fallback secret against %s: %w", dev, err)
			}
		}
		return f, nil
	}
	return f, nil
}

// encryptedDevices lists the LUKS devices of kept that carry the fallback keyslot, in the order
// the fallback secret is checked against them.
func (s *Storage) encryptedDevices(kept storage.Section, pins storage.Pins) []string {
	var devices []string
	if kept.Encryption == storage.EncryptionTPM2 {
		devices = append(devices, filepath.Join(s.BootPartitions, "state"))
	}
	names := []string{storage.VarVolume}
	var others []string
	for name := range kept.Volumes {
		if name != storage.VarVolume {
			others = append(others, name)
		}
	}
	sort.Strings(others)
	for _, name := range append(names, others...) {
		v, ok := kept.Volumes[name]
		if !ok || v.Encryption != storage.EncryptionTPM2 {
			continue
		}
		if v.Disk == storage.SystemDisk {
			devices = append(devices, filepath.Join(s.BootPartitions, v.Label))
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
