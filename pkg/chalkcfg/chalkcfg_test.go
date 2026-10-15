package chalkcfg

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// run runs chalkcfg with args on s and returns its stdout, stderr and exit status.
func run(t *testing.T, s store, args ...string) (string, string, int) {
	t.Helper()
	root := newCommand(s)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	status := Execute(context.Background(), root)
	return out.String(), errOut.String(), status
}

func TestGet(t *testing.T) {
	s := testNode(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"get"}, "KIND  GROUP             DOCUMENTS\nPeer  bgp.example.org   2\nRack  rack.example.org  1\nSite  site.example.org  0\n"},
		{[]string{"get", "Peer"}, "backup\nuplink\n"},
		{[]string{"get", "Rack", "rack"}, "rack\n"},
		{[]string{"get", "Rack", "rack", "-o", "jsonpath={.spec.location}"}, "rack-a/u12\n"},
		{[]string{"get", "Rack", "rack", "-o", "jsonpath=.spec.location"}, "rack-a/u12\n"},
		{[]string{"get", "Peer", "-o", "jsonpath={.spec.asn}"}, "4200000002\n4200000001\n"},
		{[]string{"get", "Peer", "uplink", "-o", "jsonpath={.spec.tags[*]}"}, "a b\n"},
		{[]string{"get", "Peer", "uplink", "-o", "jsonpath={.spec.labels['zone-a']}"}, "z1\n"},
		{[]string{"get", "Peer", "uplink", "-o", "jsonpath={.spec.tags}"}, `["a","b"]` + "\n"},
		{[]string{"get", "Rack", "rack", "-o", "go-template={{.spec.location}}"}, "rack-a/u12\n"},
		{[]string{"get", "Peer", "-o", "go-template={{range .spec.tags}}{{.}},{{end}}"}, "\na,b,\n"},
		{[]string{"get", "Peer", "uplink", "-o", "yaml"}, "apiVersion: bgp.example.org/v1alpha1\nkind: Peer\nmetadata:\n  name: uplink\nspec:\n  asn: 4200000001\n  labels:\n    zone-a: z1\n  port: 179\n  tags:\n    - a\n    - b\n"},
		{[]string{"get", "Site", "-o", "yaml"}, "items: []\nkind: List\n"},
		{[]string{"get", "Site", "-o", "jsonpath={.spec}"}, ""},
		// A range ends with each document, so the next document starts its own.
		{[]string{"get", "Peer", "-o", "jsonpath={range .spec.tags[*]}{@} {end}"}, "\na b \n"},
		// Numbers compare as numbers in filters and templates.
		{[]string{"get", "Peer", "backup", "-o", "jsonpath={.spec.neighbors[?(@.asn==65001)].ip}"}, "10.0.0.1\n"},
		{[]string{"get", "Peer", "backup", "-o", "jsonpath={.spec.neighbors[?(@.asn>65001)].ip}"}, "10.0.0.2\n"},
		{[]string{"get", "Peer", "-o", "go-template={{if eq .spec.port 179}}{{.metadata.name}}{{end}}"}, "\nuplink\n"},
		{[]string{"get", "Peer", "-o", "go-template={{if gt .spec.asn 4200000001}}{{.metadata.name}}{{end}}"}, "backup\n\n"},
		{[]string{"get", "Peer", "uplink", "-o", "go-template={{index .spec.labels \"zone-a\"}} {{index .spec.tags 1}}"}, "z1 b\n"},
	} {
		out, errOut, status := run(t, s, c.args...)
		if status != 0 || out != c.want {
			t.Errorf("chalkcfg %s = %d, %q, stderr %q; want 0, %q", strings.Join(c.args, " "), status, out, errOut, c.want)
		}
	}
}

