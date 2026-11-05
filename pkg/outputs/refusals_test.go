package outputs

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/refusals"
)

// report is a shell command that prints a report of refusals of Demo documents, each "<name>
// <path> <message>".
func report(rs ...string) string {
	var items []string
	for _, r := range rs {
		f := strings.SplitN(r, " ", 3)
		items = append(items, `{"kind": "Demo", "name": "`+f[0]+`", "path": "`+f[1]+`", "message": "`+f[2]+`"}`)
	}
	return `echo '{"refusals": [` + strings.Join(items, ", ") + `]}'`
}

// A renderer refuses the config with a report on standard output, whether it fails or not; the
// failure holds the refusals, each once, and the delivery's refusal reports each once however
// many outputs reported it, as a rule's refusal reads, before the other failures.
func TestRenderRefusals(t *testing.T) {
	r := renderer(t)
	taken := refusals.Refusal{Kind: "Demo", Name: "one", Path: "spec.name", Message: "is taken"}
	too := refusals.Refusal{Kind: "Demo", Name: "two", Path: "spec.name", Message: "is taken too"}
	defs := map[string]Definition{
		"failed":  def(t, r.Root, script(t, report("one spec.name is taken", "two spec.name is taken too", "one spec.name is taken")+`; echo detail >&2; exit 1`), ""),
		"passed":  def(t, r.Root, script(t, report("one spec.name is taken")), ""),
		"broken":  def(t, r.Root, script(t, `exit 2`), ""),
		"none":    def(t, r.Root, script(t, report()), ""),
		"checked": def(t, r.Root, script(t, `true`), script(t, report("one spec.name is the check")+`; exit 1`)),
	}
	for name, d := range defs {
		d.Dir = filepath.Join(r.Root, name)
		defs[name] = d
	}
	staged, failures := r.Render(context.Background(), defs, input(t, demos))
	byName := map[string]Failure{}
	for _, f := range failures {
		byName[f.Output] = f
	}
	if f := byName["failed"]; !slices.Equal(f.Refusals, []refusals.Refusal{taken, too}) ||
		f.Problem != "its renderer refused the config: Demo/one: spec.name: is taken; Demo/two: spec.name: is taken too" {
		t.Errorf("failed = %#v", f)
	}
	if f := byName["passed"]; !slices.Equal(f.Refusals, []refusals.Refusal{taken}) {
		t.Errorf("a renderer that refuses and exits 0 = %#v", f)
	}
	// A check's standard output is discarded; its failure says the check failed.
	if f := byName["checked"]; f.Refusals != nil || f.Problem != "its check refused the rendered files (exit 1); see the node's journal" {
		t.Errorf("checked = %#v", f)
	}
	// An empty report refuses nothing.
	if _, ok := staged.States()["none"]; !ok {
		t.Errorf("an empty report failed the output: %v", byName["none"])
	}
	want := "the config is refused: Demo/one: spec.name: is taken; Demo/two: spec.name: is taken too; " +
		"output broken: its renderer failed (exit 2); see the node's journal; " +
		"output checked: its check refused the rendered files (exit 1); see the node's journal"
	if got := Refusal(failures); got != want {
		t.Errorf("Refusal = %q\nwant      %q", got, want)
	}
	staged.Discard()
	if entries, _ := os.ReadDir(filepath.Join(r.Root, ".staging")); len(entries) != 0 {
		t.Errorf("staging holds %d entries after the renders", len(entries))
	}
}

// A renderer that prints anything but a report of refusals, or more than refusals.MaxReport
// bytes, fails its output.
func TestRenderPrintsNoReport(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"a line":      {`echo rendered`, "its renderer's standard output: it is no report of refusals"},
		"a bad field": {report("One spec.name taken"), `its renderer's standard output: refusal 1: name "One" is no document name`},
		"too much":    {`head -c 65537 /dev/zero`, "its renderer: it printed more than 65536 bytes"},
		// A failed renderer that prints no report fails for its exit status, as it would print
		// nothing.
		"a line and a failure": {`echo rendered; exit 3`, "its renderer failed (exit 3); see the node's journal"},
	} {
		t.Run(name, func(t *testing.T) {
			r := renderer(t)
			d := def(t, r.Root, script(t, c.body), "")
			_, failures := r.Render(context.Background(), map[string]Definition{"demo": d}, input(t, demos))
			if len(failures) != 1 || failures[0].Refusals != nil || failures[0].Problem != c.want {
				t.Errorf("failures = %#v", failures)
			}
		})
	}
}

// A refusal cannot carry a secret's value: a renderer that refuses with all it read reports the
// Secret's reference, never its value, which it never reads.
func TestRefusalsCarryNoSecret(t *testing.T) {
	r := renderer(t)
	d := def(t, r.Root, script(t, `printf '{"refusals": [{"kind": "Demo", "name": "two", "path": "spec.token", "message": "%s"}]}' "$(sed 's/["\\]/\\&/g')"`), "")
	_, failures := r.Render(context.Background(), map[string]Definition{"demo": d}, input(t, demos))
	if len(failures) != 1 || len(failures[0].Refusals) != 1 {
		t.Fatalf("failures = %#v", failures)
	}
	if msg := Refusal(failures); !strings.Contains(msg, `"secretRef":{"key":"token","name":"demo"}`) || strings.Contains(msg, "s3cret") {
		t.Errorf("the refusal = %s", msg)
	}
}
