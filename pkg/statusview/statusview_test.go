package statusview

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/name, or writes it there with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("the output differs from %s; run go test ./pkg/statusview -run %s -update and review the diff:\n%s", path, t.Name(), got)
	}
}

// fakeInventory says what chalkctl's inventory says of a node, in words the test can find.
type fakeInventory struct{}

func (fakeInventory) ConfigState(v string) string { return "inventory on " + v }
func (fakeInventory) StagedConfigState(*nodev1.StatusResponse) string {
	return "inventory on the staged"
}
func (fakeInventory) PlatformLine(p string) string        { return "platform " + p + " (the inventory's)" }
func (fakeInventory) ImageLine(*nodev1.BootStatus) string { return "image line of the inventory" }

// richStatus is a node's status that reaches each line Write writes.
func richStatus() *nodev1.StatusResponse {
	running := &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x81, 0xbd}, 16), Generation: 7, Revision: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", Tag: "v1.4.0"}
	return &nodev1.StatusResponse{
		ConfigVersion: "3fa9c1e2b7d04e5f",
		Trial: &nodev1.ConfigTrial{
			Version: "3fa9c1e2b7d04e5f", PreviousVersion: "77aa01bc23de45f6", Deadline: timestamppb.New(time.Date(2026, 11, 13, 15, 4, 0, 0, time.UTC)),
			IgnoreUnits: []string{"late.service"}, Error: "output demo failed",
		},
		TrialError:            "parse chalkd/trial: unexpected end of JSON input",
		PreviousConfigVersion: "77aa01bc23de45f6",
		StagedConfigVersion:   "77aa01bc23de45f6",
		PendingConfig:         &nodev1.PendingConfig{Version: "9c0ffee12345abcd", Since: timestamppb.New(time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC))},
		Platform:              "metal",
		Boot: &nodev1.BootStatus{
			Running: running, Entry: "chalkos_7+2-1.efi",
			Staged: &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x3f, 0xa9}, 16), Generation: 8, Tag: "v1.5.0"}, StagedState: nodev1.SlotState_SLOT_STATE_STAGED,
			Previous: &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x12, 0x34}, 16), Generation: 6, Revision: "0123456789abcdef0123-dirty"},
			Failed:   &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0xde, 0xad}, 16), Generation: 9},
			Journal:  []string{"Nov 13 14:00:01 n1 chalkd[412]: health check failed", "Nov 13 14:00:02 n1 systemd[1]: Reached target Reboot."},
		},
		Volumes: []*nodev1.VolumeStatus{
			{Name: "var", Disk: "system", MountPoint: "/var", Present: true, Mounted: true},
			{Name: "data", Disk: "data", MountPoint: "/var/lib/data"},
		},
		Disks: []*nodev1.DiskStatus{
			{Name: "system", Device: "/dev/vda"},
			{Name: "data", Error: "no disk matches the selector serial=S64DNF0R"},
		},
		Outputs: []*nodev1.OutputStatus{
			{Name: "web-conf", Hash: strings.Repeat("ab", 32)},
			{Name: "motd", Error: "its renderer failed (exit 2); see the node's journal"},
		},
		Certificates: []*nodev1.CertificateStatus{
			{Name: "node", NotAfter: timestamppb.New(time.Date(2027, 10, 8, 12, 0, 0, 0, time.UTC)), Problem: "expires in 20 days; renew it with chalkctl node renew <node>"},
			{Name: "client share", Problem: "unreadable: parse the client share"},
		},
		Trust: []*nodev1.TrustStatus{
			{Name: "OS CA", Fingerprints: []string{strings.Repeat("a", 64)}, Issuing: strings.Repeat("a", 64)},
			{Name: "node CA", Fingerprints: []string{strings.Repeat("c", 64), strings.Repeat("d", 64)}, Issuing: strings.Repeat("d", 64)},
		},
		Time:        &nodev1.TimeStatus{Synchronised: true, Source: "10.0.2.2", OffsetSeconds: -0.0000123},
		FailedUnits: []string{"demo.service"},
	}
}

func TestWrite(t *testing.T) {
	var b bytes.Buffer
	err := Write(&b, richStatus(), Options{Node: "n1", Inventory: fakeInventory{}, Layers: func(w io.Writer) error {
		_, err := fmt.Fprintln(w, "layer section")
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "status.golden", b.String())
}

// Without an inventory, as on the console, the lines compare nothing.
func TestWriteWithoutInventory(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, richStatus(), Options{Node: "n1"}); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"config " + richStatus().ConfigVersion + "\n", "platform metal\n", "staged config 77aa01bc23de45f6\n"} {
		if !strings.Contains(b.String(), line) {
			t.Errorf("no %q in\n%s", line, b.String())
		}
	}
	if strings.Contains(b.String(), "inventory") {
		t.Errorf("an inventory's words without one:\n%s", b.String())
	}
}

