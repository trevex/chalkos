package config

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// homelabImage is the Kubernetes facts of the homelab's images.
var homelabImage = kubernetes.Image{Version: "1.37.1", CNI: kubernetes.CNIFlannel, Images: kubernetes.Images{
	Etcd:                  "registry.k8s.io/etcd:3.7.0-0",
	KubeAPIServer:         "registry.k8s.io/kube-apiserver:v1.37.1",
	KubeControllerManager: "registry.k8s.io/kube-controller-manager:v1.37.1",
	KubeScheduler:         "registry.k8s.io/kube-scheduler:v1.37.1",
	KubeProxy:             "registry.k8s.io/kube-proxy:v1.37.1",
	CoreDNS:               "registry.k8s.io/coredns/coredns:v1.14.6",
	Flannel:               "ghcr.io/flannel-io/flannel:v0.28.9",
}}

// The homelab worker's documents on the homelab's image give the whole cluster in
// testdata/homelab-w1-cluster.json.
func TestClusterHomelabWorker(t *testing.T) {
	c, err := homelabSet(t, "w1").Documents().Cluster(homelabImage)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		t.Fatal(err)
	}
	golden := "testdata/homelab-w1-cluster.json"
	if *update {
		if err := os.WriteFile(golden, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Errorf("the cluster differs from %s; run go test ./pkg/config -update and review the diff:\n%s", golden, b.String())
	}
}
