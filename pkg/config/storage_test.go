package config

import (
	"strings"
	"testing"
)

func TestUUIDOf(t *testing.T) {
	// The partition type of VAR, as the image derives it too.
	if got := uuidOf("chalkos-partition-type:var"); got != "65f335d7-a1f7-f6df-b954-a97d9a5db9e6" {
		t.Errorf("uuidOf = %s", got)
	}
}

func TestDefinitionMatchesNix(t *testing.T) {
	size := "200G"
	got := definition("var", "ext4", &size, "tpm2", nil)
	want := "[Partition]\nEncrypt=tpm2\nFormat=ext4\nLabel=var\nSizeMaxBytes=200G\nSizeMinBytes=200G\nTPM2PCRs=7\nType=65f335d7-a1f7-f6df-b954-a97d9a5db9e6\n"
	if got != want {
		t.Errorf("definition =\n%s\nwant\n%s", got, want)
	}
	// A VAR without a size fills the disk: no size keys at all.
	got = definition("var", "ext4", nil, "tpm2", nil)
	want = "[Partition]\nEncrypt=tpm2\nFormat=ext4\nLabel=var\nTPM2PCRs=7\nType=65f335d7-a1f7-f6df-b954-a97d9a5db9e6\n"
	if got != want {
		t.Errorf("definition =\n%s\nwant\n%s", got, want)
	}
}

