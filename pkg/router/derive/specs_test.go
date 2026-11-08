package derive

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// specTypes are the Go types Build decodes each router kind's spec into.
var specTypes = map[string]reflect.Type{
	"Router":         reflect.TypeFor[RouterSpec](),
	"FirewallZone":   reflect.TypeFor[ZoneSpec](),
	"FirewallPolicy": reflect.TypeFor[PolicySpec](),
	"AddressSet":     reflect.TypeFor[AddressSetSpec](),
	"NATRule":        reflect.TypeFor[NATSpec](),
	"BGP":            reflect.TypeFor[BGPSpec](),
	"BGPPeer":        reflect.TypeFor[PeerSpec](),
	"PrefixList":     reflect.TypeFor[PrefixListSpec](),
	"CommunityList":  reflect.TypeFor[CommunityListSpec](),
	"RoutingPolicy":  reflect.TypeFor[RoutingPolicySpec](),
	"NAT64":          reflect.TypeFor[NAT64Spec](),
	"Resolver":       reflect.TypeFor[ResolverSpec](),
	"VRF":            reflect.TypeFor[VRFSpec](),
	"PolicyRoute":    reflect.TypeFor[PolicyRouteSpec](),
	"NextHop":        reflect.TypeFor[NextHopSpec](),
	"DHCPSubnet":     reflect.TypeFor[DHCPSubnetSpec](),
	"DHCPRelay":      reflect.TypeFor[DHCPRelaySpec](),
}

// The specs' types decode every value of each enum of the kinds' schemas, and a field of an
// address or an address range is one whose schema holds it to the format ip or cidr, or to a
// pattern: the typed decode refuses nothing the schemas accept.
func TestSpecTypesTakeTheSchemas(t *testing.T) {
	for kind, typ := range specTypes {
		data, err := os.ReadFile(filepath.Join(routerSchemas, strings.ReplaceAll(API, "/", "_")+"_"+kind+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var s map[string]any
		if err := json.Unmarshal(data, &s); err != nil {
			t.Fatal(err)
		}
		props, _ := s["properties"].(map[string]any)
		matchesSchema(t, kind+".spec", props["spec"], typ)
	}
}

func matchesSchema(t *testing.T, path string, node any, typ reflect.Type) {
	t.Helper()
	s, _ := node.(map[string]any)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if branches, ok := s["anyOf"].([]any); ok {
		for _, b := range branches {
			if b.(map[string]any)["type"] != "null" {
				matchesSchema(t, path, b, typ)
			}
		}
		return
	}
	if values, ok := s["enum"].([]any); ok {
		for _, v := range values {
			data, _ := json.Marshal(v)
			if err := json.Unmarshal(data, reflect.New(typ).Interface()); err != nil {
				t.Errorf("%s: the schema's enum value %s does not decode into %s: %v", path, data, typ, err)
			}
		}
	}
	if typ == reflect.TypeFor[netip.Addr]() || typ == reflect.TypeFor[netip.Prefix]() {
		want := map[reflect.Type]string{reflect.TypeFor[netip.Addr](): "ip", reflect.TypeFor[netip.Prefix](): "cidr"}[typ]
		if _, pattern := s["pattern"]; s["format"] != want && !pattern {
			t.Errorf("%s: the schema holds the %s to neither the format %s nor a pattern", path, typ, want)
		}
		return
	}
	switch typ.Kind() {
	case reflect.Slice:
		matchesSchema(t, path+"[]", s["items"], typ.Elem())
	case reflect.Struct:
		props, _ := s["properties"].(map[string]any)
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" {
				continue
			}
			p, ok := props[name]
			if !ok {
				t.Errorf("%s: the schema has no field %s", path, name)
				continue
			}
			matchesSchema(t, path+"."+name, p, f.Type)
		}
	}
}

// A spec whose enum holds a value the schema lacks, whose community is none, or whose address is
// missing or holds a zone, does not decode: Build guards it with an internal error.
func TestDecodeGuards(t *testing.T) {
	for name, c := range map[string]struct {
		kind, spec, want string
	}{
		"a family":           {"AddressSet", `{"family": "ipx", "prefixes": ["192.0.2.0/24"]}`, `internal error: AddressSet a: "ipx" is none of ipv4, ipv6`},
		"a verdict":          {"FirewallPolicy", `{"from": "lan", "to": "wan", "default": "drop; flush ruleset", "rules": []}`, `internal error: FirewallPolicy a: "drop; flush ruleset" is none of accept, drop, reject`},
		"a community":        {"CommunityList", `{"communities": ["65000:1 permit"]}`, `internal error: CommunityList a: "65000:1 permit" is no community: its parts are numbers of 16 bits`},
		"a large community":  {"CommunityList", `{"communities": ["1:2:3:4"]}`, `internal error: CommunityList a: "1:2:3:4" is no community <AS>:<value> or large community <AS>:<value>:<value>`},
		"a missing address":  {"NextHop", `{"address": null, "interface": "eth0", "table": 100}`, `internal error: NextHop a: "invalid IP" is no address without a zone`},
		"an address's zone":  {"NextHop", `{"address": "fe80::1%eth0", "interface": "eth0", "table": 100}`, `internal error: NextHop a: "fe80::1%eth0" is no address without a zone`},
		"a missing range":    {"AddressSet", `{"family": "ipv4", "prefixes": [""]}`, `internal error: AddressSet a: "invalid Prefix" is no address range`},
		"a range of no bits": {"AddressSet", `{"family": "ipv4", "prefixes": ["192.0.2.0"]}`, `internal error: AddressSet a: netip.ParsePrefix("192.0.2.0"): no '/'`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Build(map[string]any{c.kind: []any{routerDoc(t, c.kind, "a", c.spec)}})
			if err == nil || !strings.HasPrefix(err.Error(), c.want) {
				t.Errorf("Build = %v, want the error %q", err, c.want)
			}
		})
	}
}

// A community decodes into the form FRR writes, and encodes as it decoded.
func TestCommunity(t *testing.T) {
	var got []Community
	if err := json.Unmarshal([]byte(`["65000:100", "4200000000:1:2"]`), &got); err != nil {
		t.Fatal(err)
	}
	want := []Community{{Value: "65000:100"}, {Value: "4200000000:1:2", Large: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("communities = %+v, want %+v", got, want)
	}
	if data, err := json.Marshal(got); err != nil || string(data) != `["65000:100","4200000000:1:2"]` {
		t.Errorf("encoded = %s, %v", data, err)
	}
}
