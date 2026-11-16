package frr

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

const (
	vtyshShow  = "vtysh -c show running-config"
	vtyshApply = "vtysh -f"
)

func TestReload(t *testing.T) {
	changed := edit(t, " neighbor peer-a password one", " neighbor peer-a password two")
	for _, tc := range []struct {
		name string
		// shows are the running configs vtysh shows in turn, the last from then on.
		shows   []string
		calls   []string
		wantErr string
	}{{
		name:  "no change runs nothing",
		shows: []string{changed},
		calls: []string{vtyshShow},
	}, {
		name:  "a change applies once",
		shows: []string{base, changed},
		calls: []string{vtyshShow, vtyshApply, vtyshShow},
	}, {
		name:  "what a pass leaves out a second applies",
		shows: []string{base, base, changed},
		calls: []string{vtyshShow, vtyshApply, vtyshShow, vtyshApply, vtyshShow},
	}, {
		name:    "a config that does not change fails after two passes",
		shows:   []string{base},
		calls:   []string{vtyshShow, vtyshApply, vtyshShow, vtyshApply, vtyshShow},
		wantErr: "still differs from frr.conf after two passes, by:\nhostname r1\nrouter bgp 64512\n neighbor peer-a password <hidden>",
	}, {
		name:    "a running config of another command fails",
		shows:   []string{"router bgp 64512\n neighbor x ebgp-multihop 2\nexit\n"},
		calls:   []string{vtyshShow},
		wantErr: `FRR's running config: line 7: "neighbor x ebgp-multihop 2" in router bgp 64512 is no command chalkos writes; restart frr.service`,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			r := &tooltest.Runner{}
			for i, s := range tc.shows {
				times := 1
				if i == len(tc.shows)-1 {
					times = 0
				}
				r.Rules = append(r.Rules, tooltest.Rule{Prefix: vtyshShow, Out: "Building configuration...\n\nCurrent configuration:\n!\nfrr version 10.7.1\n" + s, Times: times})
			}
			dir := t.TempDir()
			r.Before = func(line string) {
				file, ok := strings.CutPrefix(line, vtyshApply+" ")
				if !ok {
					return
				}
				if filepath.Dir(file) != dir {
					t.Errorf("vtysh -f reads %s, not a file in %s", file, dir)
				}
				fi, err := os.Stat(file)
				if err != nil || fi.Mode().Perm() != 0o600 {
					t.Errorf("vtysh -f reads %s: %v, %v", file, fi, err)
				}
				if b, _ := os.ReadFile(file); string(b) != "hostname r1\nrouter bgp 64512\n neighbor peer-a password two\nexit\n" {
					t.Errorf("vtysh -f reads %q", b)
				}
			}
			err := Reload(context.Background(), r, "vtysh", dir, mustParse(t, changed), log.New(io.Discard, "", 0))
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Reload() = %v, want an error holding %q", err, tc.wantErr)
			}
			calls := slices.Clone(r.Calls)
			for i, c := range calls {
				if strings.HasPrefix(c, vtyshApply+" ") {
					calls[i] = vtyshApply
				}
			}
			if !slices.Equal(calls, tc.calls) {
				t.Errorf("calls %q, want %q", calls, tc.calls)
			}
			if left, _ := os.ReadDir(dir); len(left) > 0 {
				t.Errorf("the reload left %v", left)
			}
		})
	}
}

// vtysh repeats a line it fails on on standard error, which the reload runs quietly and logs
// without the password.
func TestReloadHidesPasswordsOfRefusedLines(t *testing.T) {
	const secret = "s3cret-value"
	target := edit(t, " neighbor peer-a password one", " neighbor peer-a password "+secret)
	r := &tooltest.Runner{Rules: []tooltest.Rule{
		{Prefix: vtyshShow, Out: base},
		{Prefix: vtyshApply, Err: &tool.Error{Command: "vtysh -f", Code: 2, Stderr: "line 3: Failure to communicate[13] to bgpd, line:  neighbor peer-a password " + secret + "\n"}},
	}}
	var logged strings.Builder
	err := Reload(context.Background(), r, "vtysh", t.TempDir(), mustParse(t, target), log.New(&logged, "", 0))
	if err == nil {
		t.Fatal("Reload() succeeded with a config FRR does not take")
	}
	for _, s := range []string{logged.String(), err.Error()} {
		if strings.Contains(s, secret) {
			t.Errorf("the reload shows the password:\n%s", s)
		}
	}
	if !strings.Contains(logged.String(), "line:  neighbor peer-a password <hidden>") {
		t.Errorf("the log lacks vtysh's refusal:\n%s", logged.String())
	}
	for _, c := range r.Cmds {
		if strings.HasPrefix(c.String(), vtyshApply) && !c.Quiet {
			t.Errorf("%s runs with its standard error on the console", c)
		}
	}
}
