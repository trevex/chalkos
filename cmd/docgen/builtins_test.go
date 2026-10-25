package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteBuiltins(t *testing.T) {
	page := filepath.Join(t.TempDir(), "builtins.md")
	if err := writeBuiltins(page, "testdata/builtins/default", "testdata/builtins/plain"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.HasPrefix(got, "---\ntitle: \"Built-ins\"\n") {
		t.Errorf("front matter: %q", got[:min(len(got), 80)])
	}
	// Each section has the built-ins of every image, those of the image without Kubernetes, and
	// then those only the default image has.
	order := []string{
		"\n# Built-ins\n\nThis page is for ",
		"\n## Change rules { #change-rules }\n\n",
		"\n### Every image { #rules-every-image }\n\n",
		"\n#### Volume { #rules-volume }\n\n",
		"\n### Images with Kubernetes { #rules-kubernetes }\n\n",
		"\n#### Node { #rules-node }\n\n",
		"\n## Facts { #facts }\n\n",
		"\n### Every image { #facts-every-image }\n\n",
		"| `disks`{ #fact-disks } |",
		"\n### Images with Kubernetes { #facts-kubernetes }\n\n",
		"| `share`{ #fact-share } |",
		"\n## Outputs { #outputs }\n\n",
		"\n### Every image { #outputs-every-image }\n\n",
		"| `chrony`{ #output-chrony } |",
		"\n### Images with Kubernetes { #outputs-kubernetes }\n\n",
		"| `kubelet`{ #output-kubelet } |",
		"\n## Effects { #effects }\n\n",
		"\n### Every image { #effects-every-image }\n\n",
		"| `chrony`{ #effect-chrony } |",
		"\n### Images with Kubernetes { #effects-kubernetes }\n\n",
		"| `kubernetes`{ #effect-kubernetes } |",
		"\n## Diagnostics { #diagnostics }\n\n",
		"\n### Every image { #diagnostics-every-image }\n\n",
		"| `ping`{ #diagnostic-ping } |",
		"\n### Images with Kubernetes { #diagnostics-kubernetes }\n\n",
		"| `crictl-pods`{ #diagnostic-crictl-pods } |",
		"\nSources: ",
	}
	last := -1
	for _, h := range order {
		i := strings.Index(got, h)
		if i <= last {
			t.Errorf("%q at %d, after %d", h, i, last)
		}
		last = i
	}
	for _, want := range []string{
		// A kind rule comes first, then the field rules by path, also those of a $ref, each with
		// what it guards, or — without a description. A long expression follows the table; a pipe
		// outside a code span is escaped, and one inside is not, and so is a placeholder.
		"| Rule | Field | Guards | Takes | Expression | Message |\n| --- | --- | --- | --- | --- | --- |\n" +
			"| 1 | — | A volume keeps its recorded size, and its disk is plugged in. | `reset` | after the table | after the table |\n" +
			"| 2 | `spec.size` | A volume keeps its size, &lt;size>. | `refuse` | `self == oldSelf || facts.recorded == null` | the size \\| stays |\n" +
			"| 3 | `spec.taints[].key` | — | `refuse` | `self != ''` | a key |\n",
		"\nExpression of rule 1:\n\n```text\nfacts.recorded == null || documents.all(n, documents[n].spec.size == facts.recorded.volumes[n].size && facts.disks[n].present)\n```\n",
		"\nMessage of rule 1:\n\n```text\ndocuments.filter(n, true)\n  .join(\"; \")\n```\n",
		"| 1 | — | The node keeps the type of its share. | `refuse` | `facts.share == null || documents.all(n, documents[n].spec.type == facts.share.kind)` | `\"the type is \" + facts.share.kind` |\n" +
			"| 2 | `spec.labels` | — | `refuse` | `facts.share == null || self == oldSelf` | the labels are fixed |\n",
		// Store paths show as the names of their programs and files.
		"| Fact | Reads | Read by |\n| --- | --- | --- |\n" +
			"| `disks`{ #fact-disks } | What `chalkos-storage-facts disks` prints. | The rules of `Volume`. |\n" +
			"| `fallback`{ #fact-fallback } | What `chalkos-storage-facts fallback` prints, given the fallback secret. | — |\n" +
			"| `recorded`{ #fact-recorded } | File `/state/storage/storage.json`. | The rules of `Volume`. |\n",
		"| `cluster`{ #fact-cluster } | File `/run/chalkos/kubernetes/cluster.json`. | The effects `chalkd-cluster` and `kubernetes`. |\n" +
			"| `share`{ #fact-share } | The key `kind` of the file `/state/kubernetes/share.json`, and the SHA-256 of each other key. | The rules of `Node`, and the effects `chalkd-share` and `kubernetes`. |\n",
		"| Output | Inputs | Directory | Renderer | Check |\n| --- | --- | --- | --- | --- |\n" +
			"| `chrony`{ #output-chrony } | `TimeSync` | `/run/chalkos/outputs/chrony` | Go template `servers.sources.tmpl` | yes |\n" +
			"| `networkd`{ #output-networkd } | `Link`, `NetDev`, `Network` | `/run/systemd/network` | Command `chalkos-render networkd chalkos-networkd-schemas` | no |\n",
		"| `kubelet`{ #output-kubelet } | `Node` | `/run/chalkos/outputs/kubelet` | The documents of `Node` and `Cluster` | no |\n" +
			"| `labels`{ #output-labels } | `Node` | `/run/chalkos/outputs/labels` | jq program `labels.conf.jq` | no |\n",
		"| Effect | Fires when | Does |\n| --- | --- | --- |\n" +
			"| `chrony`{ #effect-chrony } | The output `chrony` changed. | Runs `chalkos-chrony-reload-sources`. |\n" +
			"| `networkd`{ #effect-networkd } | The output `networkd` changed. | Reloads `systemd-networkd.service`. |\n",
		"| `chalkd-cluster`{ #effect-chalkd-cluster } | `facts.cluster != oldFacts.cluster` holds. | Restarts chalkd's Kubernetes loops. |\n" +
			"| `chalkd-share`{ #effect-chalkd-share } | `facts.share != oldFacts.share` holds. | Reloads chalkd's control-plane loop. |\n" +
			"| `kubernetes`{ #effect-kubernetes } | The documents of `Host`, or a value of a Secret they reference, changed, or the `when` after the table holds, which reads the documents of `Node` and the facts `cluster` and `share`. | Restarts `chalkos-kubernetes.service` and `kubelet.service`, and starts them when they are stopped. |\n",
		"\n`when` of `kubernetes`:\n\n```text\ncel.bind(d, config[\"kubernetes.chalkos.dev/v1alpha1\"].Node, d)",
		// A diagnostic's command shows without store paths, and a script of several lines follows
		// the table; its parameters list the required ones first, each with its range, values or
		// pattern and its default.
		"| Diagnostic | Does | Parameters | Role | Capabilities |\n| --- | --- | --- | --- | --- |\n" +
			"| `ping`{ #diagnostic-ping } | Sends ICMP echo requests to an address. Runs `ping -n -w 10 -c {count} {address}`. | `address`, address; `count`, int from 1 to 10, default `4` | operator | `CAP_NET_RAW` |\n" +
			"| `route-get`{ #diagnostic-route-get } | The route the kernel takes to an address. Runs `ip -json route get {address}`. | `address`, address | reader | — |\n" +
			"| `routes`{ #diagnostic-routes } | The routes of one family in one table. Runs `ip -json -family {family} route show table {table}`. | `family`, enum of `inet` or `inet6`, default `inet`; `table`, pattern `^(main|local|default|all|[0-9]+)$`, default `main` | reader | — |\n" +
			"| `tcp-connect`{ #diagnostic-tcp-connect } | Opens a TCP connection to an address and port. Runs `bash -c <script> tcp-connect {address} {port}`, with the script after the table. | `address`, address; `port`, int from 1 to 65535 | operator | — |\n" +
			"\nScript of `tcp-connect`:\n\n```text\nif : <>\"/dev/tcp/$1/$2\"; then\n  echo \"connected to $1 port $2\"\nfi\n```\n",
		"| `crictl-pods`{ #diagnostic-crictl-pods } | The pods the kubelet runs, as crictl pods prints them. Runs `crictl pods`. | — | reader | — |\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks:\n%s", want)
		}
	}
	if strings.Contains(got, "/nix/store") {
		t.Errorf("the page shows a store path:\n%s", got)
	}
	if strings.Contains(got, "#rules-host") {
		t.Errorf("the page has a section for a kind without rules:\n%s", got)
	}
	if every := got[:strings.Index(got, "### Images with Kubernetes { #outputs-kubernetes }")]; strings.Contains(every, "#output-kubelet") {
		t.Errorf("an output of the default image alone is listed for every image")
	}
}

