package translate

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/internal/strs"
	"github.com/trevex/chalkos/pkg/kubernetes"
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
// from its own documents, so nodes that differ would each run another one.
var clusterKinds = []string{"Cluster", "Flannel"}

// Endpoint checks that the nodes describe one cluster and returns the URL of its Kubernetes API
// server, the endpoint of their Cluster documents; documents gives a node's documents. Every
// node that holds a Cluster document holds the same Cluster spec, and the same Flannel spec or,
// on every one of them, none, whatever the documents are called; a node without a Cluster
// document runs no Kubernetes and is skipped. The specs compare as written, so a field left to
// its default differs from one set to it, and only the order of Flannel's vxlanSourceSubnets
// makes no difference. The endpoint is empty when no node holds a Cluster document, as for no
// nodes.
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
// the first field whose values differ, walking the fields in name order, and the nodes of each of
// its values. Flannel's vxlanSourceSubnets compare as a set.
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
		path, ok := firstDifference(specs, nil)
		if !ok {
			return nil
		}
		values := make([]any, len(specs))
		for i, s := range specs {
			values[i] = at(s, path)
		}
		for i, g := range groupNodes(nodes, values) {
			part := show(g.value) + " on " + strs.And(g.nodes)
			if i == 0 {
				part = showPath(path) + " is " + part
			}
			parts = append(parts, part)
		}
	}
	return fmt.Errorf("the nodes hold different %s specs: %s; every node of the cluster holds the same Cluster and Flannel specs", kind, strings.Join(parts, ", "))
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

// unset stands for a field a spec lacks.
type unset struct{}

// firstDifference returns the path, below path, of the first field whose values differ: a
// field's name or a list item's index for each step. It walks the fields of objects in name order
// and the items of lists of one length in their order, and returns false when the values are
// equal.
func firstDifference(values []any, path []any) ([]any, bool) {
	if !slices.ContainsFunc(values, func(v any) bool { return !reflect.DeepEqual(v, values[0]) }) {
		return nil, false
	}
	if objects, ok := every[map[string]any](values); ok {
		var keys []string
		for _, o := range objects {
			keys = append(keys, slices.Collect(maps.Keys(o))...)
		}
		slices.Sort(keys)
		for _, k := range slices.Compact(keys) {
			field := make([]any, len(objects))
			for i, o := range objects {
				field[i] = at(o, []any{k})
			}
			if p, ok := firstDifference(field, append(slices.Clip(path), k)); ok {
				return p, true
			}
		}
	}
	lists, ok := every[[]any](values)
	if ok && !slices.ContainsFunc(lists, func(l []any) bool { return len(l) != len(lists[0]) }) {
		for i := range lists[0] {
			item := make([]any, len(lists))
			for j, l := range lists {
				item[j] = l[i]
			}
			if p, ok := firstDifference(item, append(slices.Clip(path), i)); ok {
				return p, true
			}
		}
	}
	return path, true
}

// every returns values as Ts when each is one.
func every[T any](values []any) ([]T, bool) {
	out := make([]T, len(values))
	for i, v := range values {
		t, ok := v.(T)
		if !ok {
			return nil, false
		}
		out[i] = t
	}
	return out, true
}

// at returns the value at path below v, unset when v lacks it.
func at(v any, path []any) any {
	for _, step := range path {
		switch s := step.(type) {
		case string:
			o, ok := v.(map[string]any)
			if !ok {
				return unset{}
			}
			if v, ok = o[s]; !ok {
				return unset{}
			}
		case int:
			l, ok := v.([]any)
			if !ok || s >= len(l) {
				return unset{}
			}
			v = l[s]
		}
	}
	return v
}

// showPath writes a path below a document's spec as spec.vip.addresses[0].
func showPath(path []any) string {
	var b strings.Builder
	b.WriteString("spec")
	for _, step := range path {
		if i, ok := step.(int); ok {
			fmt.Fprintf(&b, "[%d]", i)
		} else {
			fmt.Fprintf(&b, ".%s", step)
		}
	}
	return b.String()
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

// show writes a value as JSON, and a field a spec lacks as unset.
func show(v any) string {
	if _, ok := v.(unset); ok {
		return "unset"
	}
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}
