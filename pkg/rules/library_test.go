package rules

import (
	"strings"
	"testing"
)

// eval evaluates expr with x bound to the value given.
func eval(t *testing.T, expr string, x any) (any, error) {
	t.Helper()
	e, err := newEnv("x")
	if err != nil {
		t.Fatal(err)
	}
	ast, iss := e.Compile(expr)
	if iss.Err() != nil {
		t.Fatalf("compile %s: %v", expr, iss.Err())
	}
	prg, err := e.Program(ast)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := prg.Eval(map[string]any{"x": x})
	if err != nil {
		return nil, err
	}
	return out.Value(), nil
}

func TestLibrary(t *testing.T) {
	for _, tc := range []struct {
		expr string
		x    any
		want any
	}{
		// Ranges and addresses compare as what they denote.
		{`cidr("fd00:0::/108") == cidr("fd00::/108")`, nil, true},
		{`cidr("10.244.1.0/16") == cidr("10.244.0.0/16")`, nil, true},
		{`cidr("10.244.0.0/16") == cidr("10.245.0.0/16")`, nil, false},
		{`ip("fd00:10:96:0::a") == ip("fd00:10:96::a")`, nil, true},
		{`string(cidr("fd00:0:0::1/108"))`, nil, "fd00::/108"},
		{`string(ip("FD00::A"))`, nil, "fd00::a"},
		// The 10th address of the service range is the cluster DNS's default.
		{`cidr(x).ip(10) == ip("10.96.0.10")`, "10.96.0.0/12", true},
		{`string(cidr("fd00:10:96::/112").ip(10))`, nil, "fd00:10:96::a"},
		{`cidr("10.96.0.0/12").prefix()`, nil, int64(12)},
		{`cidr("10.96.0.0/12").contains(ip("10.97.0.1"))`, nil, true},
		{`cidr("10.96.0.0/12").contains(ip("10.112.0.1"))`, nil, false},
		{`ip("10.0.0.1").family()`, nil, "ipv4"},
		{`ip("fd00::1").family() == "ipv6"`, nil, true},
		// Sizes are powers of 1024, as repart reads them.
		{`bytes("1G") == bytes("1024M")`, nil, true},
		{`bytes(x)`, "512M", int64(512 << 20)},
		{`bytes("2T") > bytes("1T")`, nil, true},
		{`isSize("1024M") && !isSize("1X") && !isSize("8192P") && isSize("8191P")`, nil, true},
		// The extensions are there.
		{`[3, 1, 2].sort()`, nil, nil},
		{`sets.contains([1, 2, 3], [2])`, nil, true},
		{`["a", "b"].join(", ")`, nil, "a, b"},
		{`cel.bind(r, cidr("10.0.0.0/8"), r.prefix() + 1)`, nil, int64(9)},
	} {
		got, err := eval(t, tc.expr, tc.x)
		if err != nil {
			t.Errorf("%s: %v", tc.expr, err)
			continue
		}
		if tc.want != nil && got != tc.want {
			t.Errorf("%s = %v (%T), want %v", tc.expr, got, got, tc.want)
		}
	}
}

// A function's error names the function and not its argument, which may be a value of the config
// or a fact that a message must not show.
func TestLibraryErrors(t *testing.T) {
	for _, tc := range []struct{ expr, input, want string }{
		{`cidr("10.0.0.7")`, "10.0.0.7", "cidr: the argument is not an address range"},
		{`ip("10.0.0.256")`, "10.0.0.256", "ip: the argument is not an address"},
		{`cidr("10.0.0.0/30").ip(4)`, "10.0.0.0", "past the range"},
		{`cidr("10.0.0.0/30").ip(-1)`, "10.0.0.0", "negative"},
		{`cidr("fd00::/127").ip(2)`, "fd00", "past the range"},
		{`bytes("17X")`, "17X", "bytes: the argument is not a size"},
		{`bytes("99999999P")`, "99999999", "bytes: the argument is not a size below 8 EiB"},
		{`bytes("18446744073709551615")`, "18446744073709551615", "bytes: the argument is not a size below 8 EiB"},
	} {
		_, err := eval(t, tc.expr, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), tc.input) {
			t.Errorf("%s: err = %v, want %q without %q", tc.expr, err, tc.want, tc.input)
		}
	}
}

// Maps iterate in key order, so lists built from them, such as of document names, are sorted,
// also in the maps they hold.
func TestMapsIterateInKeyOrder(t *testing.T) {
	inner := map[string]any{}
	outer := map[string]any{"in": inner}
	for _, k := range strings.Split("q w e r t y u i o p a s d f", " ") {
		inner[k] = true
		outer[k] = true
	}
	for expr, want := range map[string]string{
		`x.map(k, k).join("")`:                   "adefiinopqrstuwy",
		`x["in"].filter(k, x["in"][k]).join("")`: "adefiopqrstuwy",
	} {
		got, err := eval(t, expr, outer)
		if err != nil || got != want {
			t.Errorf("%s = %v, %v; want %q", expr, got, err, want)
		}
	}
}
