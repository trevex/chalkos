package chalkctl

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/trevex/chalkos/pkg/sdk"
)

// fixedAcross refuses the config of the node name, as the inventory gives it, when it disagrees
// with one of the nodes others on a field every node of a cluster holds alike, as disagreement
// compares them, naming the first such node. Its warnings are those of disagreement for each node
// compared up to the refusal. NodeNames is empty without an inventory.
func (c *cluster) fixedAcross(ct sdk.Contract, name string, others []string) ([]string, error) {
	var warnings []string
	for _, other := range others {
		w, err := c.disagreement(ct, name, other)
		if w != "" {
			warnings = append(warnings, w)
		}
		if err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

// disagreement refuses the configs of the nodes name and other, as the inventory gives them, when
// they hold a field that either node's image marks as held alike by every node of a cluster with
// different values, naming every such field once. The node name's config takes the marks and
// defaults of ct, the contract it is checked against; the other node's those of its image in the
// inventory. An inventory may name a node whose image is not at hand: when the other node's
// image's contract cannot be read, both configs take the marks and defaults of ct, and the warning
// names that node, since its image may mark a field that ct does not. A node compared with itself
// agrees, and so does an other node whose config cannot be read: each command that reads its
// config refuses it.
func (c *cluster) disagreement(ct sdk.Contract, name, other string) (warning string, err error) {
	if other == name {
		return "", nil
	}
	docs, err := c.Documents(name)
	if err != nil {
		return "", err
	}
	theirs, err := c.Documents(other)
	if err != nil {
		return "", nil
	}
	oct, err := c.imageContractOf(other)
	if err != nil {
		warning = fmt.Sprintf("the image of node %s cannot be read, so chalkctl compared node %s with it only on the fields %s's image marks: %s", other, name, name, err)
		oct = ct
	}
	changes, err := sdk.FixedChangesBetween(oct, ct, theirs, docs)
	if err != nil {
		return warning, fmt.Errorf("the nodes %s and %s: %w", name, other, err)
	}
	if len(changes) == 0 {
		return warning, nil
	}
	fields := make([]string, len(changes))
	for i, ch := range changes {
		fields[i] = fmt.Sprintf("%s %s: %s is %s on %s and %s on %s", ch.Kind, ch.Name, ch.Path, valueText(ch.New), name, valueText(ch.Old), other)
	}
	return warning, fmt.Errorf("the nodes %s and %s disagree on fields every node of the cluster holds alike: %s; give every node of the inventory the same value", name, other, strings.Join(fields, "; "))
}

// valueText is a config value as JSON, or unset for none.
func valueText(v any) string {
	if v == nil {
		return "unset"
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}
