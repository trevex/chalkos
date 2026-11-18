package translate

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/internal/strs"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/clusterregistry"
)

// NodeType is the spec.type of the Kubernetes Node document among docs: controlplane or worker;
// empty without one.
func NodeType(docs []config.Document) string {
	kind := ""
	for _, d := range docs {
		if d.APIVersion == API && d.Kind == "Node" {
			node, _ := d.Object["spec"].(map[string]any)
			kind, _ = node["type"].(string)
		}
	}
	return kind
}

// NodeDocuments are the documents of a node's files that the node gets: all of them on a control
// plane, and all but the Addon documents on any other node. An Addon's objects go to the
// cluster's API, which only control planes apply.
func NodeDocuments(docs []config.Document) []config.Document {
	if NodeType(docs) == kubernetes.KindControlPlane {
		return docs
	}
	var out []config.Document
	for _, d := range docs {
		if d.APIVersion != API || d.Kind != "Addon" {
			out = append(out, d)
		}
	}
	return out
}

// clusterKinds are the kinds every node of the cluster holds alike: each node builds the cluster
// from its own documents, so nodes that differ would each run another one. Every node pulls from
// the endpoints of the ClusterRegistry, and every control plane serves one of them.
var clusterKinds = []string{"Cluster", "Flannel", clusterregistry.Kind}

// Endpoint returns the API server URL that the nodes' Cluster documents share, or "" when no node
// has one; documents gives a node's documents. Nodes without a Cluster document are skipped. Nodes
// whose Cluster, Flannel or ClusterRegistry specs differ as written, apart from the order of
// vxlanSourceSubnets, are refused, whatever the documents are called; a field left to its default
// differs from one set to it.
func Endpoint(nodes []string, documents func(name string) ([]config.Document, error)) (string, error) {
	var members []string
	held := map[string]map[string]map[string]any{}
	for _, n := range nodes {
		docs, err := documents(n)
		if err != nil {
			return "", err
		}
		s, err := clusterSpecs(docs)
		if err != nil {
			return "", fmt.Errorf("node %s: %w", n, err)
		}
		if s["Cluster"] != nil {
			members = append(members, n)
			held[n] = s
		}
	}
	if len(members) == 0 {
		return "", nil
	}
	for _, kind := range clusterKinds {
		if err := alike(kind, members, func(n string) map[string]any { return held[n][kind] }); err != nil {
			return "", err
		}
	}
	e, _ := held[members[0]]["Cluster"]["endpoint"].(string)
	return e, nil
}

// clusterSpecs returns the spec of each cluster kind among docs, nil for one they lack.
func clusterSpecs(docs []config.Document) (map[string]map[string]any, error) {
	found := map[string][]map[string]any{}
	for _, d := range docs {
		if d.APIVersion == API && slices.Contains(clusterKinds, d.Kind) {
			spec, _ := d.Object["spec"].(map[string]any)
			if spec == nil {
				spec = map[string]any{}
			}
			found[d.Kind] = append(found[d.Kind], spec)
		}
	}
	out := map[string]map[string]any{}
	for _, kind := range clusterKinds {
		switch n := len(found[kind]); {
		case n == 1:
			out[kind] = found[kind][0]
		case n > 1:
			return nil, fmt.Errorf("its files hold %d %s documents, not one", n, kind)
		}
	}
	return out, nil
}

// alike refuses nodes whose specs of the kind, nil for none, differ. When some nodes hold none,
// the message names the nodes of each spec in the order of their first node; otherwise it names
// the first field whose values differ, in config.ComparePaths order, and the nodes of each of its
// values. Flannel's vxlanSourceSubnets compare as a set.
func alike(kind string, nodes []string, spec func(string) map[string]any) error {
	specs := make([]any, len(nodes))
	missing := false
	for i, n := range nodes {
		if s := spec(n); s != nil {
			specs[i] = normalized(kind, s)
		} else {
			missing = true
		}
	}
	var parts []string
	if missing {
		groups := groupNodes(nodes, specs)
		if len(groups) == 1 {
			return nil
		}
		seen := false
		for _, g := range groups {
			which := "another"
			switch {
			case g.value == nil:
				which = "none"
			case !seen:
				which, seen = "one", true
			}
			parts = append(parts, which+" on "+strs.And(g.nodes))
		}
	} else {
		field, values := firstDifference(specs)
		if field == "" {
			return nil
		}
		for i, g := range groupNodes(nodes, values) {
			part := g.value.(string) + " on " + strs.And(g.nodes)
			if i == 0 {
				part = field + " is " + part
			}
			parts = append(parts, part)
		}
	}
	return fmt.Errorf("the nodes hold different %s specs: %s; every node of the cluster holds the same Cluster, Flannel and ClusterRegistry specs", kind, strings.Join(parts, ", "))
}

