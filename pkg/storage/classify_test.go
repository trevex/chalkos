package storage

import (
	"errors"
	"strings"
	"testing"
)

func baseSection() Section {
	return Section{
		Disks: map[string]Disk{
			"system": {Ref: Ref{Path: "/dev/vda"}},
			"data":   {Ref: Ref{Path: "/dev/vdb"}},
		},
		Volumes: map[string]Volume{
			"var":  {Disk: "system", Label: "var", Format: "ext4", MountPoint: "/var", Encryption: "tpm2", Size: "2G"},
			"data": {Disk: "data", Label: "data", Format: "xfs", MountPoint: "/var/lib/data", Encryption: "tpm2", Size: "1G"},
		},
		Fallback: "recovery-key",
	}
}

// with returns the base section with one volume replaced; a nil change removes it.
func with(name string, change func(*Volume)) Section {
	s := baseSection()
	v, ok := s.Volumes[name]
	if change == nil {
		delete(s.Volumes, name)
		return s
	}
	if !ok {
		v = Volume{Disk: "system", Label: name, Format: "ext4", Encryption: "tpm2", Size: "1G"}
	}
	change(&v)
	s.Volumes[name] = v
	return s
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		name      string
		delivered Section
		want      Class
		reason    string
	}{
		{"new volume", with("extra", func(v *Volume) {}), Additive, "new volume"},
		{"new volume on a new disk", func() Section {
			s := with("fast", func(v *Volume) { v.Disk = "fast" })
			s.Disks["fast"] = Disk{Ref: Ref{Path: "/dev/nvme0n1"}}
			return s
		}(), Additive, "new disk fast"},
		{"larger size", with("data", func(v *Volume) { v.Size = "2G" }), Additive, "larger"},
		{"null size where a fixed size was", with("data", func(v *Volume) { v.Size = "" }), Additive, "fills its disk"},
		{"mount point only", with("data", func(v *Volume) { v.MountPoint = "/srv/data" }), Additive, "mounted at /srv/data"},
		{"smaller size", with("data", func(v *Volume) { v.Size = "512M" }), Destructive, "smaller"},
		{"different format", with("data", func(v *Volume) { v.Format = "ext4" }), Destructive, "format ext4 instead of xfs"},
		{"raw instead of formatted", with("data", func(v *Volume) { v.Format = ""; v.MountPoint = "" }), Destructive, "format null"},
		{"encryption mode", with("data", func(v *Volume) { v.Encryption = "none" }), Destructive, "encryption none"},
		{"different disk", with("data", func(v *Volume) { v.Disk = "system" }), Destructive, "disk system instead of data"},
		{"removed volume", with("data", nil), Destructive, "removed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			changes := Classify(baseSection(), c.delivered)
			if len(changes) != 1 {
				t.Fatalf("changes = %v, want exactly one", changes)
			}
			if changes[0].Class != c.want || !strings.Contains(changes[0].Reason, c.reason) {
				t.Errorf("change = %v, want %v with %q", changes[0], c.want, c.reason)
			}
		})
	}
}

func TestClassifyFixedSizeWhereNullWas(t *testing.T) {
	recorded := with("data", func(v *Volume) { v.Size = "" })
	changes := Classify(recorded, baseSection())
	if len(changes) != 1 || changes[0].Class != Destructive {
		t.Fatalf("changes = %v, want one destructive change", changes)
	}
}

func TestClassifyUnchanged(t *testing.T) {
	if changes := Classify(baseSection(), baseSection()); len(changes) != 0 {
		t.Errorf("changes = %v, want none", changes)
	}
}

func TestClassifyMixedChangeIsDestructive(t *testing.T) {
	changes := Classify(baseSection(), with("data", func(v *Volume) {
		v.Size = "2G"
		v.Encryption = "none"
	}))
	if len(changes) != 1 || changes[0].Class != Destructive || !strings.Contains(changes[0].Reason, "larger") {
		t.Errorf("changes = %v, want one destructive change that also names the growth", changes)
	}
}

func TestClassifyDisks(t *testing.T) {
	pinned := Identity{Serial: "S1", Model: "m"}
	pins := Pins{Disks: map[string]Pin{
		"system": {Ref: Ref{Path: "/dev/vda"}, Identity: Identity{Path: "boot"}},
		"data":   {Ref: Ref{Path: "/dev/vdb"}, Identity: pinned},
	}}
	moved := func(ref Ref) Section {
		s := baseSection()
		s.Disks["data"] = Disk{Ref: ref}
		return s
	}
	resolveTo := func(id Identity, err error) func(Ref) (Identity, error) {
		return func(Ref) (Identity, error) { return id, err }
	}

	if changes := ClassifyDisks(baseSection(), pins, func(Ref) (Identity, error) {
		t.Fatal("resolved an unchanged reference")
		return Identity{}, nil
	}); len(changes) != 0 {
		t.Errorf("unchanged reference: %v", changes)
	}
	if changes := ClassifyDisks(moved(Ref{Selector: Selector{Serial: "S1"}}), pins, resolveTo(pinned, nil)); len(changes) != 0 {
		t.Errorf("reference to the pinned disk: %v", changes)
	}
	changes := ClassifyDisks(moved(Ref{Path: "/dev/vdc"}), pins, resolveTo(Identity{Serial: "S2", Model: "m"}, nil))
	if len(changes) != 1 || changes[0].Volume != "data" || changes[0].Class != Destructive || !strings.Contains(changes[0].Reason, "resolves to") {
		t.Errorf("reference to another disk: %v", changes)
	}
	changes = ClassifyDisks(moved(Ref{Path: "/dev/vdc"}), pins, resolveTo(Identity{}, errors.New("no disk matches")))
	if len(changes) != 1 || changes[0].Class != Destructive || !strings.Contains(changes[0].Reason, "no disk matches") {
		t.Errorf("unresolvable reference: %v", changes)
	}
}
