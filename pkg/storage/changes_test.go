package storage

import (
	"reflect"
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

func TestChanges(t *testing.T) {
	for _, c := range []struct {
		name      string
		delivered Section
		want      []Change
	}{
		{"new volume", with("extra", func(v *Volume) {}), []Change{{"extra", "new volume"}}},
		{"new volume on a new disk", func() Section {
			s := with("fast", func(v *Volume) { v.Disk = "fast" })
			s.Disks["fast"] = Disk{Ref: Ref{Path: "/dev/nvme0n1"}}
			return s
		}(), []Change{{"fast", "new volume on new disk fast"}}},
		{"larger size", with("data", func(v *Volume) { v.Size = "2G" }), []Change{{"data", "larger: 2G instead of 1G"}}},
		{"null size where a fixed size was", with("data", func(v *Volume) { v.Size = "" }), []Change{{"data", "fills its disk instead of 1G"}}},
		{"mount point", with("data", func(v *Volume) { v.MountPoint = "/srv/data" }), []Change{{"data", "mounted at /srv/data instead of /var/lib/data"}}},
		{"larger and mounted elsewhere", with("data", func(v *Volume) { v.Size = "2G"; v.MountPoint = "" }), []Change{{"data", "larger: 2G instead of 1G; mounted at null instead of /var/lib/data"}}},
		// The changes the node does not make are the rules' to refuse.
		{"smaller size", with("data", func(v *Volume) { v.Size = "512M" }), nil},
		{"fixed size where null was", with("var", func(v *Volume) { v.Size = "" }), []Change{{"var", "fills its disk instead of 2G"}}},
		{"another format", with("data", func(v *Volume) { v.Format = "ext4" }), nil},
		{"removed volume", with("data", nil), nil},
		{"equal size written differently", with("data", func(v *Volume) { v.Size = "1024M" }), nil},
		{"unchanged", baseSection(), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Changes(baseSection(), c.delivered); !reflect.DeepEqual(got, c.want) {
				t.Errorf("changes = %v, want %v", got, c.want)
			}
		})
	}
	if got := Changes(with("data", func(v *Volume) { v.Size = "" }), baseSection()); got != nil {
		t.Errorf("a fixed size where null was: %v, want no change", got)
	}
}
