package layer

import (
	"maps"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// --address takes ADDR, the address of the one node a command reaches, and NODE=ADDR of any
// node, repeated; the later value for a node counts.
func TestAddresses(t *testing.T) {
	parse := func(args ...string) (Addresses, error) {
		var a Addresses
		fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
		fs.Var(&a, "address", "")
		var flags []string
		for _, v := range args {
			flags = append(flags, "--address", v)
		}
		return a, fs.Parse(flags)
	}
	for _, bad := range []string{"", "=10.0.0.1", "n1="} {
		if _, err := parse(bad); err == nil || !strings.Contains(err.Error(), "want ADDR or NODE=ADDR") {
			t.Errorf("--address %q: %v, want it refused", bad, err)
		}
	}
	a, err := parse("10.0.0.1", "n2=10.0.0.2", "n1=10.0.0.3", "n2=10.0.0.4")
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Resolve("n1")
	if want := map[string]string{"n1": "10.0.0.3", "n2": "10.0.0.4"}; err != nil || !maps.Equal(got, want) {
		t.Errorf("Resolve(n1) = %v, %v, want %v", got, err, want)
	}
	if bare := a.Bare(); bare != "10.0.0.1" {
		t.Errorf("Bare() = %q, want 10.0.0.1", bare)
	}
	if _, err := a.Resolve(""); err == nil || !strings.Contains(err.Error(), "with several nodes, give each node's address as NODE=ADDR") {
		t.Errorf("Resolve() with a bare ADDR: %v, want it refused", err)
	}
	named, err := parse("n1=10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := named.Resolve(""); err != nil || got["n1"] != "10.0.0.1" || named.Bare() != "" {
		t.Errorf("Resolve() = %v, %v, Bare() = %q; want n1's address alone", got, err, named.Bare())
	}
}

// Named tells a NODE=ADDR among the values.
func TestAddressesNamed(t *testing.T) {
	var a Addresses
	if err := a.Set("10.0.0.1"); err != nil || a.Named() {
		t.Errorf("a bare ADDR: Named() = %v, %v", a.Named(), err)
	}
	if err := a.Set("n1=10.0.0.2"); err != nil || !a.Named() {
		t.Errorf("with NODE=ADDR: Named() = %v, %v", a.Named(), err)
	}
}
