package router

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// load-firewall loads the rendered ruleset, the fallback without one, and the fallback and fails
// when nft refuses the rendered one.
func TestLoadFirewall(t *testing.T) {
	const fallback = "/nix/store/x-fallback.nft"
	for name, c := range map[string]struct {
		rendered, refuse bool
		loads            []string
		says             string
		status           bool
	}{
		"rendered":    {rendered: true, loads: []string{"ruleset"}},
		"none":        {loads: []string{fallback}, says: "the node has no rendered ruleset; loading the fallback ruleset"},
		"refused":     {rendered: true, refuse: true, loads: []string{"ruleset", fallback}, says: "; loading the fallback ruleset, which forwards nothing", status: true},
		"none failed": {refuse: true, loads: []string{fallback}, says: "the node has no rendered ruleset"},
	} {
		t.Run(name, func(t *testing.T) {
			p, r := routerNode(t)
			ruleset := filepath.Join(t.TempDir(), "ruleset.nft")
			if c.rendered {
				touch(t, ruleset, "table inet chalkos {}\n")
			}
			if c.refuse {
				r.Rules = append(r.Rules, tooltest.Rule{Prefix: "nft -f ", Err: errors.New("exit status 1"), Times: 1})
			}
			var stderr bytes.Buffer
			err := p.loadFirewall(context.Background(), ruleset, fallback, &stderr)
			switch {
			case c.status && err != cli.Status(1):
				t.Errorf("error %v, want status 1", err)
			case !c.status && (err != nil) != c.refuse:
				t.Errorf("error %v", err)
			}
			var want []string
			for _, l := range c.loads {
				want = append(want, "nft -f "+strings.Replace(l, "ruleset", ruleset, 1))
			}
			if !slices.Equal(r.Calls, want) {
				t.Errorf("calls = %q, want %q", r.Calls, want)
			}
			if !strings.Contains(stderr.String(), c.says) || (c.says == "") != (stderr.Len() == 0) {
				t.Errorf("said %q, want %q", stderr.String(), c.says)
			}
		})
	}
}
