package chalkd

import (
	"context"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/internal/testwait"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// sectionErrors are the sections a status names as unreadable, by name.
func sectionErrors(st *nodev1.StatusResponse) map[string]string {
	errs := map[string]string{}
	for _, e := range st.Errors {
		errs[e.Section] = e.Message
	}
	return errs
}

// A storage status that cannot be parsed leaves the disks out and says why; the other sections
// answer all the same.
func TestStatusWithAnUnreadableStorageStatus(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	write(t, s.Paths.StorageStatus, "{")
	write(t, s.Paths.MountInfo, "30 1 0:27 / / rw - tmpfs tmpfs rw\n")
	r.Rules = append([]tooltest.Rule{{Prefix: "systemctl list-units --state=failed", Out: "demo.service loaded failed failed Demo\n"}}, r.Rules...)
	st := status(t, s)
	errs := sectionErrors(st)
	if len(errs) != 1 || !strings.Contains(errs["disks"], "parse "+s.Paths.StorageStatus) {
		t.Errorf("section errors = %v, want the disks' alone", errs)
	}
	if st.Platform == "" || len(st.Volumes) == 0 || len(st.FailedUnits) != 1 || st.Boot == nil || st.Time == nil {
		t.Errorf("status = %v, want the other sections", st)
	}
}

// hangingRunner runs every command as Runner does, but systemctl's list of failed units, which
// waits for its context to end, as on a systemd that does not answer.
type hangingRunner struct{ tool.Runner }

func (h hangingRunner) Exec(ctx context.Context, c tool.Cmd) ([]byte, error) {
	if c.Name == "systemctl" && len(c.Args) > 0 && c.Args[0] == "list-units" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return h.Runner.Exec(ctx, c)
}

// A systemctl that does not answer ends at the status's bound; the status names the failed units
// as unreadable and answers with the rest.
func TestStatusWithASystemctlThatHangs(t *testing.T) {
	testwait.Bubble(t, func(t *testing.T) {
		s, _ := installedServer(t, section("", ""), false)
		s.Run = hangingRunner{s.Run}
		start := time.Now()
		st := status(t, s)
		if took := time.Since(start); took > statusTimeout+time.Second {
			t.Errorf("status took %v, want at most %v", took, statusTimeout)
		}
		if errs := sectionErrors(st); len(errs) != 1 || errs["failed units"] != "systemctl did not answer within 5s" {
			t.Errorf("section errors = %v, want the failed units' alone", errs)
		}
		if st.Platform == "" || len(st.Volumes) == 0 || st.Boot == nil {
			t.Errorf("status = %v, want the other sections", st)
		}
	})
}
