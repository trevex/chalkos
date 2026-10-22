package rules

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// The built-in kinds' rules reach their schemas and compile: the storage kinds', Node's and
// Kubernetes's.
func TestBuiltinRulesCompile(t *testing.T) {
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/builtin")
	if err != nil {
		t.Fatal(err)
	}
	set, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, p := range set.Programs {
		count[p.Kind.APIVersion+" "+p.Kind.Kind]++
	}
	for kind, want := range map[string]int{
		"storage.chalkos.dev/v1alpha1 Volume":     7,
		"storage.chalkos.dev/v1alpha1 SystemDisk": 1,
		"kubernetes.chalkos.dev/v1alpha1 Node":    1,
		"kubernetes.chalkos.dev/v1alpha1 Cluster": 4,
	} {
		if count[kind] != want {
			t.Errorf("%s has %d rules, want %d", kind, count[kind], want)
		}
	}
}

func TestOf(t *testing.T) {
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/builtin")
	if err != nil {
		t.Fatal(err)
	}
	set, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	storage := set.Of("storage.chalkos.dev/v1alpha1")
	if len(storage.Programs) != 8 {
		t.Errorf("the storage kinds have %d rules, want 8", len(storage.Programs))
	}
	for _, p := range storage.Programs {
		if p.Kind.Kind != "Volume" && p.Kind.Kind != "SystemDisk" {
			t.Errorf("Of kept a rule of %s", p.Kind.Kind)
		}
	}
}

// storageConfig is a node's config with the SystemDisk and Volume specs given, as JSON. The
// storage rules name the node by node.name.
func storageConfig(t *testing.T, systemDisk string, volumes map[string]string) []config.Document {
	t.Helper()
	docs := []config.Document{
		builtinDoc(t, config.StorageAPI, "SystemDisk", "system", systemDisk),
	}
	for name, spec := range volumes {
		docs = append(docs, builtinDoc(t, config.StorageAPI, "Volume", name, spec))
	}
	return docs
}

func builtinDoc(t *testing.T, apiVersion, kind, name, spec string) config.Document {
	t.Helper()
	var s any
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		t.Fatal(err)
	}
	return config.FromObject(map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name}, "spec": s})
}

// storageFacts are the facts of a node whose disk data is pinned and plugged in, whose recorded
// section has the volumes given, and which was sent a fallback secret that matches.
type storageFacts map[string]any

func (f storageFacts) Fact(name string) (any, error) { return f[name], nil }

const (
	systemDisk = `{"disk": {"path": "/dev/vda"}, "encryption": {"mode": "tpm2", "fallback": "recovery-key"}, "var": {"size": "2G", "encryption": {"mode": null}}}`
	dataVolume = `{"disk": {"serial": "d"}, "size": "1G", "format": "xfs", "mountPoint": "/var/lib/data", "encryption": {"mode": null}}`
)

func recordedOf(volumes map[string]map[string]any) map[string]any {
	vs := map[string]any{"var": map[string]any{"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": "2G"}}
	for name, v := range volumes {
		vs[name] = v
	}
	return map[string]any{"volumes": vs, "fallback": "recovery-key", "encryption": "tpm2"}
}

