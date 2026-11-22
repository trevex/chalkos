package translate

import (
	"fmt"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/kubernetes/kubetest"
)

// TestOpenVXLAN reports a flannel cluster without vxlanSourceSubnets and suggests the prefixes of
// the addresses its nodes register with: the network of a static address, a Node's own address
// alone, and the subnets a node picks from.
func TestOpenVXLAN(t *testing.T) {
	const (
		flannelCluster = "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: default}\nspec: {endpoint: \"https://10.0.0.10:6443\", images: {flannel: ghcr.io/flannel-io/flannel:v0.28.9}}\n"
		dualCluster    = "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: default}\nspec: {endpoint: \"https://10.0.0.10:6443\", ipFamilies: [ipv4, ipv6], images: {flannel: ghcr.io/flannel-io/flannel:v0.28.9}}\n"
		subnetsCluster = "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: default}\nspec: {endpoint: \"https://10.0.0.10:6443\", nodeIP: {validSubnets: [10.1.0.0/16, \"!10.1.9.0/24\"]}, images: {flannel: ghcr.io/flannel-io/flannel:v0.28.9}}\n"
		closed         = "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Flannel\nmetadata: {name: default}\nspec: {vxlanSourceSubnets: [10.0.0.0/24]}\n"
		emptyFlannel   = "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Flannel\nmetadata: {name: default}\nspec: {mtu: 1400}\n"
	)
	network := func(addresses string) string {
		return "---\napiVersion: networkd.chalkos.dev/v1alpha1\nkind: Network\nmetadata: {name: 10-uplink}\nspec: {network: {address: " + addresses + "}}\n"
	}
	worker := kubetest.NodeDoc("default", "{type: worker}")
	for name, tc := range map[string]struct {
		nodes [][]string // the documents of each node
		open  bool
		want  string
	}{
		"static networks": {nodes: [][]string{
			{flannelCluster, worker, network("[10.0.0.11/24]")},
			{flannelCluster, worker, network("[10.0.0.12/24]")},
			{flannelCluster, worker, network("[192.168.5.3/23]")},
		}, open: true, want: "[10.0.0.0/24 192.168.4.0/23]"},
		"both families": {nodes: [][]string{
			{dualCluster, worker, network("[10.0.0.11/24, fd00:1::11/64]")},
		}, open: true, want: "[10.0.0.0/24 fd00:1::/64]"},
		"a Node's own address": {nodes: [][]string{
			{flannelCluster, kubetest.NodeDoc("default", "{type: worker, nodeIP: {addresses: [10.2.0.7]}}"), network("[10.0.0.11/24]")},
		}, open: true, want: "[10.2.0.7/32]"},
		"subnets": {nodes: [][]string{
			{subnetsCluster, worker},
		}, open: true, want: "[10.1.0.0/16]"},
		"a Flannel document without sources": {nodes: [][]string{
			{flannelCluster, emptyFlannel, worker, network("[10.0.0.11/24]")},
		}, open: true, want: "[10.0.0.0/24]"},
		"sources": {nodes: [][]string{
			{flannelCluster, closed, worker, network("[10.0.0.11/24]")},
		}, want: "[]"},
		"no flannel": {nodes: [][]string{
			{endpointDoc("default", "https://10.0.0.10:6443"), worker, network("[10.0.0.11/24]")},
		}, want: "[]"},
	} {
		names := make([]string, len(tc.nodes))
		docs := map[string][]config.Document{}
		for i, d := range tc.nodes {
			names[i] = fmt.Sprintf("n%d", i+1)
			var all string
			for _, s := range d {
				all += s
			}
			docs[names[i]] = parseDocs(t, all)
		}
		open, got := OpenVXLAN(names, func(n string) ([]config.Document, error) { return docs[n], nil })
		if open != tc.open || fmt.Sprint(got) != tc.want {
			t.Errorf("%s: OpenVXLAN = %v, %v; want %v, %s", name, open, got, tc.open, tc.want)
		}
	}
}
