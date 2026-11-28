package chalkctl

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/trevex/chalkos/pkg/sdk"
)

// fixedAcross refuses the config of the node name, as the inventory gives it, when it holds a
// field that every node of a cluster holds alike with another value than one of the nodes others
// holds, naming the first such node. The fields are those the contract ct marks: that of the
// node's own image, which the node checks its config against, so nodes of different images are
// compared by the marks of the image of the node checked. A partial cluster has no documents, so
// nothing is compared without an inventory.
func (c *cluster) fixedAcross(ct sdk.Contract, name string, others []string) error {
	for _, other := range others {
		if err := c.disagreement(ct, name, other); err != nil {
			return err
		}
	}
	return nil
}

// disagreement refuses the configs of the nodes name and other, as the inventory gives them, when
// they hold a field that the contract ct marks as held alike by every node of a cluster with
// different values, naming every such field. A node compared with itself agrees, and so does an
// other node whose config cannot be read: each command that reads its config refuses it.
func (c *cluster) disagreement(ct sdk.Contract, name, other string) error {
	if other == name {
		return nil
	}
	docs, err := c.Documents(name)
	if err != nil {
		return err
	}
	theirs, err := c.Documents(other)
	if err != nil {
		return nil
	}
	changes, err := sdk.FixedChanges(ct, theirs, docs)
	if err != nil {
		return fmt.Errorf("node %s: %w", name, err)
	}
	if len(changes) == 0 {
		return nil
	}
	fields := make([]string, len(changes))
	for i, ch := range changes {
		fields[i] = fmt.Sprintf("%s %s: %s is %s on %s and %s on %s", ch.Kind, ch.Name, ch.Path, valueText(ch.New), name, valueText(ch.Old), other)
	}
	return fmt.Errorf("the nodes %s and %s disagree on fields every node of the cluster holds alike: %s", name, other, strings.Join(fields, "; "))
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