func TestWriteBuiltinsRefusesWhatOnlyThePlainImageHas(t *testing.T) {
	plain := t.TempDir()
	if err := os.CopyFS(plain, os.DirFS("testdata/builtins/plain")); err != nil {
		t.Fatal(err)
	}
	effects := `{"ntp":{"chalkd":"","kinds":["TimeSync"],"outputs":[],"reload":[],"restart":["ntp.service"],"run":"","start":false,"when":""}}`
	if err := os.WriteFile(filepath.Join(plain, "effects.json"), []byte(effects), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeBuiltins(filepath.Join(t.TempDir(), "builtins.md"), "testdata/builtins/default", plain)
	if err == nil || !strings.Contains(err.Error(), "effect ntp") {
		t.Errorf("error %v, want one naming the effect ntp", err)
	}
}

func TestWriteBuiltinsRefusesARuleOnlyThePlainImageHas(t *testing.T) {
	plain := t.TempDir()
	if err := os.CopyFS(plain, os.DirFS("testdata/builtins/plain")); err != nil {
		t.Fatal(err)
	}
	volume := filepath.Join(plain, "schemas", "storage.chalkos.dev_v1alpha1_Volume.json")
	data, err := os.ReadFile(volume)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), `"self != ''"`, `"self != 'x'"`, 1)
	if changed == string(data) {
		t.Fatal("the plain image's Volume has no rule self != ''")
	}
	if err := os.WriteFile(volume, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	err = writeBuiltins(filepath.Join(t.TempDir(), "builtins.md"), "testdata/builtins/default", plain)
	if err == nil || !strings.Contains(err.Error(), "a rule of Volume that the default image lacks: self != 'x'") {
		t.Errorf("error %v, want one naming the rule of Volume", err)
	}
}
