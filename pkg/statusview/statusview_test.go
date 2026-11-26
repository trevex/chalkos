package statusview

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/internal/golden"
)

// fakeInventory says what chalkctl's inventory says of a node, in words the test can find.
type fakeInventory struct{}

func (fakeInventory) ConfigState(s *nodev1.StatusResponse) string {
	return "inventory on " + s.ConfigVersion
}
func (fakeInventory) StagedConfigState(*nodev1.StatusResponse) string {
	return "inventory on the staged"
}
func (fakeInventory) PlatformLine(p string) string        { return "platform " + p + " (the inventory's)" }
func (fakeInventory) ImageLine(*nodev1.BootStatus) string { return "image line of the inventory" }
func (fakeInventory) Compare(*nodev1.StatusResponse) Comparison {
	return Comparison{Config: Differs, StagedConfig: Same, Image: Differs, Platform: Same}
}

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
	err := Write(&b, richStatus(), Options{Node: "n1", Inventory: fakeInventory{}, ModulePolicy: &nodev1.ModulePolicyStatus{State: nodev1.ModulePolicyState_MODULE_POLICY_STATE_ENFORCED, Version: "0.5.0"}, Layers: func(w io.Writer) error {
		_, err := fmt.Fprintln(w, "layer section")
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	golden.Check(t, "testdata/status.golden", b.String())
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

// A section the node could not read shows in its place, with why, and counts as a problem; a
// section this program does not know of shows at the end.
func TestWriteSectionErrors(t *testing.T) {
	s := richStatus()
	s.Volumes, s.Disks, s.FailedUnits, s.PendingConfig = nil, nil, nil, nil
	s.Errors = []*nodev1.SectionError{
		{Section: "volumes", Message: "read the recorded storage: parse storage.json: unexpected end of JSON input"},
		{Section: "disks", Message: "parse /run/chalkos/storage-status.json: unexpected end of JSON input"},
		{Section: "pending config", Message: "read the pending config's version: permission denied"},
		{Section: "failed units", Message: "systemctl did not answer within 5s"},
		{Section: "later", Message: "a section of a newer node"},
	}
	var b bytes.Buffer
	if err := Write(&b, s, Options{Node: "n1"}); err != nil {
		t.Fatal(err)
	}
	golden.Check(t, "testdata/status-section-errors.golden", b.String())
	want := []string{"volumes unknown", "disks unknown", "pending config unknown", "failed units unknown", "later unknown"}
	if got := Problems(s, Options{}); !slices.Equal(got[:len(want)], want) {
		t.Errorf("problems = %q, want them to start with %q", got, want)
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
	golden.Check(t, "testdata/disks.golden", b.String())
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

func TestModulePolicyLine(t *testing.T) {
	for _, tc := range []struct {
		in   *nodev1.ModulePolicyStatus
		want string
	}{
		{&nodev1.ModulePolicyStatus{State: nodev1.ModulePolicyState_MODULE_POLICY_STATE_ENFORCED, Version: "0.5.0"}, "module policy: enforced (version 0.5.0)"},
		{&nodev1.ModulePolicyStatus{State: nodev1.ModulePolicyState_MODULE_POLICY_STATE_NOT_ENFORCED, Detail: "the image is not signed"}, "module policy: not enforced (no Secure Boot): the image is not signed"},
		{&nodev1.ModulePolicyStatus{State: nodev1.ModulePolicyState_MODULE_POLICY_STATE_FAILED, Detail: "the kernel refused the module policy: required key not available"}, "module policy: failed: the kernel refused the module policy: required key not available"},
		{&nodev1.ModulePolicyStatus{State: nodev1.ModulePolicyState_MODULE_POLICY_STATE_TAMPERED, Detail: "IPE is permissive"}, "module policy: tampered: IPE is permissive; the node's journal holds the audit records"},
		{&nodev1.ModulePolicyStatus{State: nodev1.ModulePolicyState_MODULE_POLICY_STATE_OFF}, "module policy: off (the image leaves it out)"},
		{nil, "module policy: unknown"},
	} {
		if got := ModulePolicyLine(tc.in); got != tc.want {
			t.Errorf("ModulePolicyLine(%v) = %q, want %q", tc.in, got, tc.want)
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

func TestWriteActivation(t *testing.T) {
	var out strings.Builder
	writeActivation(&out, &nodev1.ConfigActivation{
		CompletedVersion: "v1", TargetVersion: "v2", State: nodev1.ActivationState_ACTIVATION_STATE_FAILED,
		Step: "a later batch", FailedStep: "effect batch 2 (resolver)",
	})
	want := "activated config v1\nactivation of v2 failed: effect batch 2 (resolver)\n"
	if out.String() != want {
		t.Fatalf("status = %q, want %q", out.String(), want)
	}
	out.Reset()
	writeActivation(&out, &nodev1.ConfigActivation{State: nodev1.ActivationState_ACTIVATION_STATE_IN_PROGRESS, Unwritten: true})
	want = "activated config unrecorded\nan activation is in progress\nthe config activation record could not be written, so it may be out of date; the journal says why\n"
	if out.String() != want {
		t.Fatalf("status = %q, want %q", out.String(), want)
	}
}

// The problems of a status are those its lines show, in their order, and none for a healthy node.
func TestProblems(t *testing.T) {
	got := Problems(richStatus(), Options{Inventory: fakeInventory{}, ModulePolicy: &nodev1.ModulePolicyStatus{State: nodev1.ModulePolicyState_MODULE_POLICY_STATE_TAMPERED}})
	want := []string{
		"trial rollback failed", "trial record unreadable", "config not the inventory's", "boot not found healthy yet",
		"upgrade to deaddeaddead failed", "image not the inventory's", "module policy tampered", "volume data missing",
		"disk data not found", "output motd failed", "certificate node expires in 20 days",
		"certificate client share unreadable: parse the client share", "unit demo.service failed",
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems = %q, want %q", got, want)
	}
	healthy := &nodev1.StatusResponse{Boot: &nodev1.BootStatus{Blessed: true}, Time: &nodev1.TimeStatus{Synchronised: true}}
	if got := Problems(healthy, Options{}); len(got) != 0 {
		t.Errorf("a healthy node has the problems %q", got)
	}
	for problems, want := range map[int]string{0: "n1: healthy", 1: "n1: 1 problem: p1", 2: "n1: 2 problems: p1, p2"} {
		var p []string
		for i := range problems {
			p = append(p, fmt.Sprintf("p%d", i+1))
		}
		if got := SummaryLine("n1", p); got != want {
			t.Errorf("SummaryLine(%q) = %q, want %q", p, got, want)
		}
	}
}