func TestGetJSON(t *testing.T) {
	s := testNode(t)
	out, _, status := run(t, s, "get", "Rack", "rack", "-o", "json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); status != 0 || err != nil || doc["spec"].(map[string]any)["location"] != "rack-a/u12" {
		t.Errorf("Rack rack -o json = %d, %q", status, out)
	}

	out, _, status = run(t, s, "get", "Peer", "-o", "json")
	var list struct {
		Kind  string           `json:"kind"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); status != 0 || err != nil || list.Kind != "List" || len(list.Items) != 2 ||
		list.Items[0]["metadata"].(map[string]any)["name"] != "backup" {
		t.Errorf("Peer -o json = %d, %q", status, out)
	}
	if !strings.Contains(out, `"asn": 4200000001`) {
		t.Errorf("Peer -o json rounds the AS number:\n%s", out)
	}

	out, _, status = run(t, s, "get", "Site", "-o", "json")
	if status != 0 || out != "{\n  \"items\": [],\n  \"kind\": \"List\"\n}\n" {
		t.Errorf("Site -o json = %d, %q; want an empty list", status, out)
	}
}

// A declared kind without documents is an empty result, and says so on stderr alone.
func TestGetEmptyKind(t *testing.T) {
	out, errOut, status := run(t, testNode(t), "get", "Site")
	if status != 0 || out != "" || !strings.Contains(errOut, "the node has no Site documents") {
		t.Errorf("get Site = %d, %q, %q", status, out, errOut)
	}
}

func TestGetFails(t *testing.T) {
	s := testNode(t)
	for _, c := range []struct {
		args   []string
		status int
		want   string
	}{
		{[]string{"get", "Nope"}, 1, "chalkcfg: the image declares no kind Nope; it declares Peer, Rack, Site\n"},
		{[]string{"get", "Rack", "other"}, 1, `chalkcfg: the node has no Rack named "other"` + "\n"},
		{[]string{"get", "Secret"}, 1, "chalkcfg: Secrets are never printed: a service gets"},
		{[]string{"get", "Rack", "rack", "-o", "jsonpath={.spec.nope}"}, 1, "chalkcfg: Rack rack: nope is not found\n"},
		{[]string{"get", "Rack", "rack", "-o", "jsonpath={.spec["}, 1, "chalkcfg: the jsonpath expression {.spec[: unterminated array\n"},
		{[]string{"get", "Rack", "rack", "-o", "go-template={{.spec.nope}}"}, 1, `map has no entry for key "nope"`},
		{[]string{"get", "Rack", "rack", "-o", "go-template={{index .spec \"nope\"}}"}, 1, `map has no entry for key "nope"`},
		{[]string{"get", "Peer", "uplink", "-o", "go-template={{index .spec.tags 2}}"}, 1, "index out of range: 2"},
		{[]string{"get", "Rack", "rack", "-o", "go-template={{"}, 1, "chalkcfg: the template:"},
		{[]string{"get", "Rack", "rack", "-o", "xml"}, 2, `unknown output format "xml"`},
		{[]string{"get", "Rack", "rack", "extra"}, 2, "get takes at most a kind and a name"},
		{[]string{"get", "-o", "json"}, 2, "--output needs a kind"},
		{[]string{"get", "--outptu", "json"}, 2, "unknown flag: --outptu"},
		{[]string{"list"}, 2, `unknown command "list"`},
	} {
		out, errOut, status := run(t, s, c.args...)
		if status != c.status || out != "" || !strings.Contains(errOut, c.want) {
			t.Errorf("chalkcfg %s = %d, %q, stderr %q; want %d and %q", strings.Join(c.args, " "), status, out, errOut, c.status, c.want)
		}
	}
}

// Every visible command has the descriptions and examples its reference page shows.
func TestCommandsDescribed(t *testing.T) {
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if !cmd.Hidden && (cmd.Short == "" || cmd.Long == "" || cmd.Example == "") {
			t.Errorf("%s lacks a short or long description or examples", cmd.CommandPath())
		}
		for _, c := range cmd.Commands() {
			walk(c)
		}
	}
	walk(NewCommand())
}

// Nodes have no logins to complete commands in.
func TestNoCompletion(t *testing.T) {
	root := NewCommand()
	root.InitDefaultCompletionCmd()
	if cmd, _, err := root.Find([]string{"completion"}); err == nil && cmd != root {
		t.Error("chalkcfg has a completion command")
	}
}

// YAML numbers keep an integer's digits, and a float written with an exponent gets a point, which
// YAML 1.1 needs to read it as a float.
func TestYAMLNumbers(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader(`{"a":1e3,"b":2.5,"c":1E-7,"d":12345678901234567890,"e":-0.5e10}`))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := (output{format: "yaml"}).write(&buf, []document{{kind: "X", name: "x", object: obj}}, true); err != nil {
		t.Fatal(err)
	}
	if want := "a: 1000.0\nb: 2.5\nc: 1.0e-07\nd: 12345678901234567890\ne: -5.0e+09\n"; buf.String() != want {
		t.Errorf("YAML = %q, want %q", buf.String(), want)
	}
}
