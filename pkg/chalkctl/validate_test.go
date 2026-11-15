package chalkctl

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	routerlayer "github.com/trevex/chalkos/pkg/chalkctl/layers/router"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/schema/schematest"
)

// reservedPlugin is a layer that links the validator reserved, which an image with Network
// documents runs: it refuses a Network address of 10.0.0.99, and records what it read.
type reservedPlugin struct{ read *[]byte }

func (reservedPlugin) Name() string { return "reserved" }

func (p reservedPlugin) Validators(s *schema.Schemas) []outputs.LinkedValidator {
	if !slices.ContainsFunc(s.Kinds(), func(k schema.Kind) bool { return k.Kind == "Network" }) {
		return nil
	}
	return []outputs.LinkedValidator{{Name: "reserved", Inputs: []string{"Network"}, Validate: func(stdin []byte) ([]refusals.Refusal, error) {
		*p.read = stdin
		var in struct {
			Documents struct {
				Network []struct {
					Metadata struct{ Name string }
					Spec     struct {
						Network struct{ Address []string }
					}
				}
			}
		}
		if err := json.Unmarshal(stdin, &in); err != nil {
			return nil, err
		}
		var rs []refusals.Refusal
		for _, n := range in.Documents.Network {
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

// chalkctl validate runs the validators its layers link on each node's config, as the node would.
func TestValidateRunsLinkedValidators(t *testing.T) {
	var read []byte
	withPlugin(t, reservedPlugin{&read})
	ta := newTestApp(t)
	ta.withoutSecrets(t)
	path := writeInventory(t, ta.dir)
	if err := ta.run(context.Background(), []string{"validate", "--inventory", path}); err != nil {
		t.Fatal(err)
	}
	var in struct {
		Node      struct{ Name string }
		Documents map[string][]map[string]any
	}
	if err := json.Unmarshal(read, &in); err != nil || in.Node.Name != "n1" || len(in.Documents) != 1 || len(in.Documents["Network"]) != 1 {
		t.Errorf("the validator read %s: %v", read, err)
	}
	reserved := strings.Replace(n1Network, "10.0.0.11/24", "10.0.0.99/24", 1)
	path = writeInventory(t, ta.dir, n1SystemDisk, reserved)
	err := ta.run(context.Background(), []string{"validate", "--inventory", path})
	if want := "node n1: the config is refused: Network/10-uplink: spec.network.address[0]: 10.0.0.99 is the lab's reserved address"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("validate = %v, want %q", err, want)
	}
}

// recordingRouter is chalkctl's router layer whose linked validators record the documents they
// read before they run.
type recordingRouter struct {
	*routerlayer.Plugin
	read *[][]byte
}

func (p recordingRouter) Validators(s *schema.Schemas) []outputs.LinkedValidator {
	linked := p.Plugin.Validators(s)
	for i, l := range linked {
		linked[i].Validate = func(stdin []byte) ([]refusals.Refusal, error) {
			*p.read = append(*p.read, stdin)
			return l.Validate(stdin)
		}
	}
	return linked
}

// chalkctl validate runs the router validator in-process on a node whose image declares the router
// kinds, and refuses what it refuses.
func TestValidateRunsTheRouterValidator(t *testing.T) {
	var read [][]byte
	saved := plugins
	plugins = slices.Clone(plugins)
	for i, p := range plugins {
		if r, ok := p.(*routerlayer.Plugin); ok {
			plugins[i] = recordingRouter{r, &read}
		}
	}
	t.Cleanup(func() { plugins = saved })
	ta := newTestApp(t)
	ta.withoutSecrets(t)
	if err := ta.run(context.Background(), []string{"validate", "--inventory", writeInventory(t, ta.dir)}); err != nil || len(read) != 0 {
		t.Fatalf("validate without the router kinds = %v, the validator read %q", err, read)
	}
	// The uplink is a link of the default VRF, which the router kinds' rules want for the node API.
	uplink := strings.Replace(n1Network, "spec: {", "spec: {match: {name: enp1s0}, ", 1)
	vrf := "apiVersion: router.chalkos.dev/v1alpha1\nkind: VRF\nmetadata: {name: tenant}\nspec: {table: 100}\n"
	path := writeInventory(t, ta.dir, n1SystemDisk, uplink, vrf)
	schematest.Copy(t, filepath.Join(ta.dir, "images", "test-metal", "schemas"), schematest.Router)
	if err := ta.run(context.Background(), []string{"validate", "--inventory", path}); err != nil {
		t.Fatal(err)
	}
	if len(read) != 1 {
		t.Fatalf("the validator ran %d times", len(read))
	}
	var in struct {
		Documents map[string][]struct{ Kind string }
	}
	if err := json.Unmarshal(read[0], &in); err != nil || len(in.Documents["Network"]) != 1 || len(in.Documents["VRF"]) != 1 || len(in.Documents["Router"]) != 0 {
		t.Errorf("the validator read %s: %v", read[0], err)
	}
	// It refuses what the validator refuses, as the node would: a zone that names a VRF's device.
	zone := "apiVersion: router.chalkos.dev/v1alpha1\nkind: FirewallZone\nmetadata: {name: lan}\nspec: {interfaces: [tenant]}\n"
	path = writeInventory(t, ta.dir, n1SystemDisk, uplink, vrf, zone)
	err := ta.run(context.Background(), []string{"validate", "--inventory", path})
	if want := "FirewallZone/lan: spec.interfaces[0]: tenant matches tenant, the device of VRF tenant; name the VRF in vrfs"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("validate = %v, want %q", err, want)
	}
}
