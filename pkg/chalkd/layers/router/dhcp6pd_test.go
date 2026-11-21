package router

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// pdHook runs the hook for the hook point with the variables vars, over p's records.
func pdHook(t *testing.T, p *Plugin, hook string, vars map[string]string) {
	t.Helper()
	if err := p.dhcp6PD(hook, func(k string) string { return vars[k] }); err != nil {
		t.Fatal(err)
	}
}

// records are the names of p's records.
func records(t *testing.T, p *Plugin) []string {
	t.Helper()
	entries, err := os.ReadDir(p.PDDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A committed delegation is recorded with its link-local address and end, readable by everyone;
// leases of other types, of malformed values, and the deleted leases of a commit are not, and a
// release, decline or expiry removes the record.
func TestDHCP6PD(t *testing.T) {
	p, _ := routerNode(t)
	if err := os.Mkdir(p.PDDir+".new", 0o700); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(p.PDDir, "cust0_2001:db8:100:100::_56"), "fe80::9 1\n")
	pdHook(t, p, "leases6_committed", map[string]string{
		"QUERY6_IFACE_NAME":              "cust0",
		"QUERY6_REMOTE_ADDR":             "fe80::1",
		"DELETED_LEASES6_SIZE":           "1",
		"DELETED_LEASES6_AT0_TYPE":       "IA_PD",
		"DELETED_LEASES6_AT0_ADDRESS":    "2001:db8:100:100::",
		"DELETED_LEASES6_AT0_PREFIX_LEN": "56",
		"LEASES6_SIZE":                   "4",
		"LEASES6_AT0_TYPE":               "IA_PD",
		"LEASES6_AT0_ADDRESS":            "2001:db8:100::",
		"LEASES6_AT0_PREFIX_LEN":         "56",
		"LEASES6_AT0_CLTT":               "1800000000",
		"LEASES6_AT0_VALID_LIFETIME":     "600",
		"LEASES6_AT1_TYPE":               "IA_NA",
		"LEASES6_AT1_ADDRESS":            "2001:db8:1::10",
		"LEASES6_AT1_PREFIX_LEN":         "128",
		"LEASES6_AT1_CLTT":               "1800000000",
		"LEASES6_AT1_VALID_LIFETIME":     "600",
		"LEASES6_AT2_TYPE":               "IA_PD",
		"LEASES6_AT2_ADDRESS":            "../../etc",
		"LEASES6_AT2_PREFIX_LEN":         "56",
		"LEASES6_AT2_CLTT":               "1800000000",
		"LEASES6_AT2_VALID_LIFETIME":     "600",
		"LEASES6_AT3_TYPE":               "IA_PD",
		"LEASES6_AT3_ADDRESS":            "2001:db8:200::",
		"LEASES6_AT3_PREFIX_LEN":         "56",
		"LEASES6_AT3_CLTT":               "soon",
		"LEASES6_AT3_VALID_LIFETIME":     "600",
	})
	if got := records(t, p); !slices.Equal(got, []string{"cust0_2001:db8:100::_56"}) {
		t.Fatalf("records = %q", got)
	}
	f := filepath.Join(p.PDDir, "cust0_2001:db8:100::_56")
	if data, err := os.ReadFile(f); err != nil || string(data) != "fe80::1 1800000600\n" {
		t.Errorf("record = %q, %v", data, err)
	}
	if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("record's mode = %v, %v", fi.Mode(), err)
	}
	if left, _ := os.ReadDir(p.PDDir + ".new"); len(left) != 0 {
		t.Errorf("left %d files in the new records' directory", len(left))
	}

	// A request from an address that is not link-local, or on a link of no link's name, records
	// nothing.
	for _, vars := range []map[string]string{
		{"QUERY6_IFACE_NAME": "cust0", "QUERY6_REMOTE_ADDR": "2001:db8::1"},
		{"QUERY6_IFACE_NAME": "cust0/../x", "QUERY6_REMOTE_ADDR": "fe80::1"},
	} {
		for k, v := range map[string]string{"LEASES6_SIZE": "1", "LEASES6_AT0_TYPE": "IA_PD", "LEASES6_AT0_ADDRESS": "2001:db8:300::", "LEASES6_AT0_PREFIX_LEN": "56", "LEASES6_AT0_CLTT": "1", "LEASES6_AT0_VALID_LIFETIME": "1"} {
			vars[k] = v
		}
		pdHook(t, p, "leases6_committed", vars)
	}
	if got := records(t, p); !slices.Equal(got, []string{"cust0_2001:db8:100::_56"}) {
		t.Fatalf("records = %q", got)
	}

	// A release of an address leaves the record, a release of the prefix removes it.
	pdHook(t, p, "lease6_release", map[string]string{"QUERY6_IFACE_NAME": "cust0", "LEASE6_TYPE": "IA_NA", "LEASE6_ADDRESS": "2001:db8:100::", "LEASE6_PREFIX_LEN": "56"})
	pdHook(t, p, "lease6_renew", map[string]string{"QUERY6_IFACE_NAME": "cust0", "LEASE6_TYPE": "IA_PD", "LEASE6_ADDRESS": "2001:db8:100::", "LEASE6_PREFIX_LEN": "56"})
	if got := records(t, p); len(got) != 1 {
		t.Fatalf("records = %q", got)
	}
	pdHook(t, p, "lease6_decline", map[string]string{"QUERY6_IFACE_NAME": "cust0", "LEASE6_TYPE": "IA_PD", "LEASE6_ADDRESS": "2001:db8:100::", "LEASE6_PREFIX_LEN": "56"})
	if got := records(t, p); len(got) != 0 {
		t.Fatalf("records = %q", got)
	}

	// An expiry names no link: the prefix's records of every link go.
	touch(t, filepath.Join(p.PDDir, "cust0_2001:db8:100::_56"), "fe80::1 1\n")
	touch(t, filepath.Join(p.PDDir, "cust1_2001:db8:100::_56"), "fe80::1 1\n")
	touch(t, filepath.Join(p.PDDir, "cust1_2001:db8:100::_48"), "fe80::1 1\n")
	pdHook(t, p, "lease6_expire", map[string]string{"LEASE6_TYPE": "IA_PD", "LEASE6_ADDRESS": "2001:db8:100::", "LEASE6_PREFIX_LEN": "56"})
	if got := records(t, p); !slices.Equal(got, []string{"cust1_2001:db8:100::_48"}) {
		t.Errorf("records = %q", got)
	}
}