func TestWriteDisks(t *testing.T) {
	var b bytes.Buffer
	if err := WriteDisks(&b, []*nodev1.Disk{
		{Device: "/dev/vda", Size: 16 << 30, Type: "hdd", Model: "QEMU HARDDISK", Serial: "chalk-sys", Usage: "boot", Partitions: []*nodev1.Partition{
			{Device: "/dev/vda1", Size: 512 << 20, Label: "esp", Content: "vfat"},
		}},
		{Device: "/dev/nvme0n1", Size: 1000204886016, Type: "ssd", Model: "Samsung SSD 980", Serial: "S64DNF0R", Wwn: "eui.002538b1", Usage: "volume data"},
	}); err != nil {
		t.Fatal(err)
	}
	golden(t, "disks.golden", b.String())
}

// status names each output with the start of its hash and why its last render failed, or that
// none rendered it yet.
func TestOutputLine(t *testing.T) {
	for _, tc := range []struct {
		o    *nodev1.OutputStatus
		want string
	}{
		{&nodev1.OutputStatus{Name: "web-conf", Hash: strings.Repeat("ab", 32)}, "web-conf\tabababababab\trendered"},
		{&nodev1.OutputStatus{Name: "web-conf", Hash: strings.Repeat("ab", 32), Error: "its check refused the rendered files (exit 1); see the node's journal"}, "web-conf\tabababababab\tfailed: its check refused the rendered files (exit 1); see the node's journal"},
		{&nodev1.OutputStatus{Name: "web-conf", Error: "its renderer failed (exit 2); see the node's journal"}, "web-conf\tnone\tfailed: its renderer failed (exit 2); see the node's journal"},
		{&nodev1.OutputStatus{Name: "web-conf"}, "web-conf\tnone\tnot rendered yet"},
	} {
		if got := OutputLine(tc.o); got != tc.want {
			t.Errorf("OutputLine(%v) = %q, want %q", tc.o, got, tc.want)
		}
	}
}

// TestBootLines names each image by its root hash, with the tag and revision the node knows.
func TestBootLines(t *testing.T) {
	running := &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x81, 0xbd}, 16), Generation: 7, Revision: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", Tag: "v1.4.0"}
	next := &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x3f, 0xa9}, 16), Generation: 8, Tag: "v1.5.0"}
	for _, tc := range []struct {
		boot *nodev1.BootStatus
		want []string
	}{
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true}, []string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7+2-1.efi"}, []string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7+2-1.efi, not found healthy yet"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Staged: next, StagedState: nodev1.SlotState_SLOT_STATE_ACTIVE},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "upgrade to 3fa93fa93fa9 (v1.5.0) active; it boots next"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Staged: next, StagedState: nodev1.SlotState_SLOT_STATE_STAGED},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "upgrade to 3fa93fa93fa9 (v1.5.0) staged; node activate makes it boot next"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Staged: next, Rollback: next},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "rollback to 3fa93fa93fa9 (v1.5.0) staged; it boots next"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Previous: &nodev1.BootImage{RootHash: next.RootHash, Generation: 6, Tag: "v1.5.0"}},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "previous image 3fa93fa93fa9 (v1.5.0) (upgrade --rollback boots it)"}},
		{&nodev1.BootStatus{Running: running, Entry: "chalkos_7.efi", Blessed: true, Failed: next, Journal: []string{"a", "b"}},
			[]string{"image 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6), booted from chalkos_7.efi", "upgrade to 3fa93fa93fa9 (v1.5.0) failed: rolled back to 81bd81bd81bd (v1.4.0, a1b2c3d4e5f6)", "  a", "  b"}},
		{&nodev1.BootStatus{Running: &nodev1.BootImage{RootHash: next.RootHash}, Error: "no UKI on the ESP boots the running store"}, []string{"image 3fa93fa93fa9; its boot is unknown: no UKI on the ESP boots the running store"}},
		{nil, nil},
	} {
		if got := BootLines(tc.boot); !slices.Equal(got, tc.want) {
			t.Errorf("BootLines(%v) = %q, want %q", tc.boot, got, tc.want)
		}
	}
}

