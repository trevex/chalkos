package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
)

// typedRepartKeys are the keys of a volume's definition that its typed options set.
var typedRepartKeys = []string{"Type", "Label", "Encrypt", "TPM2PCRs", "Format", "SizeMinBytes", "SizeMaxBytes", "MountPoint"}

// repartKeys are the keys of repart's [Partition] section a Volume's spec.repart may set: those
// that place or size the partition or set its GPT fields, and take nothing from the host.
// systemd-repart runs the definitions as root in the initrd, with STATE open, so any key not
// listed, such as one that copies files or blocks from the host or makes directories, is refused.
// FactoryReset may be no alone.
var repartKeys = []string{"Priority", "Weight", "PaddingWeight", "PaddingMinBytes", "PaddingMaxBytes", "GrowFileSystem", "NoAuto", "ReadOnly", "Flags", "UUID", "FactoryReset"}

// definition renders a repart.d definition as installed nodes hold it: keys in name
// order, the typed ones replacing the passthrough, and = in a key escaped.
func definition(label, format string, size *string, mode string, repart map[string]string) string {
	keys := map[string]string{}
	maps.Copy(keys, repart)
	keys["Type"] = storage.PartitionType(label)
	keys["Label"] = label
	if format != "" {
		keys["Format"] = format
	}
	if size != nil {
		keys["SizeMinBytes"] = *size
		keys["SizeMaxBytes"] = *size
	}
	if mode == storage.EncryptionTPM2 {
		keys["Encrypt"] = "tpm2"
		keys["TPM2PCRs"] = "7"
	}
	var b strings.Builder
	b.WriteString("[Partition]\n")
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		fmt.Fprintf(&b, "%s=%s\n", strings.ReplaceAll(k, "=", `\=`), keys[k])
	}
	return b.String()
}

