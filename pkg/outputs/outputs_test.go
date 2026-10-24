package outputs

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// script writes an executable shell script and returns its path; it reads its input on stdin and
// the directory to write as $OUT.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -e\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// def is an output of the Demo kind that the test's user owns, with the renderer and check given.
func def(t *testing.T, root, render, check string) Definition {
	t.Helper()
	return Definition{
		Inputs: []string{"Demo"},
		Render: render,
		Check:  check,
		Dir:    filepath.Join(root, "demo"),
		User:   strconv.Itoa(os.Getuid()),
		Group:  strconv.Itoa(os.Getgid()),
		Mode:   "0640",
	}
}

func input(t *testing.T, text string) Input {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/image/schemas")
	if err != nil {
		t.Fatal(err)
	}
	return Input{Node: "cp1", Documents: docs, Schemas: s}
}

const demos = `apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: two}
spec: {name: second, token: {secretRef: {name: demo, key: token}}}
---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: one}
spec: {name: first}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: demo}
spec: {data: {token: s3cret}}
`

func renderer(t *testing.T) Renderer {
	return Renderer{Root: t.TempDir(), Run: Direct{}}
}

// A renderer reads the node's name and the documents of its kinds, sorted by name, without any
// Secret; its files get the output's mode, and swapping them in links the output's dir to them.
func TestRenderAndSwap(t *testing.T) {
	r := renderer(t)
	d := def(t, r.Root, script(t, `cat > "$OUT/input.json"; mkdir "$OUT/sub"; echo hi > "$OUT/sub/file"`), "")
	in := input(t, demos)
	s, failures := r.Render(context.Background(), map[string]Definition{"demo": d}, in)
	if len(failures) > 0 {
		t.Fatal(failures)
	}
	if _, err := os.Stat(d.Dir); !os.IsNotExist(err) {
		t.Fatalf("the dir exists before the swap: %v", err)
	}
	if err := s.Swap(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Node      map[string]string           `json:"node"`
		Documents map[string][]map[string]any `json:"documents"`
	}
	data, err := os.ReadFile(filepath.Join(d.Dir, "input.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Node["name"] != "cp1" || len(got.Documents) != 1 || len(got.Documents["Demo"]) != 2 {
		t.Fatalf("input = %s", data)
	}
	if name := got.Documents["Demo"][0]["metadata"].(map[string]any)["name"]; name != "one" {
		t.Errorf("the first document is %v, want one", name)
	}
	if strings.Contains(string(data), "s3cret") {
		t.Errorf("the renderer read a secret value: %s", data)
	}
	info, err := os.Stat(filepath.Join(d.Dir, "sub", "file"))
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Errorf("the file: %v, %v", info, err)
	}
	if link, err := os.Readlink(d.Dir); err != nil || filepath.Dir(link) != filepath.Join(r.Root, ".rendered") {
		t.Errorf("the dir links to %q: %v", link, err)
	}
	st, err := ReadStatus(r.Root)
	if err != nil || st["demo"].Hash != s.States()["demo"].Files || st["demo"].Error != "" {
		t.Errorf("status = %v, %v", st, err)
	}
}

