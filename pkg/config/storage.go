package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
)

// uuidOf hashes input into the shape of a UUID, so the same input always gives the same UUID: the
// partition types, which the image derives too, and the disks' seeds installed nodes hold.
func uuidOf(input string) string {
	sum := sha256.Sum256([]byte(input))
	h := hex.EncodeToString(sum[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// repart assigns existing partitions to definitions by type, then in file name order. A type
// per label keeps a new volume from taking over the partition of another one.
func partitionType(label string) string { return uuidOf("chalkos-partition-type:" + label) }

var reservedRepartKeys = []string{"Type", "Label", "Encrypt", "Format", "SizeMinBytes", "SizeMaxBytes", "MountPoint"}

// Labels the system region and VAR use, and the system disk's name. A volume on the system disk
// with one of them would share its link below /dev/disk/chalk-boot with a partition of the image;
// a volume with its own disk named like the system disk would replace its entry.
var reservedNames = []string{"esp", "store", "store-verity", "state", storage.VarVolume, storage.SystemDisk}

var (
	volumeName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	// Mount points go verbatim into unit files, where spaces, newlines or backslashes would
	// change their meaning.
	mountPath = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
)

func validMountPoint(p string) bool {
	if !mountPath.MatchString(p) || slices.Contains([]string{"/", "/nix", "/state", "/var"}, p) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return !strings.HasPrefix(p, "/nix/") && !strings.HasPrefix(p, "/state/")
}

// definition renders a repart.d definition as installed nodes hold it: keys in name
// order, the typed ones replacing the passthrough, and = in a key escaped.
func definition(label, format string, size *string, mode string, repart map[string]string) string {
	keys := map[string]string{}
	for k, v := range repart {
		keys[k] = v
	}
	keys["Type"] = partitionType(label)
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
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("[Partition]\n")
	for _, k := range names {
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
	if err := sd.decodeSpec(&sys); err != nil {
		return storage.Section{}, err
	}
	systemRef, err := sys.Disk.Ref()
	if err != nil {
		return storage.Section{}, fmt.Errorf("%s: disk: %w", sd, err)
	}
	var vols []namedVolume
	for _, d := range s.Of(StorageAPI, "Volume") {
		v := namedVolume{name: d.Name}
		if err := d.decodeSpec(&v.spec); err != nil {
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
		return uuidOf("chalkos-seed:" + name + "/" + disk)
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
		def := definition(v.name, deref(v.spec.Format), v.spec.Size, mode, v.spec.Repart)
		disk := storage.SystemDisk
		if v.ref == nil {
			sec.Disks[storage.SystemDisk].Repart["60-"+v.name+".conf"] = def
		} else {
			disk = v.name
			sec.Disks[v.name] = storage.Disk{Ref: *v.ref, Seed: seed(v.name), Repart: map[string]string{"10-" + v.name + ".conf": def}}
		}
		sec.Volumes[v.name] = storage.Volume{Disk: disk, Label: v.name, Format: deref(v.spec.Format),
			MountPoint: deref(v.spec.MountPoint), Encryption: mode, Size: deref(v.spec.Size)}
	}
	return sec, nil
}

// storageErrors are the storage checks of a node's config, all reported at once and in this
// order: each volume's own problems by volume name, then shared disks, then volumes that fill
// the system disk, then mount points used twice. vols are ordered by name.
func storageErrors(system storage.Ref, sys systemDiskSpec, vols []namedVolume) error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	// VAR counts as a volume of the system disk, and is mounted at /var like any volume.
	var filling []string
	if sys.Var.Size == nil {
		filling = append(filling, "var")
	}
	mounts := map[string][]string{"/var": {"var"}}
	for _, v := range vols {
		at := "Volume " + v.name
		if !volumeName.MatchString(v.name) || len(v.name) > 32 {
			add("%s: the name must match [a-z0-9]([a-z0-9-]*[a-z0-9])? and have at most 32 characters", at)
		}
		if slices.Contains(reservedNames, v.name) {
			add("%s: the name %s is reserved", at, v.name)
		}
		mp := v.spec.MountPoint
		if mp != nil && !validMountPoint(*mp) {
			add("%s: mountPoint must be an absolute path of the characters A-Z, a-z, 0-9, ., _, - and /, other than /, /nix, /state and /var, and not below /nix or /state", at)
		}
		if v.spec.Format == nil && mp != nil {
			add("%s: a raw volume (format = null) cannot have a mountPoint", at)
		}
		if v.spec.Format != nil && *v.spec.Format == "swap" && mp != nil {
			add("%s: a swap volume cannot have a mountPoint", at)
		}
		if v.ref != nil && *v.ref == system {
			add("%s: disk is the system disk; leave disk out to place the volume there", at)
		}
		repartKeys := slices.Sorted(maps.Keys(v.spec.Repart))
		for _, k := range repartKeys {
			if slices.Contains(reservedRepartKeys, k) {
				add("%s: repart must not set %s; it comes from the typed options", at, k)
			}
		}
		// A line break would end the key=value line and let the rest set keys or sections the
		// typed options own.
		for _, k := range repartKeys {
			if strings.ContainsAny(k+v.spec.Repart[k], "\n\r") {
				add("%s: repart %q: a key or value cannot contain a line break", at, k)
			}
		}
		if v.ref == nil && v.spec.Size == nil {
			filling = append(filling, v.name)
		}
		if mp != nil {
			mounts[*mp] = append(mounts[*mp], at)
		}
	}
	for i, v := range vols {
		for _, w := range vols[i+1:] {
			if v.ref != nil && w.ref != nil && *v.ref == *w.ref {
				add("Volume %s and Volume %s reference the same disk; a volume with a disk occupies it alone", v.name, w.name)
			}
		}
	}
	if len(filling) > 1 {
		add("at most one volume per disk may have size = null; on the system disk these do: %s", strings.Join(filling, ", "))
	}
	paths := make([]string, 0, len(mounts))
	for path := range mounts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if names := mounts[path]; len(names) > 1 {
			add("%s have the same mountPoint %s", strings.Join(names, " and "), path)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid storage:\n- %s", strings.Join(errs, "\n- "))
}