func TestTimeLine(t *testing.T) {
	for _, tc := range []struct {
		status *nodev1.TimeStatus
		want   string
	}{
		{&nodev1.TimeStatus{Synchronised: true, Source: "10.0.2.2", OffsetSeconds: -0.0000123}, "time: synchronised to 10.0.2.2, offset -0.000012 s"},
		{&nodev1.TimeStatus{}, "time: not synchronised; certificates are checked against this clock"},
		{&nodev1.TimeStatus{Error: "506 Cannot talk to daemon"}, "time: unknown: 506 Cannot talk to daemon"},
	} {
		if got := TimeLine(tc.status); got != tc.want {
			t.Errorf("TimeLine = %q, want %q", got, tc.want)
		}
	}
}

func TestCertificateLine(t *testing.T) {
	expires := time.Date(2027, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		status *nodev1.CertificateStatus
		want   string
	}{
		{&nodev1.CertificateStatus{Name: "node", NotAfter: timestamppb.New(expires)}, "  node\texpires 2027-10-08"},
		{&nodev1.CertificateStatus{Name: "node", NotAfter: timestamppb.New(expires), Problem: "expired; renew it with chalkctl node renew <node>"}, "  node\texpires 2027-10-08\texpired; renew it with chalkctl node renew n1"},
		{&nodev1.CertificateStatus{Name: "client share", Problem: "unreadable: parse the client share"}, "  client share\texpiry unknown\tunreadable: parse the client share"},
	} {
		if got := CertificateLine(tc.status, "n1"); got != tc.want {
			t.Errorf("CertificateLine = %q, want %q", got, tc.want)
		}
	}
}

func TestTrustLine(t *testing.T) {
	line := TrustLine(&nodev1.TrustStatus{Name: "node CA", Fingerprints: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}, Issuing: strings.Repeat("b", 64)})
	if line != "  node CA\taaaaaaaaaaaaaaaa, bbbbbbbbbbbbbbbb (issues)" {
		t.Errorf("trust line %q", line)
	}
}

func TestTrialLine(t *testing.T) {
	tr := &nodev1.ConfigTrial{Version: "v2", PreviousVersion: "v1", Deadline: timestamppb.New(time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))}
	if got, want := TrialLine(tr), "config v2 on trial until 2026-10-29T12:00:00Z: it rolls back to config v1 unless config confirm confirms it"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	tr.IgnoreUnits = []string{"late.service", "sync.timer"}
	if got := TrialLine(tr); !strings.HasSuffix(got, " confirms it; its health check leaves out late.service, sync.timer") {
		t.Errorf("with ignored units: %q", got)
	}
	tr.Error = "output demo failed"
	if got := TrialLine(tr); !strings.HasSuffix(got, "; an attempt to roll the config back failed and is tried again each minute: output demo failed") {
		t.Errorf("with an error: %q", got)
	}
	tr.RollbackChanged = true
	if got := TrialLine(tr); !strings.HasSuffix(got, "; an attempt to roll the config back failed after it changed the node and is tried again each minute: output demo failed; config confirm no longer keeps the config, and config rollback finishes the rollback now") {
		t.Errorf("with a rollback that changed the node: %q", got)
	}
}

// TestImageName names an image by its root hash's first 12 hex digits, then the tag and revision
// it was built from as far as they are known; a dirty revision says so.
func TestImageName(t *testing.T) {
	root := bytes.Repeat([]byte{0x3f, 0xa9}, 16)
	for _, tc := range []struct {
		revision, tag, want string
	}{
		{"", "", "3fa93fa93fa9"},
		{"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", "v1.5.0", "3fa93fa93fa9 (v1.5.0, a1b2c3d4e5f6)"},
		{"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", "", "3fa93fa93fa9 (a1b2c3d4e5f6)"},
		{"", "v1.5.0", "3fa93fa93fa9 (v1.5.0)"},
		{"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678-dirty", "", "3fa93fa93fa9 (a1b2c3d4e5f6-dirty)"},
		{"abc", "", "3fa93fa93fa9 (abc)"},
	} {
		if got := ImageName(root, tc.revision, tc.tag); got != tc.want {
			t.Errorf("ImageName(%q, %q) = %q, want %q", tc.revision, tc.tag, got, tc.want)
		}
	}
	if got := ImageName(nil, "", ""); got != "unknown" {
		t.Errorf("the name of an image of no root hash = %q", got)
	}
}