// A second swap replaces the link in one rename and removes the files the first one linked to.
func TestSwapReplacesTheOldFiles(t *testing.T) {
	r := renderer(t)
	render := func(text string) string {
		d := def(t, r.Root, script(t, `echo `+text+` > "$OUT/file"`), "")
		s, failures := r.Render(context.Background(), map[string]Definition{"demo": d}, input(t, demos))
		if len(failures) > 0 {
			t.Fatal(failures)
		}
		if err := s.Swap(); err != nil {
			t.Fatal(err)
		}
		link, _ := os.Readlink(d.Dir)
		return link
	}
	first := render("one")
	second := render("two")
	if first == second {
		t.Fatal("the second swap links to the first's files")
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Errorf("the first files stay: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(r.Root, "demo", "file"))
	if string(data) != "two\n" {
		t.Errorf("the dir holds %q", data)
	}
}

// A dir that a directory holds, as one a tmpfiles rule made, becomes a link too.
func TestSwapReplacesADirectory(t *testing.T) {
	r := renderer(t)
	d := def(t, r.Root, script(t, `echo new > "$OUT/file"`), "")
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, failures := r.Render(context.Background(), map[string]Definition{"demo": d}, input(t, demos))
	if len(failures) > 0 {
		t.Fatal(failures)
	}
	if err := s.Swap(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(d.Dir, "file")); string(data) != "new\n" {
		t.Errorf("the dir holds %q", data)
	}
}

// A renderer or a check that fails is a failure that names its exit status, while what it wrote
// to standard error goes to the log alone; a failed output is not staged, and the others are.
func TestRenderFailures(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	r := renderer(t)
	ok := script(t, `echo fine > "$OUT/file"`)
	defs := map[string]Definition{
		"broken":  def(t, r.Root, script(t, `echo "leaked s3cret" >&2; exit 3`), ""),
		"refused": def(t, r.Root, ok, script(t, `echo "bad file" >&2; exit 1`)),
		"link":    def(t, r.Root, script(t, `ln -s /etc/passwd "$OUT/passwd"`), ""),
		"big":     def(t, r.Root, script(t, `head -c 16777217 /dev/zero > "$OUT/big"`), ""),
		"fine":    def(t, r.Root, ok, script(t, `grep -q fine "$OUT/file"`)),
	}
	for name, d := range defs {
		d.Dir = filepath.Join(r.Root, name)
		defs[name] = d
	}
	s, failures := r.Render(context.Background(), defs, input(t, demos))
	want := []string{
		"output big: its renderer wrote more than 16777216 bytes",
		"output broken: its renderer failed (exit 3); see the node's journal",
		"output link: its renderer wrote passwd, which is no regular file",
		"output refused: its check refused the rendered files (exit 1); see the node's journal",
	}
	if len(failures) != len(want) {
		t.Fatalf("failures = %v", failures)
	}
	for i, f := range failures {
		if f.Error() != want[i] {
			t.Errorf("failure %d = %q, want %q", i, f.Error(), want[i])
		}
	}
	if !strings.Contains(logged.String(), "leaked s3cret") || !strings.Contains(logged.String(), "bad file") {
		t.Errorf("the log lacks the programs' stderr: %s", logged.String())
	}
	if states := s.States(); len(states) != 1 || states["fine"].Files == "" {
		t.Errorf("staged = %v", states)
	}
	entries, _ := os.ReadDir(filepath.Join(r.Root, ".staging"))
	if len(entries) != 1 {
		t.Errorf("staging holds %d directories, want the passing output's alone", len(entries))
	}
	s.Discard()
	if entries, _ := os.ReadDir(filepath.Join(r.Root, ".staging")); len(entries) != 0 {
		t.Errorf("staging holds %d directories after Discard", len(entries))
	}
	if err := RecordFailures(r.Root, failures); err != nil {
		t.Fatal(err)
	}
	if st, _ := ReadStatus(r.Root); st["broken"].Error != "its renderer failed (exit 3); see the node's journal" {
		t.Errorf("status = %v", st)
	}
}

// An output's state changes with its files and with the values of the Secrets its documents
// reference, and not with other Secrets.
func TestStates(t *testing.T) {
	r := renderer(t)
	d := def(t, r.Root, script(t, `echo same > "$OUT/file"`), "")
	state := func(text string) State {
		s, failures := r.Render(context.Background(), map[string]Definition{"demo": d}, input(t, text))
		if len(failures) > 0 {
			t.Fatal(failures)
		}
		return s.States()["demo"]
	}
	base := state(demos)
	if base.Secrets == "" {
		t.Fatal("no Secret hash for a document that references one")
	}
	rotated := state(strings.Replace(demos, "s3cret", "other", 1))
	if rotated.Files != base.Files || rotated.Secrets == base.Secrets {
		t.Errorf("a rotated password: %v, was %v", rotated, base)
	}
	other := state(demos + "---\napiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: unused}\nspec: {data: {k: v}}\n")
	if other != base {
		t.Errorf("an unreferenced Secret changed the state: %v, was %v", other, base)
	}
	none := state(`apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: one}
spec: {name: first}
`)
	if none.Secrets != "" {
		t.Errorf("Secret hash %q without a reference", none.Secrets)
	}
}

// Live hashes the files an output's dir serves as Render hashed them, and nothing when it has
// none yet.
func TestLive(t *testing.T) {
	r := renderer(t)
	d := def(t, r.Root, script(t, `echo hi > "$OUT/file"`), "")
	in := input(t, demos)
	if st, err := Live(d, in); err != nil || st.Files != "" || st.Secrets == "" {
		t.Fatalf("Live before a render = %v, %v", st, err)
	}
	s, _ := r.Render(context.Background(), map[string]Definition{"demo": d}, in)
	if err := s.Swap(); err != nil {
		t.Fatal(err)
	}
	if st, err := Live(d, in); err != nil || st != s.States()["demo"] {
		t.Errorf("Live = %v, %v, want %v", st, err, s.States()["demo"])
	}
}

// Read refuses programs that are no absolute paths, a dir outside /run, in chalkos's own
// directories or in another output's, and a mode that is no octal number.
func TestRead(t *testing.T) {
	for name, tc := range map[string]struct{ json, want string }{
		"fine":     {`{"a": {"inputs": [], "render": "/nix/store/x/bin/r a", "dir": "/run/a", "user": "root", "group": "root", "mode": "0644"}}`, ""},
		"renderer": {`{"a": {"render": "r", "dir": "/run/a", "mode": "0644"}}`, `output a: the renderer "r" names no program by its absolute path`},
		"check":    {`{"a": {"render": "/nix/store/x", "check": "true", "dir": "/run/a", "mode": "0644"}}`, `output a: the check "true" names no program`},
		"dir":      {`{"a": {"render": "/nix/store/x", "dir": "run/a", "mode": "0644"}}`, `the dir "run/a" must be a clean path below /run`},
		"mode":     {`{"a": {"render": "/nix/store/x", "dir": "/run/a", "mode": "rw"}}`, `the mode "rw" is no octal file mode`},
		"networkd": {`{"a": {"render": "/nix/store/x", "dir": "/run/systemd/network", "mode": "0644"}}`, ""},
		"own":      {`{"a": {"render": "/nix/store/x", "dir": "/run/chalkos/outputs/a/sub", "mode": "0644"}}`, ""},
		"etc":      {`{"a": {"render": "/nix/store/x", "dir": "/etc/a", "mode": "0644"}}`, `the dir "/etc/a" must be a clean path below /run`},
		"run":      {`{"a": {"render": "/nix/store/x", "dir": "/run", "mode": "0644"}}`, `the dir "/run" must be a clean path below /run`},
		"unclean":  {`{"a": {"render": "/nix/store/x", "dir": "/run/a/", "mode": "0644"}}`, `the dir "/run/a/" must be a clean path below /run`},
		"dot":      {`{"a": {"render": "/nix/store/x", "dir": "/run/a/.b", "mode": "0644"}}`, `the dir "/run/a/.b" must be a clean path below /run`},
		"chalkos":  {`{"a": {"render": "/nix/store/x", "dir": "/run/chalkos/config", "mode": "0644"}}`, `the dir "/run/chalkos/config" is in /run/chalkos, where an output may take only /run/chalkos/outputs/a`},
		"other":    {`{"a": {"render": "/nix/store/x", "dir": "/run/chalkos/outputs/b", "mode": "0644"}}`, `where an output may take only /run/chalkos/outputs/a`},
		"same":     {`{"a": {"render": "/nix/store/x", "dir": "/run/x", "mode": "0644"}, "b": {"render": "/nix/store/x", "dir": "/run/x", "mode": "0644"}}`, `output b: the dir "/run/x" is the dir of the output a`},
		"nested":   {`{"a": {"render": "/nix/store/x", "dir": "/run/x/y", "mode": "0644"}, "b": {"render": "/nix/store/x", "dir": "/run/x", "mode": "0644"}}`, `output a: the dir "/run/x/y" is inside the dir of the output b`},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "outputs.json")
			os.WriteFile(path, []byte(tc.json), 0o644)
			_, err := Read(path, DefaultRoot)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("Read = %v, want %q", err, tc.want)
			}
		})
	}
	if defs, err := Read(filepath.Join(t.TempDir(), "missing.json"), DefaultRoot); err != nil || len(defs) != 0 {
		t.Errorf("a missing file: %v, %v", defs, err)
	}
}

