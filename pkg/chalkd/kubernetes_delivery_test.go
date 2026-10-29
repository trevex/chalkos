package chalkd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// clusterDocuments is a worker's set with its Kubernetes and Node documents as chalkd stores
// them, schema defaults filled in.
const clusterDocuments = `
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: default}
spec:
  endpoint: "https://10.0.0.10:6443"
  domain: cluster.local
  allowSchedulingOnControlPlanes: false
  ipFamilies: [ipv4]
  podCIDRs: {ipv4: 10.244.0.0/16, ipv6: "fd00:10:244::/56"}
  serviceCIDRs: {ipv4: 10.96.0.0/12, ipv6: "fd00:10:96::/112"}
  dnsIPs: {ipv4: null, ipv6: null}
  nodeCIDRMaskSizes: {ipv4: 24, ipv6: 64}
  nodeIP: {validSubnets: [], timeout: 300}
  vip: {addresses: [], mode: l2}
  extraArgs: {etcd: {}, kube-apiserver: {}, kube-controller-manager: {}, kube-scheduler: {}}
---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Node
metadata: {name: default}
spec:
  type: worker
  labels: {}
  taints: []
  kubeletExtraArgs: {}
  nodeIP: {addresses: null, validSubnets: null}
  vip: {interface: null}
`

// deliveryLayer is the Kubernetes layer of an image whose image.json is testImage, with its files
// in a directory of the test.
func deliveryLayer(t *testing.T) *kubernetesLayer {
	t.Helper()
	root := t.TempDir()
	k := NewKubernetes(KubernetesOptions{
		Paths: knode.Paths{
			State: filepath.Join(root, "state"), Cluster: filepath.Join(root, "cluster.json"), NodeFile: filepath.Join(root, "node.json"),
			Run: filepath.Join(root, "run"), PKI: filepath.Join(root, "pki"), KubeletPKI: filepath.Join(root, "kubelet"), EtcdData: filepath.Join(root, "etcd"),
		},
		ImageFile:   filepath.Join(root, "etc", "image.json"),
		ClusterFile: filepath.Join(root, "run", "cluster.json"),
	})
	write(t, k.imageFile, testImage)
	return &kubernetesLayer{k: k}
}