func TestStorageRejects(t *testing.T) {
	vol := func(name, spec string) string {
		return "---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: " + name + "}\nspec: " + spec + "\n"
	}
	for name, tc := range map[string]struct{ extra, want string }{
		"reserved name":        {vol("state", "{}"), "reserved"},
		"invalid name":         {vol("a.b", "{}"), "must match"},
		"forbidden mount":      {vol("data", "{mountPoint: /nix/x}"), "mountPoint"},
		"raw with mount":       {vol("data", "{format: null, mountPoint: /srv}"), "raw volume"},
		"swap with mount":      {vol("data", "{format: swap, mountPoint: /srv}"), "swap volume"},
		"system disk as own":   {vol("data", "{disk: {path: /dev/vda}}"), "system disk"},
		"shared disk":          {vol("a", "{disk: {path: /dev/vdb}}") + vol("b", "{disk: {path: /dev/vdb}}"), "same disk"},
		"two filling":          {vol("data", "{}"), "size = null"},
		"same mount point":     {vol("a", "{size: 1G, mountPoint: /srv}") + vol("b", "{size: 1G, mountPoint: /srv}"), "same mountPoint"},
		"mount at var":         {vol("data", "{size: 1G, mountPoint: /var}"), "mountPoint"},
		"reserved repart key":  {vol("data", "{size: 1G, repart: {Label: x}}"), "must not set Label"},
		"path and selector":    {vol("data", "{disk: {path: /dev/vdb, model: x}}"), "path or selector"},
		"empty disk reference": {vol("data", "{disk: {}}"), "path or selector"},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := parseSet(t, minimal+tc.extra)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.storage(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestStorageErrorsReadLikeNix(t *testing.T) {
	vol := func(name, spec string) string {
		return "---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: " + name + "}\nspec: " + spec + "\n"
	}
	s, err := parseSet(t, minimal+
		vol("a", "{disk: {path: /dev/vdb}}")+vol("b", "{disk: {path: /dev/vdb}}")+vol("c", "{disk: {path: /dev/vdb}}")+
		vol("data", "{size: 1G, mountPoint: /var}"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.storage()
	want := "invalid storage:\n" +
		"- Volume data: mountPoint must be an absolute path of the characters A-Z, a-z, 0-9, ., _, - and /, other than /, /nix, /state and /var, and not below /nix or /state\n" +
		"- Volume a and Volume b reference the same disk; a volume with a disk occupies it alone\n" +
		"- Volume a and Volume c reference the same disk; a volume with a disk occupies it alone\n" +
		"- Volume b and Volume c reference the same disk; a volume with a disk occupies it alone\n" +
		"- var and Volume data have the same mountPoint /var"
	if err == nil || err.Error() != want {
		t.Fatalf("err =\n%v\nwant\n%s", err, want)
	}
}

func TestStorageVolumesOnSystemAndOwnDisk(t *testing.T) {
	s, err := parseSet(t, minimal+
		"---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: data}\nspec: {size: 10G, format: xfs, mountPoint: /srv/data, encryption: {mode: none}, repart: {Weight: \"2000\"}}\n"+
		"---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: fast}\nspec: {disk: {model: \"Samsung*\", type: nvme}, format: null}\n")
	if err != nil {
		t.Fatal(err)
	}
	sec, err := s.storage()
	if err != nil {
		t.Fatal(err)
	}
	if got := sec.Disks["system"].Repart["60-data.conf"]; !strings.Contains(got, "Weight=2000\n") || strings.Contains(got, "Encrypt") {
		t.Errorf("60-data.conf =\n%s", got)
	}
	fast := sec.Disks["fast"]
	if fast.Ref.Selector.Model != "Samsung*" || fast.Ref.Selector.Type != "nvme" || fast.Repart["10-fast.conf"] == "" {
		t.Errorf("fast = %+v", fast)
	}
	if v := sec.Volumes["fast"]; v.Disk != "fast" || v.Format != "" || v.Encryption != "tpm2" {
		t.Errorf("volume fast = %+v", v)
	}
	if sec.Disks["fast"].Seed != uuidOf("chalkos-seed:lab/n1/fast") {
		t.Errorf("seed = %s", sec.Disks["fast"].Seed)
	}
}

func TestReservedRepartKeysInKeyOrder(t *testing.T) {
	s, err := parseSet(t, minimal+"---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: data}\n"+
		"spec: {size: 1G, repart: {Type: x, Label: y, Weight: \"10\"}}\n")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.storage()
	want := "invalid storage:\n" +
		"- Volume data: repart must not set Label; it comes from the typed options\n" +
		"- Volume data: repart must not set Type; it comes from the typed options"
	if err == nil || err.Error() != want {
		t.Fatalf("err =\n%v\nwant\n%s", err, want)
	}
}

func TestRepartRejectsLineBreaks(t *testing.T) {
	for name, repart := range map[string]string{
		"value": `{Weight: "10\nFormat=vfat"}`,
		"key":   `{"Weight\r": "10"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, err := parseSet(t, minimal+"---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: data}\n"+
				"spec: {size: 1G, repart: "+repart+"}\n")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.storage(); err == nil || !strings.Contains(err.Error(), "Volume data: repart") || !strings.Contains(err.Error(), "line break") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestOwnDiskDefinitionMatchesNix(t *testing.T) {
	s, err := parseSet(t, minimal+"---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: longhorn}\n"+
		"spec: {disk: {model: \"Samsung SSD 870*\", type: ssd}, format: xfs, mountPoint: /var/lib/longhorn}\n")
	if err != nil {
		t.Fatal(err)
	}
	sec, err := s.storage()
	if err != nil {
		t.Fatal(err)
	}
	// The homelab worker's longhorn definition.
	want := "[Partition]\nEncrypt=tpm2\nFormat=xfs\nLabel=longhorn\nTPM2PCRs=7\nType=4e6bd13b-2e2a-03b6-3fc9-fc66eae45cba\n"
	if got := sec.Disks["longhorn"].Repart; len(got) != 1 || got["10-longhorn.conf"] != want {
		t.Errorf("repart = %q, want 10-longhorn.conf = %q", got, want)
	}
}

func TestDefinitionEscapesEqualsInKeys(t *testing.T) {
	size := "1G"
	got := definition("data", "xfs", &size, "none", map[string]string{"A=B": "c"})
	want := "[Partition]\nA\\=B=c\nFormat=xfs\nLabel=data\nSizeMaxBytes=1G\nSizeMinBytes=1G\nType=" + partitionType("data") + "\n"
	if got != want {
		t.Errorf("definition =\n%s\nwant\n%s", got, want)
	}
}

// TestStorageRefusesNamesMountPointsAndKeys refuses volume names that cannot name a partition or
// that chalkos's own partitions hold, mount points outside the paths a volume may take, and the
// repart keys the typed fields set.
func TestStorageRefusesNamesMountPointsAndKeys(t *testing.T) {
	vol := func(name, spec string) string {
		return "---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: \"" + name + "\"}\nspec: " + spec + "\n"
	}
	refused := func(extra string) bool {
		s, err := parseSet(t, minimal+extra)
		if err != nil {
			return true
		}
		_, err = s.storage()
		return err != nil
	}
	for _, name := range []string{"Data", "-data", strings.Repeat("a", 33), "var", "state", "esp", "store", "store-verity", "system"} {
		if !refused(vol(name, "{size: 1G}")) {
			t.Errorf("took the volume name %q", name)
		}
	}
	for _, p := range []string{"srv/data", "/", "/nix", "/nix/store/x", "/state", "/state/x", "/var", "/srv/../nix", "/srv/", "/srv/my data", `/srv/d\\x2data`, "/srv/dätä", "/srv/a:b", `/srv/a\nb`} {
		if !refused(vol("data", `{size: 1G, mountPoint: "`+p+`"}`)) {
			t.Errorf("took the mount point %q", p)
		}
	}
	for _, key := range []string{"Type", "Label", "Encrypt", "Format", "SizeMinBytes", "SizeMaxBytes", "MountPoint"} {
		if !refused(vol("data", "{size: 1G, repart: {"+key+": x}}")) {
			t.Errorf("took the repart key %s", key)
		}
	}
	if refused(vol("data", "{size: 1G, mountPoint: /srv/My_data-2.0}")) {
		t.Error("refused the mount point /srv/My_data-2.0")
	}
}

// TestStorageEncryptionModes encrypts VAR as the node's SystemDisk says unless VAR's own mode
// says otherwise, and a volume as its own mode says.
func TestStorageEncryptionModes(t *testing.T) {
	for name, tc := range map[string]struct {
		systemDisk, extra   string
		state, var_, secret string
	}{
		"node none": {
			"spec: {disk: {path: /dev/vda}, encryption: {mode: none}, var: {size: 8G}}",
			"---\napiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: secret}\nspec: {mountPoint: /srv/secret, encryption: {mode: tpm2}}\n",
			"none", "none", "tpm2",
		},
		"var none": {"spec: {disk: {path: /dev/vda}, var: {encryption: {mode: none}}}", "", "tpm2", "none", ""},
		"default":  {"spec: {disk: {path: /dev/vda}}", "", "tpm2", "tpm2", ""},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := parseSet(t, strings.Replace(minimal, "spec: {disk: {path: /dev/vda}}", tc.systemDisk, 1)+tc.extra)
			if err != nil {
				t.Fatal(err)
			}
			sec, err := s.storage()
			if err != nil {
				t.Fatal(err)
			}
			if sec.Encryption != tc.state || sec.Volumes["var"].Encryption != tc.var_ || sec.Volumes["secret"].Encryption != tc.secret {
				t.Errorf("state %q, var %q, secret %q; want %q, %q, %q", sec.Encryption, sec.Volumes["var"].Encryption, sec.Volumes["secret"].Encryption, tc.state, tc.var_, tc.secret)
			}
			conf, ok := sec.Disks["system"].Repart["50-var.conf"]
			if !ok || strings.Contains(conf, "Encrypt=") != (tc.var_ == "tpm2") {
				t.Errorf("50-var.conf =\n%s", conf)
			}
		})
	}
}