// A check reads the files as the renderer wrote them, before they get the output's owner and
// mode, which may leave the sandbox's user unable to read them.
func TestCheckReadsTheRenderersFiles(t *testing.T) {
	r := renderer(t)
	d := def(t, r.Root, script(t, `echo hi > "$OUT/file"`), script(t, `grep -q hi "$OUT/file"`))
	d.Mode = "0200"
	s, failures := r.Render(context.Background(), map[string]Definition{"demo": d}, input(t, demos))
	if len(failures) > 0 {
		t.Fatal(failures)
	}
	if err := s.Swap(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(d.Dir, "file")); err != nil || info.Mode().Perm() != 0o200 {
		t.Errorf("the file: %v, %v", info, err)
	}
}

// A program that does not finish within the renderer's timeout fails its output.
func TestRenderTimeout(t *testing.T) {
	r := renderer(t)
	r.Timeout = 100 * time.Millisecond
	d := def(t, r.Root, script(t, `exec sleep 5`), "")
	_, failures := r.Render(context.Background(), map[string]Definition{"demo": d}, input(t, demos))
	if len(failures) != 1 || failures[0].Problem != "its renderer did not finish within 100ms" {
		t.Errorf("failures = %v", failures)
	}
}

// A renderer runs as the unprivileged user, without capabilities or devices and with STATE, /var,
// and chalkd's config, credentials and Kubernetes keys hidden; it may write $OUT alone, and a
// check may write nothing. systemd stops it at its timeout.
func TestSandboxArgs(t *testing.T) {
	p := Program{Argv: []string{"/nix/store/x/bin/r", "a"}, Out: "/run/chalkos/outputs/.staging/demo.1", Write: true, Timeout: 30 * time.Second}
	args := strings.Join(sandboxArgs("chalkos-render-1.service", p, "/in"), "\n")
	for _, want := range []string{
		"--property=User=chalkos-render",
		"--property=CapabilityBoundingSet=",
		"--property=NoNewPrivileges=yes",
		"--property=PrivateDevices=yes",
		"--property=ProtectHome=yes",
		"--property=ProtectSystem=strict",
		"--property=TemporaryFileSystem=/tmp:size=16M,mode=1777",
		"--property=InaccessiblePaths=-/state -/var -/run/chalkos/config -/run/chalkos/credentials -/run/chalkos/kubernetes",
		"--property=TimeoutStartSec=30000ms",
		"--property=ReadWritePaths=/run/chalkos/outputs/.staging/demo.1",
		"--property=StandardInput=file:/in",
		"--\n/nix/store/x/bin/r\na",
	} {
		if !strings.Contains(args+"\n", want+"\n") {
			t.Errorf("the arguments lack %q:\n%s", want, args)
		}
	}
	p.Write = false
	if args := strings.Join(sandboxArgs("chalkos-render-1.service", p, "/in"), "\n"); strings.Contains(args, "ReadWritePaths") {
		t.Errorf("a check may write:\n%s", args)
	}
}
