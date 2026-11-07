package translate

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

// parseDocs parses the YAML documents of a node's files.
func parseDocs(t *testing.T, yaml string) []config.Document {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// endpointDoc is a Cluster document called name that names the endpoint alone.
func endpointDoc(name, endpoint string) string {
	return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: " + name + "}\nspec: {endpoint: \"" + endpoint + "\"}\n"
}

// plainHost is a Host document of the hostname n1.lab.
const plainHost = "---\napiVersion: chalkos.dev/v1alpha1\nkind: Host\nmetadata: {name: default}\nspec: {hostname: n1.lab}\n"

// addonDoc is an Addon document without objects.
const addonDoc = "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Addon\nmetadata: {name: monitoring}\nspec: {objects: []}\n"

func kindsOf(docs []config.Document) []string {
	var out []string
	for _, d := range docs {
		out = append(out, d.Kind+"/"+d.Name)
	}
	return out
}

// TestNodeType reads the type of the Kubernetes Node document among a node's documents.
func TestNodeType(t *testing.T) {
	for name, tc := range map[string]struct{ docs, want string }{
		"a control plane": {plainHost + nodeDoc("default", "{type: controlplane}"), "controlplane"},
		"a worker":        {nodeDoc("default", "{type: worker}") + plainHost, "worker"},
		"a Host alone":    {plainHost, ""},
		"no documents":    {"", ""},
	} {
		if got := NodeType(parseDocs(t, tc.docs)); got != tc.want {
			t.Errorf("%s: NodeType = %q, want %q", name, got, tc.want)
		}
	}
}

// TestNodeDocuments gives the Addon documents to control planes alone: not to a worker, nor to a
// node without a Kubernetes Node, whatever the Node is called.
func TestNodeDocuments(t *testing.T) {
	for name, tc := range map[string]struct {
		docs string
		want []string
	}{
		"a control plane": {endpointDoc("default", "https://10.0.0.11:6443") + addonDoc + nodeDoc("cp2", "{type: controlplane}"),
			[]string{"Cluster/default", "Addon/monitoring", "Node/cp2"}},
		"a worker": {endpointDoc("default", "https://10.0.0.11:6443") + addonDoc + nodeDoc("default", "{type: worker}") + plainHost,
			[]string{"Cluster/default", "Node/default", "Host/default"}},
		"no Node": {addonDoc + plainHost, []string{"Host/default"}},
	} {
		if got := kindsOf(NodeDocuments(parseDocs(t, tc.docs))); !slices.Equal(got, tc.want) {
			t.Errorf("%s: NodeDocuments = %q, want %q", name, got, tc.want)
		}
	}
}

// TestEndpoint takes the endpoint of the Cluster documents, which every node holding one holds
// with the same spec, as it holds the same Flannel spec or none, whatever the documents' names;
// nodes without a Cluster document are skipped. Nodes that differ, and a node holding two, are
// refused, and the message names the nodes of each spec.
func TestEndpoint(t *testing.T) {
	const want = "https://10.0.0.11:6443"
	for name, tc := range map[string]struct{ cp1, w1, err string }{
		"the same documents of other names": {
			endpointDoc("default", want) + flannelDoc("default", "{mtu: 1400}"),
			endpointDoc("cluster", want) + flannelDoc("flannel", "{mtu: 1400}"), ""},
		"a node without Kubernetes": {endpointDoc("default", want) + flannelDoc("default", "{mtu: 1400}"), plainHost, ""},
		"another endpoint": {endpointDoc("default", want), endpointDoc("default", "https://10.0.0.99:6443"),
			`the nodes hold different Cluster specs: spec.endpoint is "https://10.0.0.11:6443" on cp1, "https://10.0.0.99:6443" on w1; every node of the cluster holds the same Cluster and Flannel specs`},
		"another MTU": {
			endpointDoc("default", want) + flannelDoc("default", "{mtu: 1400}"),
			endpointDoc("default", want) + flannelDoc("default", "{mtu: 1450}"),
			"the nodes hold different Flannel specs: spec.mtu is 1400 on cp1, 1450 on w1; every node of the cluster holds the same Cluster and Flannel specs"},
		"a Flannel document on one node": {
			endpointDoc("default", want),
			endpointDoc("default", want) + flannelDoc("default", "null"),
			"the nodes hold different Flannel specs: none on cp1, one on w1; every node of the cluster holds the same Cluster and Flannel specs"},
		"two Cluster documents": {endpointDoc("default", want), endpointDoc("a", want) + endpointDoc("b", want),
			"node w1: its files hold 2 Cluster documents, not one"},
	} {
		t.Run(name, func(t *testing.T) {
			docs := map[string][]config.Document{"cp1": parseDocs(t, tc.cp1), "w1": parseDocs(t, tc.w1)}
			endpoint, err := Endpoint([]string{"cp1", "w1"}, func(n string) ([]config.Document, error) { return docs[n], nil })
			switch {
			case tc.err == "" && (err != nil || endpoint != want):
				t.Errorf("endpoint = %q, %v", endpoint, err)
			case tc.err != "" && (err == nil || err.Error() != tc.err):
				t.Errorf("err = %v, want %q", err, tc.err)
			}
		})
	}
	// Without a Cluster document, and without nodes, no node names an endpoint.
	for name, nodes := range map[string][]string{"no Cluster document": {"cp1", "w1"}, "no nodes": nil} {
		endpoint, err := Endpoint(nodes, func(string) ([]config.Document, error) { return parseDocs(t, plainHost), nil })
		if err != nil || endpoint != "" {
			t.Errorf("%s: endpoint = %q, %v", name, endpoint, err)
		}
	}
	// A node whose documents cannot be read stops the check with its error.
	if _, err := Endpoint([]string{"cp1"}, func(string) ([]config.Document, error) { return nil, fmt.Errorf("node cp1: broken") }); err == nil || err.Error() != "node cp1: broken" {
		t.Errorf("err = %v, want node cp1: broken", err)
	}
}

// TestEndpointNamesTheNodesOfEachSpec lists the nodes of each spec in the nodes' order, the spec
// of the first node first.
func TestEndpointNamesTheNodesOfEachSpec(t *testing.T) {
	hosts := map[string]string{"cp1": "10", "cp2": "10", "w1": "20", "w2": "10", "w3": "30"}
	docs := map[string][]config.Document{}
	for name, host := range hosts {
		docs[name] = parseDocs(t, endpointDoc("default", "https://10.0.0."+host+":6443"))
	}
	want := `the nodes hold different Cluster specs: spec.endpoint is "https://10.0.0.10:6443" on cp1, cp2 and w2, "https://10.0.0.20:6443" on w1, "https://10.0.0.30:6443" on w3; every node of the cluster holds the same Cluster and Flannel specs`
	_, err := Endpoint(slices.Sorted(maps.Keys(hosts)), func(n string) ([]config.Document, error) { return docs[n], nil })
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

// TestAlikeNamesTheFirstDifference names the first field that differs in a walk of the fields in
// name order, below objects and into lists of one length, with each value and its nodes; a field
// a spec lacks is unset. Flannel's vxlanSourceSubnets compare as a set.
func TestAlikeNamesTheFirstDifference(t *testing.T) {
	parse := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	nodes := []string{"cp1", "cp2", "w1"}
	for name, tc := range map[string]struct {
		kind  string
		specs [3]string
		want  string
	}{
		"equal": {"Cluster", [3]string{`{"domain": "a"}`, `{"domain": "a"}`, `{"domain": "a"}`}, ""},
		"a field": {"Cluster", [3]string{`{"domain": "a"}`, `{"domain": "a"}`, `{"domain": "b"}`},
			`spec.domain is "a" on cp1 and cp2, "b" on w1`},
		"the first field by name": {"Cluster", [3]string{`{"domain": "a", "endpoint": "x"}`, `{"domain": "a", "endpoint": "x"}`, `{"domain": "b", "endpoint": "y"}`},
			`spec.domain is "a" on cp1 and cp2, "b" on w1`},
		"a nested field": {"Cluster", [3]string{`{"vip": {"addresses": ["10.0.0.10"], "mode": "l2"}}`, `{"vip": {"addresses": ["10.0.0.10"], "mode": "l2"}}`, `{"vip": {"addresses": ["10.0.0.20"], "mode": "l2"}}`},
			`spec.vip.addresses[0] is "10.0.0.10" on cp1 and cp2, "10.0.0.20" on w1`},
		"lists of other lengths": {"Cluster", [3]string{`{"ipFamilies": ["ipv4"]}`, `{"ipFamilies": ["ipv4", "ipv6"]}`, `{"ipFamilies": ["ipv4"]}`},
			`spec.ipFamilies is ["ipv4"] on cp1 and w1, ["ipv4","ipv6"] on cp2`},
		"a field one spec lacks": {"Cluster", [3]string{`{"domain": "cluster.local"}`, `{}`, `{}`},
			`spec.domain is "cluster.local" on cp1, unset on cp2 and w1`},
		"VXLAN sources in another order": {"Flannel", [3]string{`{"vxlanSourceSubnets": ["10.0.0.0/24", "fd00::/64"]}`, `{"vxlanSourceSubnets": ["fd00::/64", "10.0.0.0/24"]}`, `{"vxlanSourceSubnets": ["10.0.0.0/24", "fd00::/64"]}`}, ""},
		"other VXLAN sources": {"Flannel", [3]string{`{"vxlanSourceSubnets": ["10.0.0.0/24", "fd00::/64"]}`, `{"vxlanSourceSubnets": ["fd00::/64", "10.0.0.0/24"]}`, `{"vxlanSourceSubnets": ["10.0.1.0/24", "fd00::/64"]}`},
			`spec.vxlanSourceSubnets[0] is "10.0.0.0/24" on cp1 and cp2, "10.0.1.0/24" on w1`},
	} {
		t.Run(name, func(t *testing.T) {
			specs := map[string]map[string]any{}
			for i, n := range nodes {
				specs[n] = parse(tc.specs[i])
			}
			err := alike(tc.kind, nodes, func(n string) map[string]any { return specs[n] })
			want := ""
			if tc.want != "" {
				want = "the nodes hold different " + tc.kind + " specs: " + tc.want + "; every node of the cluster holds the same Cluster and Flannel specs"
			}
			if got := fmt.Sprint(err); (err == nil) != (want == "") || err != nil && got != want {
				t.Errorf("err = %v, want %q", err, want)
			}
		})
	}
}
