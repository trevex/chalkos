package diagnostics

import (
	"slices"
	"strings"
	"testing"
)

func bound(n int64) *int64 { return &n }

var (
	// ping has a required address and a count with a default.
	ping = Definition{
		Command: []string{"/nix/store/x-iputils/bin/ping", "-n", "-c", "{count}", "{address}"},
		Parameters: map[string]Parameter{
			"address": {Type: TypeAddress},
			"count":   {Type: TypeInt, Min: bound(1), Max: bound(10), Default: "4"},
		},
	}
	// loose takes any integer, any string and values a declaration should not hold, which Argv
	// refuses still where they could read as options or break a line.
	loose = Definition{
		Command: []string{"/nix/store/x/bin/t", "{n}", "{p}", "{e}"},
		Parameters: map[string]Parameter{
			"n": {Type: TypeInt, Optional: true},
			"p": {Type: TypePattern, Regex: "^.*$", Optional: true},
			"e": {Type: TypeEnum, Values: []string{"-a", "b\n"}, Optional: true},
		},
	}
	// drill has a required name and two optional parameters, one of them within an argument.
	drill = Definition{
		Command: []string{"/nix/store/x-ldns/bin/drill", "{name}", "{type}", "@{server}"},
		Parameters: map[string]Parameter{
			"name":   {Type: TypeHostname},
			"type":   {Type: TypeEnum, Values: []string{"A", "AAAA"}, Optional: true},
			"server": {Type: TypeAddress, Optional: true},
		},
	}
)

// Argv fills each placeholder with its argument in its canonical form, or with the parameter's
// default, and drops an argument that names an optional parameter left out.
func TestArgv(t *testing.T) {
	for name, tc := range map[string]struct {
		d    Definition
		args map[string]string
		want []string
	}{
		"a default":       {ping, map[string]string{"address": "192.0.2.1"}, []string{"/nix/store/x-iputils/bin/ping", "-n", "-c", "4", "192.0.2.1"}},
		"canonical forms": {ping, map[string]string{"address": "2001:DB8:0::1", "count": "007"}, []string{"/nix/store/x-iputils/bin/ping", "-n", "-c", "7", "2001:db8::1"}},
		"a zone":          {ping, map[string]string{"address": "fe80::1%eth0"}, []string{"/nix/store/x-iputils/bin/ping", "-n", "-c", "4", "fe80::1%eth0"}},
		"left out":        {drill, map[string]string{"name": "example.org"}, []string{"/nix/store/x-ldns/bin/drill", "example.org."}},
		"in an argument":  {drill, map[string]string{"name": "example.org.", "type": "AAAA", "server": "192.0.2.53"}, []string{"/nix/store/x-ldns/bin/drill", "example.org.", "AAAA", "@192.0.2.53"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.d.Argv(tc.args)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Errorf("Argv = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// Argv refuses a call that leaves out a required parameter, names one the diagnostic lacks, or
// gives a value that is not of its parameter's type.
func TestArgvRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		d    Definition
		args map[string]string
		want string
	}{
		"a missing argument":     {ping, nil, "it needs address=<address>"},
		"an unknown parameter":   {ping, map[string]string{"address": "192.0.2.1", "size": "56"}, "it has no parameter size; it takes address, count"},
		"no address":             {ping, map[string]string{"address": "example.org"}, `address: "example.org" is no IPv4 or IPv6 address`},
		"an option":              {drill, map[string]string{"name": "-x"}, `name: "-x" is no DNS name`},
		"a hyphen at the end":    {drill, map[string]string{"name": "a-.example"}, `name: "a-.example" is no DNS name`},
		"out of range":           {ping, map[string]string{"address": "192.0.2.1", "count": "11"}, "count: 11 is not from 1 to 10"},
		"no integer":             {ping, map[string]string{"address": "192.0.2.1", "count": "four"}, `count: "four" is no integer`},
		"not a value":            {drill, map[string]string{"name": "example.org", "type": "ANY"}, `type: "ANY" is not one of A, AAAA`},
		"an empty optional":      {drill, map[string]string{"name": "example.org", "server": ""}, `server: "" is no IPv4 or IPv6 address`},
		"a zone with a variable": {ping, map[string]string{"address": "fe80::1%${PATH}"}, `address: "fe80::1%${PATH}" has a zone that is no interface name`},
		"a zone with a newline":  {ping, map[string]string{"address": "fe80::1%a b\n-c"}, `address: "fe80::1%a b\n-c" has a zone that is no interface name`},
		"a negative int":         {loose, map[string]string{"n": "-1"}, `n: "-1" starts with "-"`},
		"an option of a pattern": {loose, map[string]string{"p": "-rf"}, `p: "-rf" starts with "-"`},
		"a NUL":                  {loose, map[string]string{"p": "a\x00b"}, `p: "a\x00b" holds a control character`},
		"a DEL":                  {loose, map[string]string{"p": "a\x7fb"}, `p: "a\x7fb" holds a control character`},
		"a newline of an enum":   {loose, map[string]string{"e": "b\n"}, `e: "b\n" holds a control character`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tc.d.Argv(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// Each type takes its values and passes them in its form; a pattern matches the whole value.
func TestValues(t *testing.T) {
	for _, tc := range []struct {
		p             Parameter
		in, want, err string
	}{
		{Parameter{Type: TypePrefix}, "2001:db8::/32", "2001:db8::/32", ""},
		{Parameter{Type: TypePrefix}, "192.0.2.1", "", `"192.0.2.1" is no IPv4 or IPv6 prefix`},
		{Parameter{Type: TypeInterface}, "eth0.100", "eth0.100", ""},
		{Parameter{Type: TypeInterface}, "-eth0", "", `"-eth0" is no interface name`},
		{Parameter{Type: TypeInterface}, "abcdefghijklmnop", "", `"abcdefghijklmnop" is no interface name`},
		{Parameter{Type: TypeHostname}, "_sip._tcp.example.org", "_sip._tcp.example.org.", ""},
		{Parameter{Type: TypeHostname}, "mx", "mx.", ""},
		{Parameter{Type: TypeInt, Max: bound(5)}, "6", "", "6 is not at most 5"},
		{Parameter{Type: TypePattern, Regex: "^[a-z]+$"}, "main", "main", ""},
		{Parameter{Type: TypePattern, Regex: "^a|b$"}, "ab", "", `"ab" does not match ^a|b$`},
		{Parameter{Type: TypePattern, Regex: "^[a$"}, "a", "", "error parsing regexp"},
	} {
		got, err := tc.p.value(tc.in)
		if tc.err == "" && (err != nil || got != tc.want) || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%s %q: %q, %v; want %q, %q", tc.p.Type, tc.in, got, err, tc.want, tc.err)
		}
	}
}

// ParameterNames lists the parameters a call must give first, then the others, each in the
// order the command names them.
func TestParameterNames(t *testing.T) {
	if got := ping.ParameterNames(); !slices.Equal(got, []string{"address", "count"}) {
		t.Errorf("ping: %q", got)
	}
	if got := drill.ParameterNames(); !slices.Equal(got, []string{"name", "type", "server"}) {
		t.Errorf("drill: %q", got)
	}
}
