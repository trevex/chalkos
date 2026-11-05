package chalkctl

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/validators"
)

// reservedPlugin is a layer that links the validator reserved, which an image with Network
// documents runs: it refuses a Network address of 10.0.0.99, and records what it read.
type reservedPlugin struct{ read *[]byte }

func (reservedPlugin) Name() string { return "reserved" }

func (p reservedPlugin) Validators(s *schema.Schemas) []validators.Linked {
	if !slices.ContainsFunc(s.Kinds(), func(k schema.Kind) bool { return k.Kind == "Network" }) {
		return nil
	}
	return []validators.Linked{{Name: "reserved", Inputs: []string{"Network"}, Validate: func(docs []byte) ([]refusals.Refusal, error) {
		*p.read = docs
		var networks []struct {
			Metadata struct{ Name string }
			Spec     struct {
				Network struct{ Address []string }
			}
		}
		if err := json.Unmarshal(docs, &networks); err != nil {
			return nil, err
		}
		var rs []refusals.Refusal
		for _, n := range networks {
			if i := slices.Index(n.Spec.Network.Address, "10.0.0.99/24"); i >= 0 {
				rs = append(rs, refusals.Refusal{Kind: "Network", Name: n.Metadata.Name, Path: "spec.network.address[" + strconv.Itoa(i) + "]", Message: "10.0.0.99 is the lab's reserved address"})
			}
		}
		return rs, nil
	}}}
}

// withPlugin compiles p into chalkctl for the test, beside its layers.
func withPlugin(t *testing.T, p layer.Plugin) {
	saved := plugins
	plugins = append(slices.Clone(plugins), p)
	t.Cleanup(func() { plugins = saved })
}

// chalkctl validate runs the validators its layers link on each node's config, in-process, on the
// JSON array the image's validator programs read, and refuses a config one of them refuses as
// the node would, naming the document and its field.
func TestValidateRunsLinkedValidators(t *testing.T) {
	var read []byte
	withPlugin(t, reservedPlugin{&read})
	ta := newTestApp(t)
	ta.withoutSecrets(t)
	path := writeInventory(t, ta.dir)
	if err := ta.run(context.Background(), []string{"validate", "--inventory", path}); err != nil {
		t.Fatal(err)
	}
	var docs []map[string]any
	if err := json.Unmarshal(read, &docs); err != nil || len(docs) != 1 || docs[0]["kind"] != "Network" {
		t.Errorf("the validator read %s: %v", read, err)
	}
	reserved := strings.Replace(n1Network, "10.0.0.11/24", "10.0.0.99/24", 1)
	path = writeInventory(t, ta.dir, n1SystemDisk, reserved)
	err := ta.run(context.Background(), []string{"validate", "--inventory", path})
	if want := "node n1: the config is refused: Network/10-uplink: spec.network.address[0]: 10.0.0.99 is the lab's reserved address"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("validate = %v, want %q", err, want)
	}
}
