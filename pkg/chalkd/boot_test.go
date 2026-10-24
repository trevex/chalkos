package chalkd

import (
	"bytes"
	"context"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// TestStatusReportsTheBoot reads the boot from the ESP: the entry booted and whether it was
// blessed, an upgrade staged to boot next, and one the node fell back from, with what chalkd and
// the health check logged during its last boot, as the check recorded it; a record of another
// store or generation is not shown, nor UKIs of other images. It names each image by its root
// hash and generation, with the provenance the record of installed images knows.
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
		{"staged", []string{"chalkos_2.efi", "chalkos_3+3.efi"}, nil, "",
			&nodev1.BootStatus{Running: runs, Entry: "chalkos_2.efi", Blessed: true, Staged: &nodev1.BootImage{RootHash: next, Generation: 3}}},
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
				Staged: &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x33}, 32), Generation: 3}}
			if !proto.Equal(resp.Msg.Boot, want) {
				t.Errorf("boot = %v, want %v", resp.Msg.Boot, want)
			}
		})
	}
}