// The storage kinds' rules take a reset for what chalkctl storage reset recreates, a reinstall
// for VAR, a removed volume and one that moves to another disk, and refuse a missing fallback
// secret; a larger volume, a moved mount point and a new volume pass.
func TestStorageRules(t *testing.T) {
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/builtin")
	if err != nil {
		t.Fatal(err)
	}
	set, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"disk": "data", "label": "data", "format": "xfs", "mountPoint": "/var/lib/data", "encryption": "tpm2", "size": "1G"}
	with := func(changes map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range data {
			out[k] = v
		}
		for k, v := range changes {
			out[k] = v
		}
		return out
	}
	edit := func(spec string, key, value string) string {
		var m map[string]any
		if err := json.Unmarshal([]byte(spec), &m); err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal([]byte(value), &v); err != nil {
			t.Fatal(err)
		}
		m[key] = v
		out, _ := json.Marshal(m)
		return string(out)
	}
	for _, tc := range []struct {
		name       string
		systemDisk string
		volumes    map[string]string
		recorded   map[string]any
		fallback   map[string]any
		unplugged  bool
		// presentError is why the node cannot tell whether the disk is plugged in.
		presentError string
		want         []string
		takes        Takes
	}{
		{name: "unchanged", volumes: map[string]string{"data": dataVolume}},
		{name: "a new volume", volumes: map[string]string{"data": dataVolume, "extra": `{"disk": null, "size": "1G", "format": "ext4", "mountPoint": null, "encryption": {"mode": null}}`}},
		{name: "larger", volumes: map[string]string{"data": edit(dataVolume, "size", `"2G"`)}},
		{name: "filling its disk", volumes: map[string]string{"data": edit(dataVolume, "size", "null")}},
		{name: "the same size written differently", volumes: map[string]string{"data": edit(dataVolume, "size", `"1024M"`)}},
		// A size of the same bytes is no change, so a missing disk does not matter.
		{name: "the same size written differently on an unplugged disk", volumes: map[string]string{"data": edit(dataVolume, "size", `"1024M"`)}, unplugged: true},
		{name: "a larger size on an unplugged disk", volumes: map[string]string{"data": edit(dataVolume, "size", `"2G"`)}, unplugged: true, takes: Refuse,
			want: []string{`Volume data: its disk data (serial "d") is not plugged in; plug it in`}},
		{name: "a larger size on a disk the node cannot tell is plugged in", volumes: map[string]string{"data": edit(dataVolume, "size", `"2G"`)}, unplugged: true, presentError: "two disks have its identity", takes: Refuse,
			want: []string{"Volume data: the node cannot find its disk data: two disks have its identity; try again once it can"}},
		// A recorded size bytes() cannot read, as an older image could write one, is recreated.
		{name: "a recorded size past the limit", volumes: map[string]string{"data": dataVolume}, recorded: recordedOf(map[string]map[string]any{"data": with(map[string]any{"size": "99999999P"})}), takes: Reset,
			want: []string{"Volume data: size 1G instead of 99999999P; reset it with chalkctl storage reset n1 data"}},
		{name: "mounted elsewhere", volumes: map[string]string{"data": edit(dataVolume, "mountPoint", `"/srv/data"`)}},
		{name: "smaller", volumes: map[string]string{"data": edit(dataVolume, "size", `"512M"`)}, takes: Reset,
			want: []string{"Volume data: smaller: 512M instead of 1G; reset it with chalkctl storage reset n1 data"}},
		{name: "another format", volumes: map[string]string{"data": edit(dataVolume, "format", `"ext4"`)}, takes: Reset,
			want: []string{"Volume data: format ext4 instead of xfs; reset it with chalkctl storage reset n1 data"}},
		{name: "raw instead of formatted", volumes: map[string]string{"data": edit(edit(dataVolume, "format", `"raw"`), "mountPoint", "null")}, takes: Reset,
			want: []string{"Volume data: format raw instead of xfs; reset it with chalkctl storage reset n1 data"}},
		// A raw volume's recorded format is empty, as one recorded when null meant raw.
		{name: "raw as recorded", volumes: map[string]string{"data": edit(edit(dataVolume, "format", `"raw"`), "mountPoint", "null")}, recorded: recordedOf(map[string]map[string]any{"data": with(map[string]any{"format": nil, "mountPoint": nil})})},
		{name: "raw as recorded empty", volumes: map[string]string{"data": edit(edit(dataVolume, "format", `"raw"`), "mountPoint", "null")}, recorded: recordedOf(map[string]map[string]any{"data": with(map[string]any{"format": "", "mountPoint": nil})})},
		{name: "formatted instead of raw", volumes: map[string]string{"data": dataVolume}, recorded: recordedOf(map[string]map[string]any{"data": with(map[string]any{"format": nil})}), takes: Reset,
			want: []string{"Volume data: format xfs instead of raw; reset it with chalkctl storage reset n1 data"}},
		{name: "another encryption", volumes: map[string]string{"data": edit(dataVolume, "encryption", `{"mode": "none"}`)}, takes: Reset,
			want: []string{"Volume data: encryption none instead of tpm2; reset it with chalkctl storage reset n1 data"}},
		{name: "a fixed size where it filled its disk", volumes: map[string]string{"data": dataVolume}, recorded: recordedOf(map[string]map[string]any{"data": with(map[string]any{"size": nil})}), takes: Reset,
			want: []string{"Volume data: size 1G instead of filling its disk; reset it with chalkctl storage reset n1 data"}},
		// A growth is no part of what a reset recreates the volume for.
		{name: "larger and of another encryption", volumes: map[string]string{"data": edit(edit(dataVolume, "size", `"2G"`), "encryption", `{"mode": "none"}`)}, takes: Reset,
			want: []string{"Volume data: encryption none instead of tpm2; reset it with chalkctl storage reset n1 data"}},
		{name: "on another disk", volumes: map[string]string{"data": edit(dataVolume, "disk", "null")}, takes: Reinstall,
			want: []string{"Volume data: disk system instead of data; only a reinstall of n1 carries it out"}},
		{name: "removed", takes: Reinstall, want: []string{"Volume data: the config removes it; only a reinstall of n1 removes a volume"}},
		{name: "VAR smaller", systemDisk: edit(systemDisk, "var", `{"size": "1G", "encryption": {"mode": null}}`), volumes: map[string]string{"data": dataVolume}, takes: Reinstall,
			want: []string{"VAR: smaller: 1G instead of 2G; only a reinstall of n1 carries it out"}},
		{name: "VAR larger", systemDisk: edit(systemDisk, "var", `{"size": "3G", "encryption": {"mode": null}}`), volumes: map[string]string{"data": dataVolume}},
		{name: "VAR recorded with another format", volumes: map[string]string{"data": dataVolume}, recorded: func() map[string]any {
			r := recordedOf(map[string]map[string]any{"data": data})
			r["volumes"].(map[string]any)["var"].(map[string]any)["format"] = "xfs"
			return r
		}(), takes: Reinstall, want: []string{"VAR: format ext4 instead of xfs; only a reinstall of n1 carries it out"}},
		// VAR is never raw, so a recorded VAR without a format has none.
		{name: "VAR recorded without a format", volumes: map[string]string{"data": dataVolume}, recorded: func() map[string]any {
			r := recordedOf(map[string]map[string]any{"data": data})
			r["volumes"].(map[string]any)["var"].(map[string]any)["format"] = nil
			return r
		}(), takes: Reinstall, want: []string{"VAR: format ext4 instead of none; only a reinstall of n1 carries it out"}},
		// The volume follows the node's encryption, and VAR too.
		{name: "the node's encryption", systemDisk: edit(systemDisk, "encryption", `{"mode": "none", "fallback": "recovery-key"}`), volumes: map[string]string{"data": dataVolume}, takes: Reinstall,
			want: []string{"VAR: encryption none instead of tpm2; only a reinstall of n1 carries it out", "Volume data: encryption none instead of tpm2; reset it with chalkctl storage reset n1 data"}},
		{name: "an encrypted volume without the fallback secret", volumes: map[string]string{"data": dataVolume, "extra": `{"disk": null, "size": "1G", "format": "ext4", "mountPoint": null, "encryption": {"mode": null}}`},
			fallback: map[string]any{"sent": false, "matches": nil},
			want:     []string{"Volume extra is encrypted and the node's fallback is recovery-key, but no fallback secret was sent"}, takes: Refuse},
		{name: "an encrypted volume with a wrong fallback secret", volumes: map[string]string{"data": dataVolume, "extra": `{"disk": null, "size": "1G", "format": "ext4", "mountPoint": null, "encryption": {"mode": null}}`},
			fallback: map[string]any{"sent": true, "matches": false},
			want:     []string{"the fallback secret does not match the node's existing fallback keyslot"}, takes: Refuse},
		{name: "an unencrypted volume without the fallback secret", volumes: map[string]string{"data": dataVolume, "extra": `{"disk": null, "size": "1G", "format": "ext4", "mountPoint": null, "encryption": {"mode": "none"}}`},
			fallback: map[string]any{"sent": false, "matches": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd := tc.systemDisk
			if sd == "" {
				sd = systemDisk
			}
			recorded := tc.recorded
			if recorded == nil {
				recorded = recordedOf(map[string]map[string]any{"data": data})
			}
			fallback := tc.fallback
			if fallback == nil {
				fallback = map[string]any{"sent": true, "matches": true}
			}
			facts := storageFacts{
				"recorded": recorded,
				"disks":    map[string]any{"data": map[string]any{"ref": `serial "d"`, "pinned": true, "identity": `serial "d"`, "changed": false, "found": "", "same": false, "error": "", "errorKind": "", "present": !tc.unplugged, "presentError": tc.presentError}},
				"fallback": fallback,
			}
			old := storageConfig(t, systemDisk, map[string]string{"data": dataVolume})
			failures, err := set.Evaluate(Input{Documents: storageConfig(t, sd, tc.volumes), Old: old, Facts: facts, Node: "n1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, tc.want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
			if len(failures) > 0 && failures[0].Takes != tc.takes {
				t.Errorf("takes = %s, want %s", failures[0].Takes, tc.takes)
			}
			// At an install there is nothing to compare with.
			facts["recorded"] = nil
			if failures, err := set.Evaluate(Input{Documents: storageConfig(t, sd, tc.volumes), Facts: facts, Node: "n1"}); err != nil || len(failures) != 0 {
				t.Errorf("without a recorded section: %v, %v", messages(failures), err)
			}
		})
	}
}

// The storage rules name the node they refuse a change for by node.name.
func TestStorageRulesNameTheNode(t *testing.T) {
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/builtin")
	if err != nil {
		t.Fatal(err)
	}
	set, err := Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"disk": "data", "label": "data", "format": "xfs", "mountPoint": "/var/lib/data", "encryption": "tpm2", "size": "1G"}
	facts := storageFacts{
		"recorded": recordedOf(map[string]map[string]any{"data": data}),
		"disks":    map[string]any{"data": map[string]any{"ref": `serial "d"`, "pinned": true, "identity": `serial "d"`, "changed": false, "found": "", "same": false, "error": "", "errorKind": "", "present": true, "presentError": ""}},
		"fallback": map[string]any{"sent": true, "matches": true},
	}
	old := storageConfig(t, systemDisk, map[string]string{"data": dataVolume})
	smaller := strings.Replace(dataVolume, `"size": "1G"`, `"size": "512M"`, 1)
	for _, tc := range []struct {
		name    string
		volumes map[string]string
		want    string
	}{
		{"reset", map[string]string{"data": smaller}, "Volume data: smaller: 512M instead of 1G; reset it with chalkctl storage reset w1 data"},
		{"reinstall", nil, "Volume data: the config removes it; only a reinstall of w1 removes a volume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: storageConfig(t, systemDisk, tc.volumes), Old: old, Facts: facts, Node: "w1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("failures = %q, want %q", got, tc.want)
			}
		})
	}
}
