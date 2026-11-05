package rules

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
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
		{`portRange("70000-80000")`, "70000", "portRange: the argument is not a port or a range of ports from 1 to 65535, the first no higher than the last"},
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

// An address range's last address, addresses in order, and ranges of ports as the router kinds
// write them.
func TestLibraryAddressesAndPorts(t *testing.T) {
	for _, tc := range []struct {
		expr string
		x    any
		want any
	}{
		{`string(cidr("192.0.2.0/24").last())`, nil, "192.0.2.255"},
		{`string(cidr("192.0.2.77/24").last())`, nil, "192.0.2.255"},
		{`string(cidr("10.0.0.1/32").last())`, nil, "10.0.0.1"},
		{`string(cidr("0.0.0.0/0").last())`, nil, "255.255.255.255"},
		{`string(cidr("2001:db8::/64").last())`, nil, "2001:db8::ffff:ffff:ffff:ffff"},
		{`string(cidr("2001:db8::/63").last())`, nil, "2001:db8:0:1:ffff:ffff:ffff:ffff"},
		{`cidr("192.0.2.0/24").last() == ip("192.0.2.255")`, nil, true},
		{`ip("10.0.0.2").compare(ip("10.0.0.10"))`, nil, int64(-1)},
		{`ip("10.0.0.10").compare(ip("10.0.0.2"))`, nil, int64(1)},
		{`ip("fd00:0::a").compare(ip("fd00::A"))`, nil, int64(0)},
		{`ip("255.255.255.255").compare(ip("::"))`, nil, int64(-1)},
		{`portRange(22).first == 22 && portRange(22).last == 22`, nil, true},
		{`portRange("8000-8100").first`, nil, int64(8000)},
		{`portRange(x).last`, "1-65535", int64(65535)},
		{`portRange("443-443").last`, nil, int64(443)},
	} {
		got, err := eval(t, tc.expr, tc.x)
		if err != nil {
			t.Errorf("%s: %v", tc.expr, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s = %v (%T), want %v", tc.expr, got, got, tc.want)
		}
	}
	for _, s := range []string{"9-1", "0-10", "1-65536", "70000-80000", "01-10", "+1-2", "1-", "-1", "1--2", "a-b", ""} {
		_, err := eval(t, `portRange(x)`, s)
		if err == nil || err.Error() != errNotPortRange {
			t.Errorf("portRange(%q): err = %v, want %q", s, err, errNotPortRange)
		}
	}
	for _, n := range []int64{0, -1, 65536} {
		_, err := eval(t, `portRange(x)`, n)
		if err == nil || err.Error() != errNotPortRange {
			t.Errorf("portRange(%d): err = %v, want %q", n, err, errNotPortRange)
		}
	}
}

// isDNSName and isRecordData accept what Unbound's local zones take, as chalkos-render's unbound
// output checks it.
func TestLibraryDNS(t *testing.T) {
	long := strings.Repeat("a", 63)
	for _, tc := range []struct {
		expr string
		want bool
	}{
		{`isDNSName("lan.example.org")`, true},
		{`isDNSName("_sip._tcp.lan")`, true},
		{`isDNSName("a")`, true},
		{`isDNSName("` + long + `.org")`, true},
		{`isDNSName("` + long + `a.org")`, false},
		{`isDNSName("` + strings.Repeat(long+".", 3) + strings.Repeat("a", 61) + `")`, true},
		{`isDNSName("` + strings.Repeat(long+".", 3) + strings.Repeat("a", 62) + `")`, false},
		{`isDNSName("Lan.example.org")`, false},
		{`isDNSName("lan.example.org.")`, false},
		{`isDNSName("-lan.org")`, false},
		{`isDNSName("lan-.org")`, false},
		{`isDNSName("lan..org")`, false},
		{`isDNSName("")`, false},
		{`isRecordData("A", "192.0.2.1")`, true},
		{`isRecordData("A", "2001:db8::1")`, false},
		{`isRecordData("A", "192.0.2.01")`, false},
		{`isRecordData("AAAA", "2001:DB8::1")`, true},
		{`isRecordData("AAAA", "::ffff:192.0.2.1")`, false},
		{`isRecordData("AAAA", "fe80::1%eth0")`, false},
		{`isRecordData("CNAME", "www.example.org")`, true},
		{`isRecordData("PTR", "host.lan")`, true},
		{`isRecordData("CNAME", "www.example.org.")`, false},
		{`isRecordData("PTR", "Host.lan")`, false},
		{`isRecordData("MX", "10 mail.lan")`, true},
		{`isRecordData("MX", "0 mail.lan")`, true},
		{`isRecordData("MX", "65535 mail.lan")`, true},
		{`isRecordData("MX", "65536 mail.lan")`, false},
		{`isRecordData("MX", "010 mail.lan")`, false},
		{`isRecordData("MX", "10  mail.lan")`, false},
		{`isRecordData("MX", "mail.lan")`, false},
		{`isRecordData("SRV", "0 5 5060 sip.lan")`, true},
		{`isRecordData("SRV", "0 5 sip.lan")`, false},
		{`isRecordData("SRV", "0 5 5060 sip.lan.")`, false},
		{`isRecordData("SRV", "+0 5 5060 sip.lan")`, false},
		{`isRecordData("TXT", "v=spf1 -all")`, true},
		{`isRecordData("TXT", "")`, false},
		{`isRecordData("TXT", "say \"hi\"")`, false},
		{`isRecordData("TXT", "` + strings.Repeat("x", 256) + `")`, false},
		{`isRecordData("NS", "ns.lan")`, false},
	} {
		got, err := eval(t, tc.expr, nil)
		if err != nil || got != tc.want {
			t.Errorf("%s = %v, %v; want %v", tc.expr, got, err, tc.want)
		}
	}
}

// The library's functions cost one step, and isDNSName and isRecordData a tenth of a step more
// per character of the string they check, as CEL's own contains() does: the build estimates them
// with sizeHint characters, 26 steps more, and a node counts the characters it meets.
func TestLibraryCosts(t *testing.T) {
	e, err := newEnv("x")
	if err != nil {
		t.Fatal(err)
	}
	// Each estimate holds a step for each literal and variable the call reads.
	for expr, want := range map[string]uint64{
		`ip("10.0.0.1").compare(ip("10.0.0.2"))`: 3,
		`cidr("10.0.0.0/8").last()`:              2,
		`portRange(x)`:                           2,
		`isDNSName(x)`:                           2 + 26,
		`isRecordData("MX", x)`:                  2 + 26,
		`x.contains("a")`:                        1 + 26,
	} {
		ast, iss := e.Compile(expr)
		if iss.Err() != nil {
			t.Fatalf("%s: %v", expr, iss.Err())
		}
		est, err := e.EstimateCost(ast, estimator{})
		if err != nil {
			t.Fatal(err)
		}
		if est.Max != want {
			t.Errorf("%s: estimated at most %d, want %d", expr, est.Max, want)
		}
	}
	ast, iss := e.Compile(`isDNSName(x)`)
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	prg, err := e.Program(ast, cel.CostLimit(runtimeCostLimit))
	if err != nil {
		t.Fatal(err)
	}
	for n, want := range map[int]uint64{10: 3, 1000: 102} {
		_, det, err := prg.Eval(map[string]any{"x": strings.Repeat("a", n)})
		if err != nil {
			t.Fatal(err)
		}
		if got := *det.ActualCost(); got != want {
			t.Errorf("isDNSName of %d characters costs %d, want %d", n, got, want)
		}
	}
}
