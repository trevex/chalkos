package chalkd

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// TestStatusReportsTheBoot reads the boot from the ESP: the entry booted and whether it was
// blessed, an upgrade staged or active, which boots next, and one the node fell back from, with
// what chalkd and the health check logged during its last boot, as the check recorded it; a record
// of another store or generation is not shown, nor UKIs of other images. It names each image by
// its root hash and generation, with the provenance the record of installed images knows.
func TestStatusReportsTheBoot(t *testing.T) {
	journal := "2026-10-09T12:00:00+0000 node chalkd[401]: not healthy yet: units failed: broken.service\n" +
		"2026-10-09T12:00:30+0000 node chalkd[401]: rebooting\n"
	header := "usrhash=" + strings.Repeat("33", 32) + " generation=3\n"
	// The root hashes of the stores of generations 2, which the node runs, and 3, as writeESP
	// writes them.
	running, next := bytes.Repeat([]byte{0x32}, 32), bytes.Repeat([]byte{0x33}, 32)
	runs := &nodev1.BootImage{RootHash: running, Generation: 2, Revision: "a1b2c3d4e5f6", Tag: "v1.4.0"}
	for _, tc := range []struct {
		name   string
		files  []string
		others map[string]string
		record string
		want   *nodev1.BootStatus
	}{
		{"blessed", []string{"chalkos_1.efi", "chalkos_2.efi"}, nil, "",
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2.efi", Blessed: true}},
		{"counted", []string{"chalkos_1.efi", "chalkos_2+2-1.efi"}, nil, "",
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2+2-1.efi"}},
		{"active", []string{"chalkos_2.efi", "chalkos_3+3.efi"}, nil, "",
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2.efi", Blessed: true, Staged: &nodev1.BootImage{RootHash: next, Generation: 3}, StagedState: nodev1.SlotState_SLOT_STATE_ACTIVE}},
		// An image staged and not active boots from no entry; its staged name gives its generation.
		{"staged", []string{"chalkos_2.efi", ".staged-chalkos_3.efi"}, nil, "",
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2.efi", Blessed: true, Staged: &nodev1.BootImage{RootHash: next, Generation: 3}, StagedState: nodev1.SlotState_SLOT_STATE_STAGED}},
		// A rollback before the boot was found healthy stages the image before without tries; once
		// that image boots, the image it rolled back from is counted below it and boots no more.
		{"a rollback staged uncounted", []string{"chalkos_2+2-1.efi", "chalkos_3.efi"}, nil, "",
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2+2-1.efi", Staged: &nodev1.BootImage{RootHash: next, Generation: 3}, StagedState: nodev1.SlotState_SLOT_STATE_ACTIVE}},
		{"rolled back uncounted", []string{"chalkos_1+1-1.efi", "chalkos_2.efi"}, nil, "",
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2.efi", Blessed: true}},
		{"rolled back", []string{"chalkos_2.efi", "chalkos_3+0-3.efi"}, nil, header + journal,
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2.efi", Blessed: true, Failed: &nodev1.BootImage{RootHash: next, Generation: 3}, Journal: strings.Split(strings.TrimSpace(journal), "\n")}},
		{"rolled back, recorded for another generation", []string{"chalkos_2.efi", "chalkos_3+0-3.efi"}, nil, "usrhash=" + strings.Repeat("33", 32) + " generation=1\n" + journal,
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2.efi", Blessed: true, Failed: &nodev1.BootImage{RootHash: next, Generation: 3}}},
		{"rolled back, recorded for another store", []string{"chalkos_2.efi", "chalkos_3+0-3.efi"}, nil, "usrhash=" + strings.Repeat("34", 32) + " generation=3\n" + journal,
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2.efi", Blessed: true, Failed: &nodev1.BootImage{RootHash: next, Generation: 3}}},
		{"rolled back, recorded without a root hash", []string{"chalkos_2.efi", "chalkos_3+0-3.efi"}, nil, "generation=3\n" + journal,
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2.efi", Blessed: true, Failed: &nodev1.BootImage{RootHash: next, Generation: 3}}},
		// UKIs of other images on the ESP are not the node's upgrades.
		{"another image's UKIs", []string{"chalkos_2.efi"}, map[string]string{"rescue_8+0-3.efi": "0.9.0", "rescue_9+3.efi": "1.0.0"}, "",
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2.efi", Blessed: true}},
		{"booted from elsewhere", []string{"chalkos_1.efi"}, nil, "",
			&nodev1.BootStatus{Running: &nodev1.BootImage{RootHash: running, Revision: "a1b2c3d4e5f6", Tag: "v1.4.0"}, Error: "the boot loader entry chalkos_2.efi the node booted is not on the ESP"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := installedServer(t, section("", ""), false)
			if tc.record != "" {
				write(t, s.Paths.FailedBoot, tc.record)
			}
			write(t, filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord), `{"`+hex.EncodeToString(running)+`": {"revision": "a1b2c3d4e5f6", "tag": "v1.4.0"}}`)
			writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_2.efi", tc.files...)
			for name, version := range tc.others {
				write(t, filepath.Join(s.Paths.ESP, "EFI", "Linux", name), string(ukitest.UKI(map[string]string{"IMAGE_ID": "rescue", "IMAGE_VERSION": version}, "usrhash="+strings.Repeat("99", 32))))
			}
			resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(resp.Msg.Boot, tc.want) {
				t.Errorf("boot = %v, want %v", resp.Msg.Boot, tc.want)
			}
		})
	}
}

// TestStatusReportsTheBootsHealth checks the boot's health only when the request asks for it, also
// of a boot of a blessed entry, and says why the node is not healthy.
func TestStatusReportsTheBootsHealth(t *testing.T) {
	s, _ := installedServer(t, section("", ""), false)
	writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_2.efi", "chalkos_2.efi")
	checks := 0
	unhealthy := error(nil)
	s.Healthy = func(_ context.Context, ignore []string) error {
		checks++
		if len(ignore) != 0 {
			t.Errorf("the check ignores %q besides the image's units", ignore)
		}
		return unhealthy
	}
	status := func(check bool) *nodev1.BootStatus {
		t.Helper()
		resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{CheckHealth: check}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.Boot
	}
	if b := status(false); b.Health != nil || checks != 0 {
		t.Errorf("health %v unasked, %d checks", b.Health, checks)
	}
	if b := status(true); !b.Blessed || !proto.Equal(b.Health, &nodev1.BootHealth{Healthy: true}) {
		t.Errorf("boot %v, want it blessed and healthy", b)
	}
	unhealthy = errors.New("units failed: broken.service")
	if b := status(true); !b.Blessed || !proto.Equal(b.Health, &nodev1.BootHealth{Error: "units failed: broken.service"}) {
		t.Errorf("boot %v, want it blessed and not healthy", b)
	}
	if checks != 2 {
		t.Errorf("%d checks, want 2", checks)
	}
}

// TestStatusWithoutTheRecordOfImages names images by their root hashes alone when the record of
// installed images is missing or cannot be read: it is shown, never relied on.
func TestStatusWithoutTheRecordOfImages(t *testing.T) {
	for name, record := range map[string]string{"missing": "", "unreadable": "{"} {
		t.Run(name, func(t *testing.T) {
			s, _ := installedServer(t, section("", ""), false)
			if record != "" {
				if err := atomicfile.WriteFile(filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord), []byte(record), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_2.efi", "chalkos_2.efi", "chalkos_3+3.efi")
			resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			want := &nodev1.BootStatus{Running: &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x32}, 32), Generation: 2}, Entry: "chalkos_2.efi", Blessed: true,
				Staged: &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x33}, 32), Generation: 3}, StagedState: nodev1.SlotState_SLOT_STATE_ACTIVE}
			if !proto.Equal(resp.Msg.Boot, want) {
				t.Errorf("boot = %v, want %v", resp.Msg.Boot, want)
			}
		})
	}
}

// TestStatusReportsTheBootID names the boot by the ID the kernel gives it, also when the boot
// cannot be read otherwise, and names none when the ID cannot be read.
func TestStatusReportsTheBootID(t *testing.T) {
	s, _ := installedServer(t, section("", ""), false)
	if got := status(t, s).Boot.GetBootId(); got != "" {
		t.Errorf("boot ID without the file = %q", got)
	}
	s.Paths.BootID = filepath.Join(t.TempDir(), "boot_id")
	write(t, s.Paths.BootID, "9a1c7f0e-2b9c-4f7e-a0b1-5d6c7e8f9a0b\n")
	if got := status(t, s).Boot; got.GetBootId() != "9a1c7f0e-2b9c-4f7e-a0b1-5d6c7e8f9a0b" || got.GetError() == "" {
		t.Errorf("boot = %v, want the ID beside the error of an ESP without UKIs", got)
	}
}

// TestStatusReportsThePreviousImage names the image a rollback boots: the entry below the booted
// one, not bad, while STATE keeps its config slot, without which it would boot without config,
// and while no other image boots next.
func TestStatusReportsThePreviousImage(t *testing.T) {
	previous := bytes.Repeat([]byte{0x31}, 32)
	for _, tc := range []struct {
		name  string
		files []string
		slot  bool
		want  *nodev1.BootImage
	}{
		{"kept", []string{"chalkos_1.efi", "chalkos_2.efi"}, true, &nodev1.BootImage{RootHash: previous, Generation: 1}},
		{"before the boot is found healthy", []string{"chalkos_1.efi", "chalkos_2+2-1.efi"}, true, &nodev1.BootImage{RootHash: previous, Generation: 1}},
		{"without its config slot", []string{"chalkos_1.efi", "chalkos_2.efi"}, false, nil},
		{"bad", []string{"chalkos_1+0-3.efi", "chalkos_2.efi"}, true, nil},
		{"another image staged", []string{"chalkos_1.efi", "chalkos_2.efi", "chalkos_3+3.efi"}, true, nil},
		{"none", []string{"chalkos_2.efi"}, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := installedServer(t, section("", ""), false)
			writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_2.efi", tc.files...)
			if tc.slot {
				if err := slot.Write(slot.Dir(s.Loader.Slots, previous), slot.Stored{Version: "v1", Identity: []byte("{}")}); err != nil {
					t.Fatal(err)
				}
			}
			resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(resp.Msg.Boot.Previous, tc.want) {
				t.Errorf("previous = %v, want %v", resp.Msg.Boot.Previous, tc.want)
			}
		})
	}
}

// TestStatusReportsTheRollback names the image the last rollback rolled back to while the node
// runs or stages it, counted or, as a rollback before the boot was found healthy leaves it, not,
// and not once an upgrade replaced it.
func TestStatusReportsTheRollback(t *testing.T) {
	record := func(s *Server, h []byte) {
		write(t, filepath.Join(s.Paths.StateDir, "chalkd", "rollback"), hex.EncodeToString(h)+"\n")
	}
	staged := bytes.Repeat([]byte{0x33}, 32)
	for _, tc := range []struct {
		name   string
		files  []string
		record []byte
		want   *nodev1.BootImage
	}{
		{"runs it", []string{"chalkos_1.efi", "chalkos_2.efi"}, bytes.Repeat([]byte{0x32}, 32), &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x32}, 32), Generation: 2}},
		{"stages it", []string{"chalkos_2.efi", "chalkos_3+3.efi"}, staged, &nodev1.BootImage{RootHash: staged, Generation: 3}},
		{"stages it uncounted", []string{"chalkos_2+2-1.efi", "chalkos_3.efi"}, staged, &nodev1.BootImage{RootHash: staged, Generation: 3}},
		{"another image", []string{"chalkos_1.efi", "chalkos_2.efi"}, bytes.Repeat([]byte{0x31}, 32), nil},
		{"none", []string{"chalkos_1.efi", "chalkos_2.efi"}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := installedServer(t, section("", ""), false)
			writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_2.efi", tc.files...)
			if tc.record != nil {
				record(s, tc.record)
			}
			resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(resp.Msg.Boot.Rollback, tc.want) {
				t.Errorf("rollback = %v, want %v", resp.Msg.Boot.Rollback, tc.want)
			}
			// The image the node stages is named as staged too.
			if tc.want != nil && !bytes.Equal(tc.want.RootHash, resp.Msg.Boot.Running.RootHash) && !proto.Equal(resp.Msg.Boot.Staged, tc.want) {
				t.Errorf("staged = %v, want %v", resp.Msg.Boot.Staged, tc.want)
			}
		})
	}
}
