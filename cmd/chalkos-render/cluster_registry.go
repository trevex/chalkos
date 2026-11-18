package main

import (
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/kubernetes/clusterregistry"
	"github.com/trevex/chalkos/pkg/refusals"
)

// renderClusterRegistry writes the environment file of chalkos-registry.service, its port, and
// its firewall table, whose rule marks the port with mark, when the node serves the cluster
// registry: when one of the ClusterRegistry's endpoints is one of its static addresses. Without
// a ClusterRegistry, or on a node that serves none, it writes nothing, and the unit does not
// start.
func renderClusterRegistry(mark string, in map[string]any, out string) error {
	found, err := decodeDocuments[clusterregistry.Spec](in, clusterregistry.Kind)
	if err != nil || len(found) == 0 {
		return err
	}
	r, err := clusterregistry.New(found[0].Spec)
	if err != nil {
		return refusals.Errorf(clusterregistry.Kind, found[0].Metadata.Name, "spec", "%s", err)
	}
	networkDocs, err := decodeDocuments[map[string]any](in, "Network")
	if err != nil {
		return err
	}
	networks := map[string]any{}
	for _, n := range networkDocs {
		networks[n.Metadata.Name] = map[string]any{"address": identity.NetworkAddresses(n.Spec)}
	}
	static := identity.Identity{Network: map[string]any{"networks": networks}}.StaticAddresses()
	if _, ok := r.Serving(clusterregistry.Addrs(static)); !ok {
		return nil
	}
	if err := writeFile(out, "registry.env", []byte(r.Env())); err != nil {
		return err
	}
	return writeFile(out, "registry.nft", []byte(clusterregistry.Firewall(r.Port, mark)))
}
