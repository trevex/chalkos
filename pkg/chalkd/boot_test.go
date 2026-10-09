package chalkd

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// TestStatusReportsTheBoot reads the boot from the ESP: the entry booted and whether it was
// blessed, an upgrade staged to boot next, and one the node fell back from, with what chalkd and
// the health check logged during its last boot, as the check recorded it; a record of another
// version is not shown.
func TestStatusReportsTheBoot(t *testing.T) {
	journal := "2026-10-09T12:00:00+0000 node chalkd[401]: not healthy yet: units failed: broken.service\n" +
		"2026-10-09T12:00:30+0000 node chalkd[401]: rebooting\n"
	for _, tc := range []struct {
		name   string
		files  []string
		record string
		want   *nodev1.BootStatus
	}{
		{"blessed", []string{"chalkos_0.1.0.efi", "chalkos_0.2.0.efi"}, "",
			&nodev1.BootStatus{Version: "0.2.0", Entry: "chalkos_0.2.0.efi", Blessed: true}},
		{"counted", []string{"chalkos_0.1.0.efi", "chalkos_0.2.0+2-1.efi"}, "",
			&nodev1.BootStatus{Version: "0.2.0", Entry: "chalkos_0.2.0+2-1.efi"}},
		{"staged", []string{"chalkos_0.2.0.efi", "chalkos_0.3.0+3.efi"}, "",
			&nodev1.BootStatus{Version: "0.2.0", Entry: "chalkos_0.2.0.efi", Blessed: true, Staged: "0.3.0"}},
		{"rolled back", []string{"chalkos_0.2.0.efi", "chalkos_0.3.0+0-3.efi"}, "version 0.3.0\n" + journal,
			&nodev1.BootStatus{Version: "0.2.0", Entry: "chalkos_0.2.0.efi", Blessed: true, Failed: "0.3.0", Journal: strings.Split(strings.TrimSpace(journal), "\n")}},
		{"rolled back, recorded another time", []string{"chalkos_0.2.0.efi", "chalkos_0.3.0+0-3.efi"}, "version 0.2.1\n" + journal,
			&nodev1.BootStatus{Version: "0.2.0", Entry: "chalkos_0.2.0.efi", Blessed: true, Failed: "0.3.0"}},
		{"booted from elsewhere", []string{"chalkos_0.1.0.efi"}, "",
			&nodev1.BootStatus{Version: "0.2.0", Error: "the boot loader entry chalkos_0.2.0.efi the node booted is not on the ESP"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := installedServer(t, section("", ""), false)
			if tc.record != "" {
				write(t, s.Paths.FailedBoot, tc.record)
			}
			write(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=0.2.0\n")
			writeESP(t, s.Paths.ESP, s.Paths.EFIVars, s.Paths.Cmdline, "chalkos_0.2.0.efi", tc.files...)
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
