package derive

import (
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

const (
	// routerSchemas are the router kinds' schemas, as a router image generates them.
	routerSchemas = "../../../test/fixtures/kinds/router"
	// routerExamples are the examples of the router outputs, which the router image's build
	// renders.
	routerExamples = "../../../modules/node/examples/router"
)

// example is the router example name as an output's input holds it: the documents of Kinds by
// kind, each sorted by name, the router kinds' with their schema defaults, and their numbers
// exact.
func example(t *testing.T, name string) map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(routerExamples, name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	docs, err := config.Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	s, err := schema.LoadSchemas(routerSchemas)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortStableFunc(docs, func(a, b config.Document) int { return cmp.Compare(a.Name, b.Name) })
	byKind := map[string][]any{}
	for _, d := range docs {
		obj := d.Object
		if d.APIVersion == API {
			if obj, err = s.Default(d.Object); err != nil {
				t.Fatal(err)
			}
		}
		if slices.Contains(Kinds(), d.Kind) {
			byKind[d.Kind] = append(byKind[d.Kind], obj)
		}
	}
	data, err := json.Marshal(byKind)
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, data).(map[string]any)
}

// decode is data as JSON decodes it with its numbers exact.
func decode(t *testing.T, data []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// Kinds are the router kinds, those of the router image's schemas, and networkd's Network, as
// the router module gives the validator router the router kinds the image declares and Network.
func TestKinds(t *testing.T) {
	entries, err := os.ReadDir(routerSchemas)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Network"}
	for _, e := range entries {
		_, kind, _ := strings.Cut(strings.TrimSuffix(e.Name(), ".json"), "_v1alpha1_")
		want = append(want, kind)
	}
	if got := slices.Sorted(slices.Values(Kinds())); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("Kinds = %v, want %v", got, want)
	}
}

// Build reads the documents of each kind and derives what the Network and VRF documents say
// together: the links the Networks name, their static addresses, the VRF of each Network and
// each link, the VRFs' devices, the .network units in networkd's order, the NAT64 instances and
// the tables of policy routing.
func TestBuild(t *testing.T) {
	m, err := Build(example(t, "vrf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Routers) != 1 || len(m.VRFs) != 3 || len(m.BGP) != 3 || len(m.NAT64) != 1 || m.Router().RPFilter != "loose" {
		t.Errorf("documents: %d Routers, %d VRFs, %d BGPs, %d NAT64s, rpFilter %q", len(m.Routers), len(m.VRFs), len(m.BGP), len(m.NAT64), m.Router().RPFilter)
	}
	if want := []string{"eth0", "eth1", "eth2", "eth3", "eth4", "swp1", "swp2"}; !slices.Equal(m.Links, want) {
		t.Errorf("links = %v, want %v", m.Links, want)
	}
	if want := map[string]string{"20-internet": "internet", "30-tenant": "tenant", "40-guest-lan": "guest", "41-guest-wan": "guest"}; !reflect.DeepEqual(m.Membership.Networks, want) {
		t.Errorf("networks = %v, want %v", m.Membership.Networks, want)
	}
	for link, want := range map[string]string{"eth0": DefaultVRF, "eth1": "internet", "swp2": "tenant", "eth4": "guest", "lo": DefaultVRF} {
		if vrf, err := m.Membership.LinkVRF(link); err != nil || vrf != want {
			t.Errorf("LinkVRF(%s) = %q, %v, want %q", link, vrf, err, want)
		}
	}
	if got, want := m.VRFOfNetwork("10-mgmt"), DefaultVRF; got != want {
		t.Errorf("VRFOfNetwork(10-mgmt) = %q, want %q", got, want)
	}
	if want := []string{"guest", "internet", "tenant"}; !slices.Equal(m.Devices(), want) {
		t.Errorf("devices = %v, want %v", m.Devices(), want)
	}
	addresses := map[string][]string{}
	for _, a := range m.Addresses {
		addresses[a.Network] = append(addresses[a.Network], a.Prefix.String())
	}
	if want := []string{"192.0.2.1/24", "2001:db8:3::1/64"}; !slices.Equal(addresses["40-guest-lan"], want) {
		t.Errorf("addresses of 40-guest-lan = %v, want %v", addresses["40-guest-lan"], want)
	}
	units, err := m.Units()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, u := range units {
		names = append(names, u.Name)
	}
	if want := []string{"00-vrf-guest", "00-vrf-internet", "00-vrf-tenant", "10-mgmt", "20-internet", "30-tenant", "40-guest-lan", "41-guest-wan"}; !slices.Equal(names, want) {
		t.Errorf("units = %v, want %v", names, want)
	}
	nat64, err := m.NAT64Instances()
	if err != nil {
		t.Fatal(err)
	}
	if len(nat64) != 1 || nat64[0].Name != "guest" || nat64[0].VRF != "guest" || nat64[0].Table != 300 || len(nat64[0].Pool4) != 0 {
		t.Errorf("NAT64 instances = %+v", nat64)
	}
	tables, err := m.PolicyTables()
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]uint32{"guest": 300, "internet": 100, "tenant": 200}; !reflect.DeepEqual(tables.VRFs, want) || len(tables.Hops) != 0 {
		t.Errorf("policy tables = %+v", tables)
	}
}

// Without documents, the model holds none, puts every link in the default VRF and takes the
// Router kind's defaults.
func TestBuildWithoutDocuments(t *testing.T) {
	m, err := Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if vrf, err := m.Membership.LinkVRF("eth0"); err != nil || vrf != DefaultVRF || len(m.Links) != 0 || len(m.Devices()) != 0 || !reflect.DeepEqual(m.Router(), defaultRouter()) {
		t.Errorf("model of no documents: %+v, LinkVRF(eth0) = %q, %v", m, vrf, err)
	}
}

// A node without a Router document renders the kind's defaults, as the schema gives them.
func TestDefaultRouterMatchesTheSchema(t *testing.T) {
	s, err := schema.LoadSchemas(routerSchemas)
	if err != nil {
		t.Fatal(err)
	}
	filled, err := s.Default(map[string]any{"apiVersion": API, "kind": "Router", "metadata": map[string]any{"name": "router"}, "spec": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(filled["spec"])
	if err != nil {
		t.Fatal(err)
	}
	var fromSchema RouterSpec
	if err := json.Unmarshal(data, &fromSchema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromSchema, defaultRouter()) {
		t.Errorf("the schema's defaults %+v, defaultRouter %+v", fromSchema, defaultRouter())
	}
}

// MustGet gives the target of a reference the input holds, and names one it lacks as an internal
// error: chalkd refuses a config whose references do not resolve before it renders.
func TestMustGet(t *testing.T) {
	tables := map[string]uint32{"tenant": 100}
	if got, err := MustGet(tables, "VRF", "tenant"); err != nil || got != 100 {
		t.Errorf("MustGet(tenant) = %d, %v, want 100", got, err)
	}
	want := `internal error: the input holds no VRF "lab", which a reference names; chalkd checks references before it renders`
	if _, err := MustGet(tables, "VRF", "lab"); err == nil || err.Error() != want {
		t.Errorf("MustGet(lab) = %v, want %s", err, want)
	}
}
