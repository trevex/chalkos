package gpt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// TestParseTable counts sizes in the table's sectors, which older sfdisk versions do not name:
// they are 512 bytes then.
func TestParseTable(t *testing.T) {
	for _, tc := range []struct {
		sector string
		want   int64
	}{{"", 2048 * 512}, {`"sectorsize": 512,`, 2048 * 512}, {`"sectorsize": 4096,`, 2048 * 4096}} {
		out := `{"partitiontable": {"label": "gpt", ` + tc.sector + ` "partitions": [{"node": "/dev/nvme0n1p3", "start": 2048, "size": 2048, "type": "8484680C-9521-48C6-9C11-B0720656F69E", "uuid": "AB", "name": "store_2ece03b0fa094b77"}]}}`
		parts, err := ParseTable("/dev/nvme0n1", []byte(out))
		if err != nil {
			t.Fatal(err)
		}
		if p := parts[0]; p.Bytes() != tc.want || p.Number != 3 || p.Type != "8484680c-9521-48c6-9c11-b0720656f69e" || p.UUID != "ab" {
			t.Errorf("%s: %+v has %d bytes, want %d", tc.sector, p, p.Bytes(), tc.want)
		}
	}
	if _, err := ParseTable("/dev/vda", []byte(`{"partitiontable": {"label": "dos"}}`)); err == nil || !strings.Contains(err.Error(), `a "dos" partition table, not a GPT`) {
		t.Errorf("a DOS table: %v", err)
	}
}

// TestSfdiskTakesTheLock runs sfdisk with the disk's lock, again while another program holds it,
// and gives up once LockWait passed.
func TestSfdiskTakesTheLock(t *testing.T) {
	locked := &tool.Error{Command: "sfdisk", Code: 1, Stderr: "/dev/vda: device already locked, waiting to get lock ... "}
	r := &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "sfdisk", Err: locked, Times: 2}}}
	s := Sfdisk{Run: r, Disk: "/dev/vda", LockWait: time.Minute}
	if _, err := s.Exec(context.Background(), "--delete", "/dev/vda", "3"); err != nil {
		t.Fatal(err)
	}
	if len(r.Calls) != 3 || r.Calls[2] != "sfdisk --lock=nonblock --delete /dev/vda 3" {
		t.Errorf("commands = %q, want sfdisk run again until it took the lock", r.Calls)
	}

	r = &tooltest.Runner{Rules: []tooltest.Rule{{Prefix: "sfdisk", Err: locked}}}
	s = Sfdisk{Run: r, Disk: "/dev/vda", LockWait: 10 * time.Millisecond}
	if _, err := s.ReadTable(context.Background()); !errors.Is(err, locked) || !strings.Contains(err.Error(), "kept /dev/vda locked") {
		t.Errorf("err = %v, want sfdisk to give up on the lock", err)
	}
}
