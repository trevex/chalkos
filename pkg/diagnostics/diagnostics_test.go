package diagnostics

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "diagnostics.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Read reads each diagnostic as the image's build writes it.
func TestRead(t *testing.T) {
	path := write(t, `{"route-get": {"description": "The route the kernel takes to an address.",
	  "command": ["/nix/store/x-iproute2/bin/ip", "-json", "route", "get", "{address}"],
	  "parameters": {"address": {"type": "address", "description": "IPv4 or IPv6 address.", "optional": false,
	    "default": "", "min": null, "max": null, "values": [], "regex": ""}},
	  "role": "reader", "timeout": "10s", "capabilities": [], "groups": []}}`)
	got, err := Read(path)
	want := map[string]Definition{"route-get": {
		Description:  "The route the kernel takes to an address.",
		Command:      []string{"/nix/store/x-iproute2/bin/ip", "-json", "route", "get", "{address}"},
		Parameters:   map[string]Parameter{"address": {Type: TypeAddress, Description: "IPv4 or IPv6 address.", Values: []string{}}},
		Role:         "reader",
		Timeout:      "10s",
		Capabilities: []string{},
		Groups:       []string{},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics = %+v, %v; want %+v", got, err, want)
	}
	if d := got["route-get"].RunTime(); d != 10*time.Second {
		t.Errorf("RunTime = %v, want 10s", d)
	}
	if got, err := Read(filepath.Join(t.TempDir(), "missing.json")); err != nil || len(got) != 0 {
		t.Errorf("a missing file: %v, %v", got, err)
	}
}

// Read refuses a diagnostic chalkd could not run as declared, naming the file, the diagnostic and
// what is wrong.
func TestReadRefuses(t *testing.T) {
	const (
		ip = `"command": ["/nix/store/x-iproute2/bin/ip", "route", "show", "table", "{table}"]`
		ok = `"role": "reader", "timeout": "10s"`
	)
	table := func(p string) string { return `{` + ip + `, ` + ok + `, "parameters": {"table": ` + p + `}}` }
	for name, tc := range map[string]struct{ name, diagnostic, want string }{
		"a name":                    {"Routes", `{"command": ["/nix/store/x/bin/ip"], ` + ok + `}`, "diagnostic Routes: the name is not of the form [a-z][a-z0-9-]*"},
		"no command":                {"routes", `{` + ok + `}`, "diagnostic routes: its command is empty"},
		"outside the store":         {"routes", `{"command": ["/usr/bin/ip"], ` + ok + `}`, "its command runs /usr/bin/ip, which is no program of the image's store"},
		"a role":                    {"routes", `{"command": ["/nix/store/x/bin/ip"], "role": "root", "timeout": "10s"}`, `its role is "root", not reader, operator or admin`},
		"no timeout":                {"routes", `{"command": ["/nix/store/x/bin/ip"], "role": "reader"}`, `its timeout is "", not a duration from 1s to 10m`},
		"a long timeout":            {"routes", `{"command": ["/nix/store/x/bin/ip"], "role": "reader", "timeout": "11m"}`, `its timeout is "11m", not a duration from 1s to 10m`},
		"a capability":              {"routes", `{"command": ["/nix/store/x/bin/ip"], ` + ok + `, "capabilities": ["net_raw"]}`, `"net_raw" is no capability, such as CAP_NET_RAW`},
		"no parameter":              {"routes", `{` + ip + `, ` + ok + `}`, "its command names {table}, which is no parameter"},
		"no placeholder":            {"routes", `{"command": ["/nix/store/x/bin/ip"], ` + ok + `, "parameters": {"table": {"type": "int"}}}`, "parameter table: its command names no {table}"},
		"a parameter's name":        {"routes", `{"command": ["/nix/store/x/bin/ip", "{Table}"], ` + ok + `, "parameters": {"Table": {"type": "int"}}}`, `parameter "Table": the name is not of the form [a-z][a-z0-9-]*`},
		"a type":                    {"routes", table(`{"type": "string"}`), `parameter table: its type is "string", not one of address, prefix, hostname, interface, int, enum, pattern`},
		"bounds of a pattern":       {"routes", table(`{"type": "pattern", "regex": "^[a-z]+$", "min": 1}`), "parameter table: only an int parameter has min and max"},
		"bounds the wrong way":      {"routes", table(`{"type": "int", "min": 10, "max": 1}`), "parameter table: its min 10 is above its max 1"},
		"an enum without values":    {"routes", table(`{"type": "enum"}`), "parameter table: an enum parameter has values, and no other does"},
		"values of an int":          {"routes", table(`{"type": "int", "values": ["1"]}`), "parameter table: an enum parameter has values, and no other does"},
		"a pattern without regex":   {"routes", table(`{"type": "pattern"}`), "parameter table: a pattern parameter has a regex, and no other does"},
		"an unanchored regex":       {"routes", table(`{"type": "pattern", "regex": "[a-z]+"}`), `parameter table: its regex "[a-z]+" is not anchored with ^ and $`},
		"a broken regex":            {"routes", table(`{"type": "pattern", "regex": "^[a-z$"}`), "parameter table: its regex: error parsing regexp: missing closing ]"},
		"optional with a default":   {"routes", table(`{"type": "int", "optional": true, "default": "1"}`), "parameter table: it is optional and has a default"},
		"a default of another type": {"routes", table(`{"type": "int", "min": 1, "default": "0"}`), "parameter table: its default: 0 is not at least 1"},
		"accepted":                  {"routes", table(`{"type": "pattern", "regex": "^(main|[0-9]+)$", "default": "main"}`), ""},
	} {
		t.Run(name, func(t *testing.T) {
			path := write(t, `{"`+tc.name+`": `+tc.diagnostic+`}`)
			_, err := Read(path)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("err = %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), path+": diagnostic ")):
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
