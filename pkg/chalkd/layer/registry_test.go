package layer

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/layers"
)

type fake struct {
	name    string
	actions map[string]func(context.Context) error
}

func (f fake) Name() string { return f.name }
func (f fake) Handler(...connect.HandlerOption) (string, http.Handler) {
	return "/" + f.name + "/", http.NotFoundHandler()
}
func (f fake) Permissions() map[string]Permission              { return nil }
func (f fake) Start(context.Context) error                     { return nil }
func (f fake) Actions() map[string]func(context.Context) error { return f.actions }

type issuer struct{ fake }

func (issuer) IssueNodeCertificate(context.Context, []byte) (string, error) { return "", nil }

type commander struct {
	fake
	commands map[string]Command
}

func (c commander) Commands() map[string]Command { return c.commands }

func names(plugins []Plugin) []string {
	var n []string
	for _, p := range plugins {
		n = append(n, p.Name())
	}
	return n
}

// The plugins of the image's layers are active, in name order; every compiled one is listed.
func TestRegistryActivatesTheImagesLayers(t *testing.T) {
	r, err := NewRegistry(layers.Of("router"), fake{name: "router"}, fake{name: "kubernetes"})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(r.Compiled()); !slices.Equal(got, []string{"kubernetes", "router"}) {
		t.Errorf("compiled %v", got)
	}
	if got := names(r.Active()); !slices.Equal(got, []string{"router"}) {
		t.Errorf("active %v", got)
	}
	var none *Registry
	if none.Layers().Has("router") || len(none.Active()) != 0 || none.Issuer() != nil {
		t.Error("a nil registry has layers")
	}
}

// The registry refuses a layer no plugin serves, a layer two plugins serve and two active issuers.
func TestRegistryRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		image   layers.Set
		plugins []Plugin
		want    string
	}{
		"unserved marker": {layers.Of("router"), []Plugin{fake{name: "kubernetes"}}, "the image carries the router layer, which this chalkd has no plugin for"},
		"twice":           {layers.Of(), []Plugin{fake{name: "router"}, fake{name: "router"}}, "two plugins serve the router layer"},
		"two issuers":     {layers.Of("a", "b"), []Plugin{issuer{fake{name: "a"}}, issuer{fake{name: "b"}}}, "the a and b layers each issue node certificates"},
	} {
		if _, err := NewRegistry(tc.image, tc.plugins...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// An action is the active plugin's, by <layer>:<action>; another layer's or an unknown one is refused.
func TestRegistryAction(t *testing.T) {
	ran := ""
	act := func(name string) func(context.Context) error {
		return func(context.Context) error { ran = name; return nil }
	}
	r, err := NewRegistry(layers.Of("kubernetes"),
		fake{name: "kubernetes", actions: map[string]func(context.Context) error{"restart": act("restart")}},
		fake{name: "router", actions: map[string]func(context.Context) error{"reload": act("reload")}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := r.Action("kubernetes:restart")
	if err != nil || run(context.Background()) != nil || ran != "restart" {
		t.Errorf("kubernetes:restart: %v, ran %q", err, ran)
	}
	for _, name := range []string{"router:reload", "kubernetes:reload", "restart"} {
		if _, err := r.Action(name); err == nil {
			t.Errorf("%s was found", name)
		}
	}
}

// Only active issuers count: a compiled issuer of a layer the image lacks leaves the active one
// the image's issuer.
func TestRegistryCountsActiveIssuers(t *testing.T) {
	r, err := NewRegistry(layers.Of("b"), issuer{fake{name: "a"}}, issuer{fake{name: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := r.Issuer().(Plugin); !ok || got.Name() != "b" {
		t.Errorf("issuer %v, want the b layer's", got)
	}
}

// Active[T] and Compiled[T] list the plugins that implement the hook, of the active and the
// compiled plugins.
func TestRegistryHooks(t *testing.T) {
	r, err := NewRegistry(layers.Of("c"), issuer{fake{name: "a"}}, fake{name: "b"}, issuer{fake{name: "c"}}, fake{name: "d"})
	if err != nil {
		t.Fatal(err)
	}
	hookNames := func(list []Issuer) []string {
		var n []string
		for _, h := range list {
			n = append(n, h.(Plugin).Name())
		}
		return n
	}
	if got := hookNames(Compiled[Issuer](r)); !slices.Equal(got, []string{"a", "c"}) {
		t.Errorf("compiled issuers %v", got)
	}
	if got := hookNames(Active[Issuer](r)); !slices.Equal(got, []string{"c"}) {
		t.Errorf("active issuers %v", got)
	}
}

// A command is found on a compiled plugin whether or not its layer is active; an unknown layer or
// command is not.
func TestRegistryCommand(t *testing.T) {
	r, err := NewRegistry(layers.Of(), commander{fake{name: "router"}, map[string]Command{"show": {Args: "[FILE]"}}})
	if err != nil {
		t.Fatal(err)
	}
	if cmd, ok := r.Command("router", "show"); !ok || cmd.Args != "[FILE]" {
		t.Errorf("router show: %+v, %v", cmd, ok)
	}
	for _, c := range [][2]string{{"router", "hide"}, {"kubernetes", "show"}} {
		if _, ok := r.Command(c[0], c[1]); ok {
			t.Errorf("%s %s was found", c[0], c[1])
		}
	}
}
