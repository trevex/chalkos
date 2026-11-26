package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/chalkdtest"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	kubernetesplugin "github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/internal/poll"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd/etcdtest"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/sdk"
	"github.com/trevex/chalkos/pkg/sdk/sdktest"
)

// controlPlane serves chalkd with the Kubernetes layer on the control-plane node n1, whose files
// hold a share and are prepared, and dials it as an admin. options, when not nil, changes the
// layer's options once the node's files are written.
func controlPlane(t *testing.T, options func(o *kubernetesplugin.Options, s knode.State)) *client.Conn {
	t.Helper()
	secrets, err := clustersecrets.Generate(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	paths := kubernetestest.Paths(root, filepath.Join(root, "state"))
	kubernetestest.WriteNode(t, paths, k8s.KindControlPlane, true)
	slot := filepath.Join(root, "state", "config", "a")
	o := kubernetesplugin.Options{
		Paths:       paths,
		ImageFile:   kubernetestest.ImageFile(paths),
		ClusterFile: kubernetestest.ClusterFile(root),
		Hooks: kubernetesplugin.Hooks{
			Slot:              func() (string, error) { return slot, nil },
			Objects:           kubernetestest.NoObjects,
			ControlPlane:      kubernetestest.AppliedAtOnce,
			Node:              kubernetestest.NodeReady,
			Pods:              kubernetestest.NoPods,
			EtcdEndpoints:     kubernetestest.NoEtcdEndpoints,
			ClusterAnswers:    kubernetestest.NoClusterAnswers,
			CheckControlPlane: kubernetestest.ControlPlaneCurrent,
			APIClient:         kubernetestest.EmptyAPIServer,
		},
	}
	if options != nil {
		options(&o, knode.State{Paths: paths})
	}
	s := &chalkd.Server{Mode: nodev1.Mode_MODE_NORMAL, RebootNode: sdktest.StayUp}
	k := kubernetesplugin.New(s, o)
	// The loops a bootstrap started would run on against the test's removed files.
	t.Cleanup(k.Stop)
	s.Plugins = chalkdtest.Plugins(t, s, layers.Of(layers.Kubernetes), k)
	s.Loader.Layers = layer.Active[nodeconfig.Layer](s.Plugins)
	node := sdktest.Serve(t, s, secrets.OSCA, secrets.NodeCA, "n1", nil)
	cert, err := sdk.AdminCertificate(secrets, "sdk-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pki.BundlePool(secrets.OSCABundle())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sdk.Dial(node.Addr, sdk.Trust{OSCAs: pool, Name: "n1"}, cert)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// etcdRetry is how long chalkd waits before it asks etcd again to remove a member it refuses to
// remove for now.
const etcdRetry = 50 * time.Millisecond

// etcdMember serves the bootstrapped control-plane node n1, as controlPlane does, whose etcd
// member runs with the members named in others, and returns the members by name. The health
// checks wait five seconds for each member, so a member a test stops is not waited for long.
func etcdMember(t *testing.T, others ...string) (*client.Conn, map[string]*etcdtest.Member) {
	t.Helper()
	members := map[string]*etcdtest.Member{}
	conn := controlPlane(t, func(o *kubernetesplugin.Options, s knode.State) {
		share, err := s.ReadShare()
		if err != nil {
			t.Fatal(err)
		}
		n1 := etcdtest.StartNew(t, *share.EtcdCA, "n1")
		members["n1"] = n1
		cli, err := etcd.Dial([]string{n1.ClientURL}, etcdtest.ClientTLS(t, *share.EtcdCA))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cli.Close() })
		for _, name := range others {
			members[name] = addMember(t, cli, *share.EtcdCA, name)
		}
		o.LocalEtcd = n1.ClientURL
		o.Timing.EtcdRetry = etcdRetry
		o.Timing.EtcdStatusTimeout = 5 * time.Second
		o.Hooks.EtcdStopped = func(context.Context, k8s.Cluster) error { return nil }
		ips, err := s.ReadNodeIPs()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.WritePin(ips); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{s.BootstrappedMarker(), s.EtcdInitialisedMarker(), filepath.Join(s.EtcdData, "member", "snap", "db")} {
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.RenderStaticPods(); err != nil {
			t.Fatal(err)
		}
	})
	return conn, members
}

// addMember starts a member of the name and makes it a voter of the cluster cli reaches.
func addMember(t *testing.T, cli *etcd.Client, ca pki.CertKey, name string) *etcdtest.Member {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	peerURL := etcdtest.PeerURL(t)
	var added etcd.Member
	var initial string
	// etcd adds no member while one it just promoted is not yet in touch with the others.
	err := poll.UntilDone(ctx, "the learner", 100*time.Millisecond, func(ctx context.Context) (err error) {
		added, initial, err = cli.AddLearner(ctx, name, peerURL)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m := etcdtest.StartExisting(t, ca, name, peerURL, initial)
	if err := cli.Promote(ctx, added.ID, 100*time.Millisecond, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	return m
}
