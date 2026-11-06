package kubernetes

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/anypb"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/imageref"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// runs is a node of a test cluster: its Kubernetes kind ("" for a node without a Node document),
// the API server and kubelet it runs, or why its versions could not be read.
type runs struct {
	name, kind, api, kubelet string
	err                      error
}

// versionsLab is the cluster of the nodes and the versions they run, as chalkctl read them.
func versionsLab(t *testing.T, nodes ...runs) (fakeCluster, []layer.NodeVersions) {
	t.Helper()
	c := fakeCluster{types: map[string]string{}, apiServer: map[string]string{}}
	var all []layer.NodeVersions
	for _, n := range nodes {
		c.types[n.name] = n.kind
		if n.err != nil {
			all = append(all, layer.NodeVersions{Name: n.name, Err: n.err})
			continue
		}
		v, err := versionsOf(&kubernetesv1.StatusResponse{Kind: n.kind, ApiServerVersion: n.api, KubeletVersion: n.kubelet})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, layer.NodeVersions{Name: n.name, Versions: v})
	}
	return c, all
}

// threeAndOne is a cluster of three control planes and a worker, each on the API server and the
// kubelet given.
func threeAndOne(api, kubelet string) []runs {
	return []runs{
		{"cp1", k8s.KindControlPlane, api, kubelet, nil},
		{"cp2", k8s.KindControlPlane, api, kubelet, nil},
		{"cp3", k8s.KindControlPlane, api, kubelet, nil},
		{"w1", k8s.KindWorker, api, kubelet, nil},
	}
}

func apiServerRef(tag string) string { return "registry.k8s.io/kube-apiserver:" + tag }

// readOf reads the versions of all, as chalkctl reads them from the nodes named, or from every
// node with nil.
func readOf(all []layer.NodeVersions) layer.ReadVersions {
	return func(names []string) []layer.NodeVersions {
		if names == nil {
			return all
		}
		var read []layer.NodeVersions
		for _, n := range all {
			if slices.Contains(names, n.Name) {
				read = append(read, n)
			}
		}
		return read
	}
}

// readNothing fails the test when a check reads any node's versions.
func readNothing(t *testing.T) layer.ReadVersions {
	return func(names []string) []layer.NodeVersions {
		t.Errorf("read the versions of %q", names)
		return nil
	}
}