func parseDocuments(t *testing.T, text string) config.Documents {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

func TestWriteConfigWritesTheCluster(t *testing.T) {
	p := deliveryLayer(t)
	if err := p.WriteConfig(parseDocuments(t, clusterDocuments)); err != nil {
		t.Fatal(err)
	}
	c, err := k8s.ReadCluster(p.k.clusterFile)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != k8s.KindWorker || c.Endpoint != "https://10.0.0.10:6443" || c.DNSIPs.IPv4.String() != "10.96.0.10" || c.Images.KubeProxy != "p" {
		t.Errorf("cluster = %+v", c)
	}
	// Another cluster replaces it.
	c.Domain = "lab.local"
	if err := p.writeCluster(&c); err != nil {
		t.Fatal(err)
	}
	if got, err := k8s.ReadCluster(p.k.clusterFile); err != nil || got.Domain != "lab.local" {
		t.Errorf("new domain: %+v, %v", got, err)
	}
}

// A set without a Cluster document leaves no cluster file, and says why.
func TestWriteConfigWithoutClusterDocument(t *testing.T) {
	p := deliveryLayer(t)
	write(t, p.k.clusterFile, "{}")
	docs := parseDocuments(t, "apiVersion: kubernetes.chalkos.dev"+strings.SplitN(clusterDocuments, "---\napiVersion: kubernetes.chalkos.dev", 2)[1])
	if err := p.WriteConfig(docs); err == nil || !strings.Contains(err.Error(), "no Cluster") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(p.k.clusterFile); !os.IsNotExist(err) {
		t.Errorf("kept the cluster file: %v", err)
	}
}

// A slot without documents, an identity alone, has no cluster file; an image without its
// image.json neither.
func TestWriteConfigWithoutSetOrImageRemovesTheCluster(t *testing.T) {
	p := deliveryLayer(t)
	write(t, p.k.clusterFile, "{}")
	if err := p.WriteConfig(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.k.clusterFile); !os.IsNotExist(err) {
		t.Errorf("without a set: kept the cluster file: %v", err)
	}

	p = deliveryLayer(t)
	if err := os.Remove(p.k.imageFile); err != nil {
		t.Fatal(err)
	}
	write(t, p.k.clusterFile, "{}")
	if err := p.WriteConfig(parseDocuments(t, clusterDocuments)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.k.clusterFile); !os.IsNotExist(err) {
		t.Errorf("without image.json: kept the cluster file: %v", err)
	}
}

// installIdentity is the identity of the node n1 on a Kubernetes image.
const installIdentity = `{"hostname": "n1", "kubernetes": {"nodeName": "n1"}}`

// planInstall plans an install of the share for the node with the identity, trusting osCA.
func planInstall(t *testing.T, p *kubernetesLayer, share []byte, osCA string) (layer.Commit, error) {
	t.Helper()
	payload, err := kpki.Payload(share)
	if err != nil {
		t.Fatal(err)
	}
	return p.Plan(context.Background(), layer.Planned{Payloads: []*anypb.Any{payload}, Identity: []byte(installIdentity), OSCA: osCA, Install: true})
}

// osCAPEM is the test OS CA's certificate.
func osCAPEM(t *testing.T) string {
	t.Helper()
	osCA, err := testOSCA()
	if err != nil {
		t.Fatal(err)
	}
	return osCA.Certificate
}

// An install checks the share alone, against the node and the OS CAs it is installed with, and
// writes it to the STATE it lays out in its canonical encoding.
func TestPlanInstallChecksTheShareAlone(t *testing.T) {
	p := deliveryLayer(t)
	// No cluster file exists: an install reads none.
	c, err := planInstall(t, p, testShare(t, k8s.KindWorker), osCAPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	if c.Config != nil || c.Applied != nil || c.Payloads == nil {
		t.Fatalf("commit = %+v, want the share's payload alone", c)
	}
	state := t.TempDir()
	if err := c.Payloads(state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "kubernetes", "share.json")
	share, err := kpki.ParseShare([]byte(readFile(t, path)))
	if err != nil || share.Node() != "n1" {
		t.Errorf("share.json for %q: %v", share.Node(), err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("share.json mode = %v, %v; want 0600", info, err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("kubernetes/ mode = %v, %v; want 0700", info, err)
	}

	// A worker share for another node is refused: ParseShare alone cannot catch this, since the
	// share is internally consistent and only wrong for this node.
	if _, err := planInstall(t, p, testShareFor(t, k8s.KindWorker, "other"), osCAPEM(t)); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "Kubernetes share") {
		t.Errorf("a share for another node: %v", err)
	}
}

// An install refuses a share that does not parse, one with a field Share does not declare, and one
// whose node CA the OS CAs do not verify.
func TestPlanInstallRefusesShares(t *testing.T) {
	k, err := clustersecrets.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	worker, err := kpki.WorkerShare(k, "n1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := worker.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	fields["extra"] = json.RawMessage(`"unexpected"`)
	extra, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	other, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	foreignCA, err := pki.NewNodeCA(other, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := kpki.ControlPlaneShare(k, foreignCA).Encode()
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		share []byte
		want  string
	}{
		"invalid":        {[]byte(`{"kind": "worker", "ca": {"certificate": "not a certificate"}}`), "Kubernetes share"},
		"an extra field": {extra, "Kubernetes share"},
		"a foreign CA":   {foreign, "the share's node CA"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := planInstall(t, deliveryLayer(t), tc.share, osCAPEM(t)); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// STATE holds the share Encode produces, not whatever bytes the request carried, even when both
// describe the same share.
func TestPlanKeepsTheCanonicalShare(t *testing.T) {
	canonical := testShare(t, k8s.KindWorker)
	share, err := kpki.ParseShare(canonical)
	if err != nil {
		t.Fatal(err)
	}
	indented, err := json.MarshalIndent(share, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(indented, canonical) {
		t.Fatal("the indented and canonical encodings are identical; this test proves nothing")
	}
	c, err := planInstall(t, deliveryLayer(t), indented, osCAPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	if err := c.Payloads(state); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(state, "kubernetes", "share.json")); got != string(canonical) {
		t.Errorf("share.json = %s, want the canonical encoding %s", got, canonical)
	}
}

// New OS CAs delivered without a share must verify the node CA the control plane keeps; with a
// share, the delivered share's node CA is checked instead.
func TestPlanChecksTheKeptNodeCAOnlyWithoutAShare(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	p := kubernetesOf(s)
	other, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	trust := layer.Planned{Identity: []byte(installIdentity), OSCA: other.Certificate, Trust: true}
	if _, err := p.Plan(context.Background(), trust); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "do not verify the node's node CA") {
		t.Errorf("trust without a share: %v, want the kept node CA refused", err)
	}

	k, err := clustersecrets.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nodeCA, err := pki.NewNodeCA(other, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	share, err := kpki.ControlPlaneShare(k, nodeCA).Encode()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := kpki.Payload(share)
	if err != nil {
		t.Fatal(err)
	}
	trust.Payloads = []*anypb.Any{payload}
	c, err := p.Plan(context.Background(), trust)
	if err != nil {
		t.Fatalf("trust with a share of a node CA it verifies: %v", err)
	}
	if c.Payloads == nil {
		t.Error("the share is not recorded")
	}
}
