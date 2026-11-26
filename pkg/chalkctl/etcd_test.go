package chalkctl

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd"
	kubernetesplugin "github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd/etcdtest"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// etcdNode is n1 as a bootstrapped control-plane node whose etcd member runs. adjust, when not
// nil, changes the node's Kubernetes side, whose LocalEtcd is the member's.
func etcdNode(t *testing.T, ta *testApp, adjust func(*kubernetesplugin.Options)) *chalkd.Server {
	t.Helper()
	withKind(t, ta, k8s.KindControlPlane)
	share := kpki.ControlPlaneShare(&ta.secrets.Kubernetes, ta.secrets.NodeCA)
	o := kubernetesplugin.Options{
		LocalEtcd: etcdtest.StartNew(t, *share.EtcdCA, "n1").ClientURL,
		Hooks: kubernetesplugin.Hooks{
			// No API server lists the other members.
			EtcdEndpoints: func(context.Context, k8s.Cluster, kpki.Share, []netip.Addr) ([]string, error) {
				return nil, errors.New("connection refused")
			},
		},
	}
	if adjust != nil {
		adjust(&o)
	}
	s, _ := kubernetesNodeWith(t, ta, o)
	p := kubernetesState(s)
	data, err := share.Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p.Share(), string(data))
	writeFile(t, p.Cluster, clusterFile(t, "controlplane"))
	writeFile(t, p.NodeFile, `{"layers": {"kubernetes": {"nodeName": "n1"}}}`)
	writeFile(t, p.BootstrappedMarker(), "")
	return s
}

func TestEtcdMembers(t *testing.T) {
	ta := newTestApp(t)
	s := etcdNode(t, ta, nil)
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"etcd", "members", "--via", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(ta.stdout.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "NAME") || !strings.HasPrefix(lines[1], "n1 ") ||
		!strings.Contains(lines[1], "https://127.0.0.1:") || !strings.HasSuffix(lines[1], "voter   healthy") {
		t.Errorf("members:\n%s", ta.stdout)
	}
	// -o json prints the member list in protojson.
	ta.stdout.Reset()
	if err := ta.run(context.Background(), ta.args([]string{"etcd", "members", "--via", "n1", "-o", "json"}, addr)); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Members []struct {
			Name     string   `json:"name"`
			PeerURLs []string `json:"peerUrls"`
		} `json:"members"`
	}
	if err := json.Unmarshal(ta.stdout.Bytes(), &resp); err != nil || len(resp.Members) != 1 || resp.Members[0].Name != "n1" || len(resp.Members[0].PeerURLs) != 1 {
		t.Errorf("members -o json: %v\n%s", err, ta.stdout)
	}
	// --address reaches one node, which --via names.
	if err := ta.run(context.Background(), ta.args([]string{"etcd", "members"}, addr)); err == nil || !strings.Contains(err.Error(), "--via") {
		t.Errorf("without --via: %v", err)
	}
}

// --through-others reaches the node: without it the node refuses to leave while its own member does not
// answer, with it the node goes on to the quorum guard.
func TestEtcdLeaveThroughOthers(t *testing.T) {
	ta := newTestApp(t)
	s := etcdNode(t, ta, func(o *kubernetesplugin.Options) {
		local := o.LocalEtcd
		o.LocalEtcd = etcdtest.Silent(t)
		// Each leave waits the timeout out for the silent member, while the other one must answer
		// within it also on a busy machine.
		o.Timing.EtcdTimeout = 10 * time.Second
		o.Hooks.EtcdEndpoints = func(context.Context, k8s.Cluster, kpki.Share, []netip.Addr) ([]string, error) {
			return []string{local}, nil
		}
	})
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"etcd", "leave", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "pass --through-others") {
		t.Errorf("err = %v, want a refusal naming --through-others", err)
	}
	err = ta.run(context.Background(), ta.args([]string{"etcd", "leave", "n1", "--through-others"}, addr))
	if err == nil || !strings.Contains(err.Error(), "etcd's last voter") {
		t.Errorf("err = %v, want a refusal for the last voter", err)
	}
}