// Changes compares the API server the inventory's config names with the one the node's running
// config names, patch releases included.
func TestRolloutChanges(t *testing.T) {
	p := New()
	runningOn := func(ref string) []config.Document {
		docs, _ := fakeCluster{apiServer: map[string]string{"cp1": ref}}.Documents("cp1")
		return docs
	}
	for name, tc := range map[string]struct {
		running []config.Document
		config  map[string]string
		want    string
		err     string
	}{
		"a patch":             {runningOn(apiServerRef("v1.37.1")), map[string]string{"cp1": apiServerRef("v1.37.2")}, "the API server from 1.37.1 to 1.37.2", ""},
		"the same":            {runningOn(apiServerRef("v1.37.1")), map[string]string{"cp1": apiServerRef("v1.37.1")}, "", ""},
		"no Node document":    {runningOn(apiServerRef("v1.37.1")), map[string]string{"n1": apiServerRef("v1.37.2")}, "", ""},
		"pinned, the same":    {runningOn(apiServerRef("v1.37.1")), map[string]string{"cp1": apiServerRef("v1.37.1@sha256:e190f914a6cc21cab9268485d1ae7282fdeab1315ac64b668aa42d732fd1061d")}, "", ""},
		"no Cluster document": {runningOn(apiServerRef("v1.37.1")), map[string]string{}, "", ""},
		"none running":        {nil, map[string]string{"cp1": apiServerRef("v1.37.2")}, "", ""},
		"no version tag":      {runningOn(apiServerRef("v1.37.1")), map[string]string{"cp1": apiServerRef("latest")}, "", "the config of cp1: images.apiServer: "},
		"none tagged running": {runningOn(apiServerRef("latest")), map[string]string{"cp1": apiServerRef("v1.37.2")}, "", "the config cp1 runs: images.apiServer: "},
	} {
		t.Run(name, func(t *testing.T) {
			c := fakeCluster{types: map[string]string{"cp1": k8s.KindControlPlane}, apiServer: tc.config}
			name := "cp1"
			if _, ok := tc.config["n1"]; ok {
				name = "n1"
			}
			got, err := p.Changes(c, name, tc.running)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Changes = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// Kubernetes's version skew policy for a delivery's API server, each rule at its boundary.
func TestCheckConfig(t *testing.T) {
	p := New()
	unknown := errors.New("connection refused")
	with := func(nodes []runs, change func(n *runs)) []runs {
		nodes = slices.Clone(nodes)
		for i := range nodes {
			change(&nodes[i])
		}
		return nodes
	}
	onKubelet := func(name, kubelet string) func(*runs) {
		return func(n *runs) {
			if n.name == name {
				n.kubelet = kubelet
			}
		}
	}
	all := []string{"cp1", "cp2", "cp3", "w1"}
	for name, tc := range map[string]struct {
		nodes     []runs
		named     []string
		tag       string
		downgrade bool
		want      string
	}{
		"one minor newer":     {threeAndOne("1.37.1", "1.37.1"), all, "v1.38.0", false, ""},
		"two minors newer":    {threeAndOne("1.37.1", "1.37.1"), all, "v1.39.0", false, "images.apiServer: v1.39.0 is 2 minors newer than the API server 1.37 that cp1 runs; a cluster's API servers may be at most one minor apart, so upgrade one minor at a time"},
		"one minor older":     {threeAndOne("1.38.0", "1.37.1"), all, "v1.37.5", false, "images.apiServer: v1.37.5 is older than the API server 1.38 that cp1 runs; pass --allow-downgrade to downgrade the control plane"},
		"one older, allowed":  {threeAndOne("1.38.0", "1.37.1"), all, "v1.37.5", true, ""},
		"two minors older":    {threeAndOne("1.39.0", "1.37.1"), all, "v1.37.5", false, "images.apiServer: v1.37.5 is 2 minors older than the API server 1.39 that cp1 runs; a cluster's API servers may be at most one minor apart"},
		"two older, allowed":  {threeAndOne("1.39.0", "1.37.1"), all, "v1.37.5", true, "is 2 minors older than the API server 1.39 that cp1 runs"},
		"a patch older":       {threeAndOne("1.37.5", "1.37.1"), all, "v1.37.1", false, ""},
		"a kubelet newer":     {with(threeAndOne("1.38.0", "1.37.1"), onKubelet("w1", "1.38.0")), all, "v1.37.9", true, "images.apiServer: v1.37.9 is older than the kubelet 1.38 that w1 runs; a kubelet must not be newer than the API server"},
		"kubelet three older": {with(threeAndOne("1.39.0", "1.38.0"), onKubelet("w1", "1.37.0")), all, "v1.40.0", false, ""},
		"kubelet four older":  {with(threeAndOne("1.40.0", "1.38.0"), onKubelet("w1", "1.37.0")), all, "v1.41.0", false, "images.apiServer: v1.41.0 is 4 minors newer than the kubelet 1.37 that w1 runs; a kubelet may be at most three minors older than the API server, so upgrade the nodes' images first"},
		"another major":       {threeAndOne("1.37.1", "1.37.1"), all, "v2.0.0", false, "images.apiServer: v2.0.0 is of Kubernetes 2, and cp1 runs the API server v1.37.1 and the kubelet v1.37.1"},
		"a worker first": {[]runs{
			{"cp1", k8s.KindControlPlane, "1.37.2", "1.37.1", nil},
			{"cp2", k8s.KindControlPlane, "1.37.1", "1.37.1", nil},
			{"cp3", k8s.KindControlPlane, "1.37.2", "1.37.1", nil},
			{"w1", k8s.KindWorker, "1.37.1", "1.37.1", nil},
		}, []string{"w1"}, "v1.37.2", false, "w1 is no control plane, and the control planes cp2 do not run the API server v1.37.2 yet; deliver the config to them first"},
		"a worker after the control planes": {with(threeAndOne("1.37.2", "1.37.1"), func(n *runs) {
			if n.name == "w1" {
				n.api = "1.37.1"
			}
		}), []string{"w1"}, "v1.37.2", false, ""},
		"a worker with the control plane behind": {[]runs{
			{"cp1", k8s.KindControlPlane, "1.37.2", "1.37.1", nil},
			{"cp2", k8s.KindControlPlane, "1.37.1", "1.37.1", nil},
			{"w1", k8s.KindWorker, "1.37.1", "1.37.1", nil},
		}, []string{"w1", "cp2"}, "v1.37.2", false, ""},
		"an unreachable worker": {with(threeAndOne("1.37.1", "1.37.1"), func(n *runs) {
			if n.name == "w1" {
				n.err = unknown
			}
		}), []string{"cp1"}, "v1.37.2", false, "the versions w1 runs are unknown: connection refused; the version skew check needs every node of the cluster"},
		"an unreachable node without Kubernetes": {append(threeAndOne("1.37.1", "1.37.1"), runs{"n1", "", "", "", unknown}), all, "v1.37.2", false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			c, nodes := versionsLab(t, tc.nodes...)
			for _, n := range tc.named {
				c.apiServer[n] = apiServerRef(tc.tag)
			}
			u := fakeUpgrading{flags: &rolloutFlags{allowDowngrade: tc.downgrade}}
			err := p.CheckConfig(u, c, tc.named, readOf(nodes))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("err = %v, want none", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
	// The nodes of a run get one API server.
	c, nodes := versionsLab(t, threeAndOne("1.37.1", "1.37.1")...)
	c.apiServer["cp1"], c.apiServer["cp2"] = apiServerRef("v1.37.2"), apiServerRef("v1.37.3")
	want := "the config names the API server v1.37.2 for cp1 and v1.37.3 for cp2; deliver one version at a time"
	if err := p.CheckConfig(fakeUpgrading{}, c, []string{"cp1", "cp2"}, readOf(nodes)); err == nil || err.Error() != want {
		t.Errorf("two versions: err = %v, want %q", err, want)
	}
}

// imageOf is an image's layer facts with a kubelet of the version.
func imageOf(t *testing.T, kubelet string) []*anypb.Any {
	t.Helper()
	f, err := k8s.FactsPayload([]byte(`{"version": "` + kubelet + `", "cni": "flannel"}`))
	if err != nil {
		t.Fatal(err)
	}
	return []*anypb.Any{f}
}

// An image's kubelet is no newer than the oldest API server the control planes run.
func TestCheckImages(t *testing.T) {
	p := New()
	unknown := errors.New("connection refused")
	other, err := anypb.New(&kubernetesv1.EtcdMembersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	mixed := []runs{
		{"cp1", k8s.KindControlPlane, "1.38.0", "1.37.1", nil},
		{"cp2", k8s.KindControlPlane, "1.37.2", "1.37.1", nil},
		{"w1", k8s.KindWorker, "1.38.0", "1.37.1", nil},
	}
	for name, tc := range map[string]struct {
		nodes  []runs
		images map[string][]*anypb.Any
		want   string
	}{
		"a minor newer": {threeAndOne("1.37.2", "1.37.1"), map[string][]*anypb.Any{"w1": imageOf(t, "1.38.0")},
			"w1: the image's kubelet 1.38.0 would be newer than the API server 1.37.2 that cp1 runs, the oldest of the cluster; upgrade the control plane first: name the new release's images in the Cluster document and deliver it with chalkctl apply-config"},
		"a patch newer":         {threeAndOne("1.37.2", "1.37.1"), map[string][]*anypb.Any{"w1": imageOf(t, "1.37.5")}, ""},
		"the oldest API server": {mixed, map[string][]*anypb.Any{"cp1": imageOf(t, "1.38.0")}, "cp1: the image's kubelet 1.38.0 would be newer than the API server 1.37.2 that cp2 runs"},
		"no control plane":      {[]runs{{"w1", k8s.KindWorker, "1.37.1", "1.37.1", nil}}, map[string][]*anypb.Any{"w1": imageOf(t, "1.38.0")}, ""},
		"another layer's facts": {threeAndOne("1.37.2", "1.37.1"), map[string][]*anypb.Any{"w1": {other}}, ""},
		"an unreachable control plane": {append(threeAndOne("1.37.2", "1.37.1"), runs{"cp4", k8s.KindControlPlane, "", "", unknown}), map[string][]*anypb.Any{"w1": imageOf(t, "1.37.2")},
			"the versions cp4 runs are unknown: connection refused; the version skew check needs every control plane"},
		"an unreachable worker": {append(threeAndOne("1.37.2", "1.37.1"), runs{"w2", k8s.KindWorker, "", "", unknown}), map[string][]*anypb.Any{"w1": imageOf(t, "1.37.2")}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			c, all := versionsLab(t, tc.nodes...)
			err := p.CheckImages(c, tc.images, readOf(all))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("err = %v, want none", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// An upgrade whose images concern no node of the cluster reads no node's versions: neither an
// image without a kubelet nor one for a node without a Node document.
func TestCheckImagesOutsideTheCluster(t *testing.T) {
	p := New()
	c, _ := versionsLab(t, append(threeAndOne("1.37.1", "1.37.1"), runs{"n1", "", "", "", errors.New("connection refused")})...)
	if err := p.CheckImages(c, map[string][]*anypb.Any{"n1": imageOf(t, "1.38.0")}, readNothing(t)); err != nil {
		t.Errorf("a node without Kubernetes: %v", err)
	}
	if err := p.CheckImages(c, map[string][]*anypb.Any{"w1": nil}, readNothing(t)); err != nil {
		t.Errorf("an image without a kubelet: %v", err)
	}
	// The kubelet's check reads the control planes alone.
	read := func(names []string) []layer.NodeVersions {
		if !slices.Equal(names, []string{"cp1", "cp2", "cp3"}) {
			t.Errorf("read the versions of %q, want the control planes", names)
		}
		_, all := versionsLab(t, threeAndOne("1.37.1", "1.37.1")...)
		return all[:3]
	}
	if err := p.CheckImages(c, map[string][]*anypb.Any{"w1": imageOf(t, "1.37.1")}, read); err != nil {
		t.Errorf("a worker's image: %v", err)
	}
}

func TestDelivered(t *testing.T) {
	for name, tc := range map[string]struct {
		status *kubernetesv1.StatusResponse
		want   string
	}{
		"preparing": {&kubernetesv1.StatusResponse{Kind: k8s.KindControlPlane, Phase: kubernetesv1.Phase_PHASE_PREPARING}, ""},
		"not Ready": {&kubernetesv1.StatusResponse{Kind: k8s.KindWorker, Phase: kubernetesv1.Phase_PHASE_JOINED, NodeReady: "False"}, "its Node is not Ready: False"},
		"pods behind": {&kubernetesv1.StatusResponse{Kind: k8s.KindControlPlane, Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED, NodeReady: "True", ControlPlanePods: "kube-scheduler is not Ready"},
			"its control plane: kube-scheduler is not Ready"},
		"current":        {&kubernetesv1.StatusResponse{Kind: k8s.KindControlPlane, Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED, NodeReady: "True", ControlPlanePods: "current", ControlPlanePodsCurrent: true}, ""},
		"a Ready worker": {&kubernetesv1.StatusResponse{Kind: k8s.KindWorker, Phase: kubernetesv1.Phase_PHASE_JOINED, NodeReady: "True"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := deliveredOf(tc.status)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("err = %v, want none", err)
			case tc.want != "" && (err == nil || err.Error() != tc.want):
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSummary(t *testing.T) {
	c, all := versionsLab(t,
		runs{"cp1", k8s.KindControlPlane, "1.37.2", "1.37.1", nil},
		runs{"cp2", k8s.KindControlPlane, "1.37.1", "1.37.1", nil},
		runs{"cp3", k8s.KindControlPlane, "", "", errors.New("connection refused")},
		runs{"w1", k8s.KindWorker, "1.37.1", "1.37.1", nil},
	)
	want := []string{"API server unknown: cp3", "API server v1.37.1: cp2, w1", "API server v1.37.2: cp1"}
	if got := New().Summary(c, readOf(all)); !slices.Equal(got, want) {
		t.Errorf("Summary = %q, want %q", got, want)
	}
}

// versionsOf refuses a status without the versions, as of a node that reports none.
func TestVersionsOf(t *testing.T) {
	if _, err := versionsOf(&kubernetesv1.StatusResponse{Kind: k8s.KindWorker, KubeletVersion: "1.37.1"}); err == nil || !strings.Contains(err.Error(), "its API server's version") {
		t.Errorf("no API server version: %v", err)
	}
	v, err := versionsOf(&kubernetesv1.StatusResponse{Kind: k8s.KindWorker, ApiServerVersion: "1.37.2", KubeletVersion: "1.37.1"})
	if want := (versions{k8s.KindWorker, imageref.Version{Major: 1, Minor: 37, Patch: 2}, imageref.Version{Major: 1, Minor: 37, Patch: 1}}); err != nil || v != want {
		t.Errorf("versionsOf = %+v, %v; want %+v", v, err, want)
	}
}