// normalized is a spec of the kind as alike compares it: Flannel's vxlanSourceSubnets sorted, as
// their order makes no difference to the ranges flannel takes VXLAN from.
func normalized(kind string, spec map[string]any) map[string]any {
	subnets, ok := spec["vxlanSourceSubnets"].([]any)
	if kind != "Flannel" || !ok {
		return spec
	}
	sorted := slices.Clone(subnets)
	slices.SortStableFunc(sorted, func(a, b any) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
	out := maps.Clone(spec)
	out["vxlanSourceSubnets"] = sorted
	return out
}

// firstDifference returns the first field whose leaves differ among the specs, as
// spec.vip.addresses[0], with each spec's value as JSON or "unset"; the field is "" when the specs
// are equal. Inside lists of different lengths it names the list, with each spec's whole list.
func firstDifference(specs []any) (string, []any) {
	if !slices.ContainsFunc(specs, func(s any) bool { return !reflect.DeepEqual(s, specs[0]) }) {
		return "", nil
	}
	leaves := make([]map[string]config.Leaf, len(specs))
	union := map[string]config.Leaf{}
	for i, s := range specs {
		leaves[i] = config.Leaves(s)
		maps.Copy(union, leaves[i])
	}
	paths := slices.SortedFunc(maps.Values(union), func(a, b config.Leaf) int { return config.ComparePaths(a.Path, b.Path) })
	for i, p := range paths {
		key := strings.Join(p.Path, "\x00")
		values := make([]any, len(specs))
		holder, empty := 0, true
		for j := range specs {
			values[j] = "unset"
			if l, ok := leaves[j][key]; ok {
				values[j], holder = l.Value, j
				empty = empty && (l.Value == "{}" || l.Value == "[]")
			}
		}
		// An empty object or list in one spec is no field to name when another holds fields below
		// it.
		if empty && i+1 < len(paths) && len(paths[i+1].Path) > len(p.Path) && slices.Equal(paths[i+1].Path[:len(p.Path)], p.Path) {
			continue
		}
		if !slices.ContainsFunc(values, func(v any) bool { return v != values[0] }) {
			continue
		}
		for n := 1; n < len(p.Path); n++ {
			if list, values := listsOf(specs, p.Path[:n]); list {
				return "spec." + config.ShowPath(specs[holder], p.Path[:n]), values
			}
		}
		return "spec." + config.ShowPath(specs[holder], p.Path), values
	}
	// Specs that flatten alike but differ, such as {"0": 1} and [1].
	whole := make([]any, len(specs))
	for i, s := range specs {
		whole[i] = show(s)
	}
	return "spec", whole
}

// listsOf reports whether the specs hold lists of different lengths at path, and if so each
// spec's value there as JSON or "unset".
func listsOf(specs []any, path []string) (bool, []any) {
	values := make([]any, len(specs))
	lengths := map[int]bool{}
	for i, s := range specs {
		values[i] = "unset"
		v, ok := s, true
		for _, step := range path {
			switch c := v.(type) {
			case map[string]any:
				v, ok = c[step]
			case []any:
				n, err := strconv.Atoi(step)
				ok = err == nil && n < len(c)
				if ok {
					v = c[n]
				}
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}
		list, isList := v.([]any)
		if !isList {
			return false, nil
		}
		lengths[len(list)] = true
		values[i] = show(v)
	}
	return len(lengths) > 1, values
}

// show writes a value as JSON without HTML escaping.
func show(v any) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// valueGroup is the nodes of one value, in their order.
type valueGroup struct {
	value any
	nodes []string
}

// groupNodes groups the nodes by their values, the group of the first node first.
func groupNodes(nodes []string, values []any) []*valueGroup {
	var groups []*valueGroup
	for i, n := range nodes {
		j := slices.IndexFunc(groups, func(g *valueGroup) bool { return reflect.DeepEqual(g.value, values[i]) })
		if j < 0 {
			groups = append(groups, &valueGroup{value: values[i]})
			j = len(groups) - 1
		}
		groups[j].nodes = append(groups[j].nodes, n)
	}
	return groups
}