func orMode(override *string, node string) string {
	if override != nil {
		return *override
	}
	return node
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type namedVolume struct {
	name string
	spec volumeSpec
	ref  *storage.Ref
}

// storage renders the node's storage section from its SystemDisk and Volume documents: on the
// system disk VAR comes first and volumes follow by name; a volume with its own disk names that
// disk. Each disk's seed derives from the node's and the disk's name.
func (s *Set) storage(name string) (storage.Section, error) {
	sd, _ := s.One("SystemDisk")
	var sys systemDiskSpec
	if err := sd.DecodeSpec(&sys); err != nil {
		return storage.Section{}, err
	}
	systemRef, err := sys.Disk.Ref()
	if err != nil {
		return storage.Section{}, fmt.Errorf("%s: disk: %w", sd, err)
	}
	var vols []namedVolume
	for _, d := range s.docs.Of(StorageAPI, "Volume") {
		v := namedVolume{name: d.Name}
		if err := d.DecodeSpec(&v.spec); err != nil {
			return storage.Section{}, err
		}
		if v.spec.Disk != nil {
			r, err := v.spec.Disk.Ref()
			if err != nil {
				return storage.Section{}, fmt.Errorf("%s: disk: %w", d, err)
			}
			v.ref = &r
		}
		vols = append(vols, v)
	}
	if err := storageErrors(systemRef, sys, vols); err != nil {
		return storage.Section{}, err
	}

	seed := func(disk string) string {
		return storage.UUIDOf("chalkos-seed:" + name + "/" + disk)
	}
	varMode := orMode(sys.Var.Encryption.Mode, sys.Encryption.Mode)
	sec := storage.Section{
		Disks: map[string]storage.Disk{
			storage.SystemDisk: {Ref: systemRef, Seed: seed(storage.SystemDisk), Repart: map[string]string{
				"50-var.conf": definition("var", "ext4", sys.Var.Size, varMode, nil),
			}},
		},
		Volumes: map[string]storage.Volume{
			storage.VarVolume: {Disk: storage.SystemDisk, Label: "var", Format: "ext4", MountPoint: "/var",
				Encryption: varMode, Size: deref(sys.Var.Size)},
		},
		Fallback:   sys.Encryption.Fallback,
		Encryption: sys.Encryption.Mode,
	}
	for _, v := range vols {
		mode := orMode(v.spec.Encryption.Mode, sys.Encryption.Mode)
		format := fileSystem(v.spec.Format)
		def := definition(v.name, format, v.spec.Size, mode, v.spec.Repart)
		disk := storage.SystemDisk
		if v.ref == nil {
			sec.Disks[storage.SystemDisk].Repart["60-"+v.name+".conf"] = def
		} else {
			disk = v.name
			sec.Disks[v.name] = storage.Disk{Ref: *v.ref, Seed: seed(v.name), Repart: map[string]string{"10-" + v.name + ".conf": def}}
		}
		sec.Volumes[v.name] = storage.Volume{Disk: disk, Label: v.name, Format: format,
			MountPoint: deref(v.spec.MountPoint), Encryption: mode, Size: deref(v.spec.Size), Exec: v.spec.Exec}
	}
	return sec, nil
}

// storageErrors reports the storage checks of a node's config all at once: each volume's own
// problems, then volumes that fill the system disk. It holds names and mount points to the rules
// Section.Validate applies to what chalkd receives, so the rendered section passes it.
func storageErrors(system storage.Ref, sys systemDiskSpec, vols []namedVolume) error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	// VAR counts as a volume of the system disk.
	var filling []string
	if sys.Var.Size == nil {
		filling = append(filling, "var")
	}
	for _, v := range vols {
		at := "Volume " + v.name
		if !storage.ValidName(v.name) {
			add("%s: the name must match [a-z0-9]([a-z0-9-]*[a-z0-9])? and have at most 32 characters", at)
		}
		if storage.Reserved(v.name) {
			add("%s: the name %s is reserved", at, v.name)
		}
		mp := v.spec.MountPoint
		if mp != nil && !storage.SafeMountPoint(*mp) {
			add("%s: mountPoint must be an absolute path of the characters A-Z, a-z, 0-9, ., _, - and /, other than /, /nix, /state and /var, and not below /nix or /state", at)
		}
		if v.spec.Format == formatRaw && mp != nil {
			add("%s: a raw volume cannot have a mountPoint", at)
		}
		if v.spec.Format == "swap" && mp != nil {
			add("%s: a swap volume cannot have a mountPoint", at)
		}
		if v.ref != nil && *v.ref == system {
			add("%s: disk is the system disk; leave disk out to place the volume there", at)
		}
		keys := slices.Sorted(maps.Keys(v.spec.Repart))
		for _, k := range keys {
			switch {
			case slices.Contains(typedRepartKeys, k):
				add("%s: repart must not set %s; it comes from the typed options", at, k)
			case !slices.Contains(repartKeys, k):
				add("%s: repart sets %s, which a volume's definition may not; it may set %s", at, k, strings.Join(repartKeys, ", "))
			case k == "FactoryReset" && v.spec.Repart[k] != "no":
				add("%s: repart sets FactoryReset to %q, which a volume's definition may set to no alone", at, v.spec.Repart[k])
			}
		}
		// A line break would end the key=value line and let the rest set keys or sections the
		// typed options own.
		for _, k := range keys {
			if strings.ContainsAny(k+v.spec.Repart[k], "\n\r") {
				add("%s: repart %q: a key or value cannot contain a line break", at, k)
			}
		}
		if v.ref == nil && v.spec.Size == nil {
			filling = append(filling, v.name)
		}
	}
	if len(filling) > 1 {
		add("at most one volume per disk may have size = null; on the system disk these do: %s", strings.Join(filling, ", "))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid storage:\n- %s", strings.Join(errs, "\n- "))
}

// formatRaw is the format of a volume left a raw block device.
const formatRaw = "raw"

// fileSystem is the file system repart formats a volume of the format with, which the recorded
// section names: none for a raw volume.
func fileSystem(format string) string {
	if format == formatRaw {
		return ""
	}
	return format
}
