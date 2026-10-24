package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/tool"
)

// otherDisk has a serial of its own, so a reference can find it instead of the pinned disk.
var otherDisk = testDisk{"vdc", "253:32", 2 << 30 / 512, map[string]string{"ID_PATH": "pci-0000:00:06.0", "ID_SERIAL": "chalk-other"}}

// writePins pins the data disk by its serial, as an install of writeStorage's section does.
func writePins(t *testing.T, b *Storage) {
	t.Helper()
	pins := `{"disks": {
	  "system": {"ref": "/dev/vda", "identity": {"path": "pci-0000:00:04.0", "size": 17179869184, "type": "hdd"}, "partitions": {"var": "` + varUUID + `"}},
	  "data": {"ref": {"serial": "chalk-data"}, "identity": {"serial": "chalk-data", "path": "pci-0000:00:05.0", "size": 2147483648, "type": "hdd"}, "partitions": {"data": "` + dataUUID + `"}}
	}}`
	if err := os.WriteFile(filepath.Join(b.StorageDir(), "disks.json"), []byte(pins), 0o600); err != nil {
		t.Fatal(err)
	}
}

// factsInput is the input of a command fact with the Volume documents given by name, each a
// spec as JSON.
func factsInputOf(t *testing.T, secret string, volumes map[string]string) []byte {
	t.Helper()
	docs := map[string]any{}
	for name, spec := range volumes {
		var s any
		if err := json.Unmarshal([]byte(spec), &s); err != nil {
			t.Fatal(err)
		}
		docs[name] = map[string]any{"apiVersion": "storage.chalkos.dev/v1alpha1", "kind": "Volume", "metadata": map[string]any{"name": name}, "spec": s}
	}
	in, err := json.Marshal(map[string]any{
		"config":         map[string]any{"storage.chalkos.dev/v1alpha1": map[string]any{"Volume": docs}},
		"oldConfig":      map[string]any{},
		"fallbackSecret": secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func diskFacts(t *testing.T, b *Storage, spec string) DiskFact {
	t.Helper()
	out, err := b.Facts(context.Background(), FactDisks, factsInputOf(t, "", map[string]string{
		"data": `{"disk": ` + spec + `}`, "plain": `{"disk": null}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]DiskFact
	if err := json.Unmarshal(out, &facts); err != nil {
		t.Fatal(err)
	}
	if _, ok := facts["plain"]; ok || len(facts) != 1 {
		t.Errorf("facts = %+v, want the data volume's alone", facts)
	}
	return facts["data"]
}

// A changed reference is resolved, an unchanged one is not; a pinned disk is looked for, whether
// the reference changed or not.
func TestDiskFacts(t *testing.T) {
	selector := func(serial string) string {
		return `{"path": null, "model": null, "serial": "` + serial + `", "wwn": null, "size": null, "type": null}`
	}
	for _, tc := range []struct {
		name, spec string
		prepare    func(t *testing.T, b *Storage)
		want       DiskFact
	}{
		{"unchanged", selector("chalk-data"), nil, DiskFact{Ref: `serial "chalk-data"`, Pinned: true, Present: true}},
		{"changed, finding the pinned disk", `{"path": "/dev/vdb"}`, nil, DiskFact{Ref: "/dev/vdb", Pinned: true, Changed: true, Same: true, Present: true}},
		{"changed, finding another disk", selector("chalk-other"), nil, DiskFact{Ref: `serial "chalk-other"`, Pinned: true, Changed: true, Present: true}},
		{"changed, finding none", selector("chalk-missing"), nil, DiskFact{Ref: `serial "chalk-missing"`, Pinned: true, Changed: true, ErrorKind: "noDisk", Present: true}},
		{"the pinned disk unplugged", selector("chalk-data"), func(t *testing.T, b *Storage) {
			if err := os.RemoveAll(filepath.Join(b.Host.SysRoot, "block", "vdb")); err != nil {
				t.Fatal(err)
			}
		}, DiskFact{Ref: `serial "chalk-data"`, Pinned: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newTestBoot(t, &fakeRunner{}, bootDisk, dataDisk, otherDisk)
			writeStorage(t, b, "recovery-key")
			writePins(t, b)
			if tc.prepare != nil {
				tc.prepare(t, b)
			}
			got := diskFacts(t, b, tc.spec)
			if !strings.Contains(got.Identity, `serial "chalk-data"`) {
				t.Errorf("identity = %q", got.Identity)
			}
			if tc.want.ErrorKind != "" && !strings.Contains(got.Error, "no disk matches") {
				t.Errorf("error = %q", got.Error)
			}
			if tc.want.Changed && tc.want.Error == "" && !tc.want.Same && tc.want.ErrorKind == "" && !strings.Contains(got.Found, "chalk-other") {
				t.Errorf("found = %q", got.Found)
			}
			got.Identity, got.Found, got.Error = "", "", ""
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("fact = %+v, want %+v", got, tc.want)
			}
		})
	}
	// A disk the node has not pinned is a new one, which nothing compares with.
	b := newTestBoot(t, &fakeRunner{}, bootDisk, dataDisk)
	writeStorage(t, b, "recovery-key")
	if got := diskFacts(t, b, selector("chalk-data")); !reflect.DeepEqual(got, DiskFact{Ref: `serial "chalk-data"`}) {
		t.Errorf("an unpinned disk: %+v", got)
	}
}

// The fallback secret is checked against STATE's password keyslot, and only when one was sent and
// the node has a fallback.
func TestFallbackFacts(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name     string
		secret   string
		fallback string
		rules    []rule
		want     FallbackFact
		calls    int
	}{
		{"no secret", "", "recovery-key", nil, FallbackFact{}, 0},
		{"a matching secret", "s3cret", "recovery-key", nil, FallbackFact{Sent: true, Matches: &yes}, 2},
		{"a wrong secret", "wrong", "recovery-key", []rule{{prefix: "cryptsetup open", err: &tool.Error{Command: "cryptsetup open", Code: 2}}}, FallbackFact{Sent: true, Matches: &no}, 2},
		{"no fallback", "s3cret", "none", nil, FallbackFact{Sent: true}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{rules: append(tc.rules, rule{prefix: "cryptsetup luksDump", out: `{"keyslots": {"0": {}, "2": {}}, "tokens": {"0": {"keyslots": ["0"]}}}`})}
			b := newTestBoot(t, r, bootDisk, dataDisk)
			writeStorage(t, b, tc.fallback)
			writePins(t, b)
			out, err := b.Facts(context.Background(), FactFallback, factsInputOf(t, tc.secret, nil))
			if err != nil {
				t.Fatal(err)
			}
			var got FallbackFact
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("fact = %s, want %+v", out, tc.want)
			}
			if len(r.calls) != tc.calls || tc.calls > 0 && r.calls[1] != "cryptsetup open --test-passphrase --key-slot=2 --key-file=- /dev/disk/chalk-boot/var" {
				t.Errorf("ran %v", r.calls)
			}
		})
	}
	// A check that fails otherwise than for the secret fails the fact.
	r := &fakeRunner{rules: []rule{{prefix: "cryptsetup luksDump", err: &tool.Error{Command: "cryptsetup luksDump", Code: 4, Stderr: "Device does not exist."}}}}
	b := newTestBoot(t, r, bootDisk)
	writeStorage(t, b, "recovery-key")
	if _, err := b.Facts(context.Background(), FactFallback, factsInputOf(t, "s3cret", nil)); err == nil || !strings.Contains(err.Error(), "read the keyslots of /dev/disk/chalk-boot/var") {
		t.Errorf("err = %v", err)
	}
	// Without a recorded section there is nothing to check against.
	b = newTestBoot(t, &fakeRunner{}, bootDisk)
	out, err := b.Facts(context.Background(), FactFallback, factsInputOf(t, "s3cret", nil))
	if err != nil || string(out) != `{"sent":true,"matches":null}` {
		t.Errorf("without storage.json: %s, %v", out, err)
	}
}

func TestFactsRefusesAnUnknownFact(t *testing.T) {
	b := newTestBoot(t, &fakeRunner{}, bootDisk)
	if _, err := b.Facts(context.Background(), "partitions", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), `no fact "partitions"`) {
		t.Errorf("err = %v", err)
	}
}

func TestPasswordSlots(t *testing.T) {
	for dump, want := range map[string][]string{
		`{"keyslots": {"0": {"type": "luks2"}, "2": {"type": "luks2"}}, "tokens": {"0": {"type": "systemd-tpm2", "keyslots": ["0"]}}}`:                                    {"2"},
		`{"keyslots": {"0": {}, "1": {}, "10": {}}, "tokens": {"0": {"type": "systemd-tpm2", "keyslots": ["0"]}, "1": {"type": "systemd-recovery", "keyslots": ["10"]}}}`: {"1"},
		`{"keyslots": {"0": {}}, "tokens": {"0": {"type": "systemd-tpm2", "keyslots": ["0"]}}}`:                                                                           nil,
		`{"keyslots": {"3": {}, "1": {}}, "tokens": {}}`: {"1", "3"},
	} {
		got, err := passwordSlots([]byte(dump))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("passwordSlots(%s) = %v, %v, want %v", dump, got, err, want)
		}
	}
	if _, err := passwordSlots([]byte(`{"keyslots": {"0; rm": {}}}`)); err == nil {
		t.Error("accepted a keyslot that is not a number")
	}
}
