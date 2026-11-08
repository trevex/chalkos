package units

import "testing"

// Each service names its instance of a VRF, and of the default VRF, by its config file, its unit
// and its device's index record, and reads the VRF back from the config file's name. A VRF's
// dashes reach the unit escaped, as systemd-escape escapes them.
func TestService(t *testing.T) {
	for _, tc := range []struct {
		s                         Service
		vrf, file, unit, dir, rec string
	}{
		{Kea4, "default", "kea-dhcp4.json", "kea-dhcp4.service", "kea", ""},
		{Kea4, "guest", "kea-dhcp4-guest.json", "kea-dhcp4@guest.service", "kea", "kea/kea-dhcp4-guest.ifindex"},
		{Kea6, "default", "kea-dhcp6.json", "kea-dhcp6.service", "kea", ""},
		{Kea6, "dmz-a", "kea-dhcp6-dmz-a.json", `kea-dhcp6@dmz\x2da.service`, "kea", "kea/kea-dhcp6-dmz-a.ifindex"},
		{Unbound, "default", "unbound.conf", "unbound.service", "unbound", ""},
		{Unbound, "dmz-a", "unbound-dmz-a.conf", `unbound@dmz\x2da.service`, "unbound-dmz-a", "unbound-dmz-a/unbound-dmz-a.ifindex"},
		{Unbound, "v1.2", "unbound-v1.2.conf", "unbound@v1.2.service", "unbound-v1.2", "unbound-v1.2/unbound-v1.2.ifindex"},
		{Relay, "default", "dnsmasq-relay.conf", "dnsmasq-relay.service", "dnsmasq-relay", ""},
		{Relay, "a-b-c", "dnsmasq-relay-a-b-c.conf", `dnsmasq-relay@a\x2db\x2dc.service`, "dnsmasq-relay", "dnsmasq-relay/dnsmasq-relay-a-b-c.ifindex"},
	} {
		if got := tc.s.File(tc.vrf); got != tc.file {
			t.Errorf("%s.File(%q) = %q, want %q", tc.s.Name, tc.vrf, got, tc.file)
		}
		if vrf, ok := tc.s.VRFOfFile(tc.file); !ok || vrf != tc.vrf {
			t.Errorf("%s.VRFOfFile(%q) = %q, %v, want %q", tc.s.Name, tc.file, vrf, ok, tc.vrf)
		}
		if got := tc.s.Unit(tc.vrf); got != tc.unit {
			t.Errorf("%s.Unit(%q) = %q, want %q", tc.s.Name, tc.vrf, got, tc.unit)
		}
		if got := tc.s.RuntimeDir(tc.vrf); got != tc.dir {
			t.Errorf("%s.RuntimeDir(%q) = %q, want %q", tc.s.Name, tc.vrf, got, tc.dir)
		}
		if got := tc.s.IndexRecord(tc.vrf); got != tc.rec {
			t.Errorf("%s.IndexRecord(%q) = %q, want %q", tc.s.Name, tc.vrf, got, tc.rec)
		}
	}
}

// A file of another service, of another extension, of no VRF's name or of the default VRF's name
// is no config of the service.
func TestVRFOfFileRefuses(t *testing.T) {
	for _, tc := range []struct {
		s    Service
		name string
	}{
		{Kea4, "kea-dhcp6.json"},
		{Kea4, "kea-dhcp6-guest.json"},
		{Kea4, "kea-dhcp4-guest.conf"},
		{Kea4, "kea-dhcp4-.json"},
		{Kea4, "kea-dhcp4x.json"},
		{Kea4, "kea-dhcp4-default.json"},
		{Unbound, "unbound"},
		{Relay, "dnsmasq-relay-guest.conf.tmp"},
	} {
		if vrf, ok := tc.s.VRFOfFile(tc.name); ok {
			t.Errorf("%s.VRFOfFile(%q) = %q, true, want false", tc.s.Name, tc.name, vrf)
		}
	}
}

// escape escapes as systemd-escape does.
func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		"guest":   "guest",
		"dmz-a":   `dmz\x2da`,
		"v1.2":    "v1.2",
		".a":      `\x2ea`,
		"a/b":     "a-b",
		"a b":     `a\x20b`,
		"a:b_c.d": "a:b_c.d",
	} {
		if got := escape(in); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
}
