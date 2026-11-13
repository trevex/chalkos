package kubernetes_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/console"
	"github.com/trevex/chalkos/pkg/console/consoletest"
	"github.com/trevex/chalkos/pkg/console/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/layers"
)

// statusOf is a Kubernetes layer that reports st.
type statusOf struct{ st *kubernetesv1.StatusResponse }

func (s statusOf) Status(context.Context, *connect.Request[kubernetesv1.StatusRequest]) (*connect.Response[kubernetesv1.StatusResponse], error) {
	return connect.NewResponse(s.st), nil
}

// controlPlane is a bootstrapped control plane with 23 pods that do not run, of which its status
// lists two.
var controlPlane = &kubernetesv1.StatusResponse{Kind: "controlplane", State: "bootstrapped", NodeReady: "True", Vip: "holder",
	ControlPlane: "current", ApiServerVersion: "1.37.1", KubeletVersion: "1.37.1", ControlPlanePods: "current",
	PodsNotRunning: []*kubernetesv1.NodePod{
		{Namespace: "apps", Name: "migrate-7c2b", Phase: "Pending"},
		{Namespace: "web", Name: "crash-5d9f", Phase: "Running", Reason: "CrashLoopBackOff"},
	}, PodsNotRunningTotal: 23}

func kubernetesNode() *consoletest.Client {
	c := consoletest.Normal()
	c.InfoResp.Layers = []string{layers.Kubernetes}
	return c
}

// The tab shows the layer's status section, the Ready condition and the pods that do not run.
func TestKubernetesTab(t *testing.T) {
	o := consoletest.Options()
	o.Tabs = []console.Tab{kubernetes.New(statusOf{controlPlane})}
	tm := consoletest.Start(t, console.New(kubernetesNode(), o))
	consoletest.WaitFor(t, tm, "5 Kubernetes")
	consoletest.Press(tm, "5")
	consoletest.WaitFor(t, tm, "CrashLoopBackOff")
	consoletest.Snapshot(t, tm)
}

// A Ready condition that is not True shows its reason, and text from the node shows its control
// characters escaped.
func TestKubernetesTabNotReady(t *testing.T) {
	st := &kubernetesv1.StatusResponse{Kind: "worker", State: "joined", NodeReady: "False", NodeReadyReason: "KubeletNotReady",
		NodeReadyMessage: "container runtime network not ready", ApiServerVersion: "1.37.1", KubeletVersion: "1.37.1",
		PodsError: "forbidden\x1b[2J"}
	o := consoletest.Options()
	o.Tabs = []console.Tab{kubernetes.New(statusOf{st})}
	tm := consoletest.Start(t, console.New(kubernetesNode(), o))
	consoletest.WaitFor(t, tm, "5 Kubernetes")
	consoletest.Press(tm, "5")
	consoletest.WaitFor(t, tm, "pods unknown")
	if screen := consoletest.Snapshot(t, tm); !strings.Contains(screen, `forbidden\x1b[2J`) {
		t.Errorf("the error is not escaped:\n%s", screen)
	}
}

// On a control plane, a reboot from the console warns that it skips chalkctl's drain and quorum
// check.
func TestKubernetesWarnsBeforeReboot(t *testing.T) {
	o := consoletest.Options()
	o.Actions = true
	o.Tabs = []console.Tab{kubernetes.New(statusOf{controlPlane})}
	tm := consoletest.Start(t, console.New(kubernetesNode(), o))
	// The tab reads the status after the node's, so the warning holds before it is shown.
	consoletest.WaitFor(t, tm, "5 Kubernetes")
	consoletest.Press(tm, "5")
	consoletest.WaitFor(t, tm, "CrashLoopBackOff")
	consoletest.Press(tm, "a")
	consoletest.Press(tm, "r")
	consoletest.WaitFor(t, tm, "quorum")
	consoletest.Snapshot(t, tm)
}

// A worker's reboot warns that it skips chalkctl's drain, and names no quorum.
func TestKubernetesWarnsWorker(t *testing.T) {
	st := &kubernetesv1.StatusResponse{Kind: "worker", State: "joined", NodeReady: "True"}
	o := consoletest.Options()
	o.Actions = true
	o.Tabs = []console.Tab{kubernetes.New(statusOf{st})}
	tm := consoletest.Start(t, console.New(kubernetesNode(), o))
	consoletest.WaitFor(t, tm, "5 Kubernetes")
	consoletest.Press(tm, "5")
	consoletest.WaitFor(t, tm, "kubernetes worker: joined")
	consoletest.Press(tm, "a")
	consoletest.Press(tm, "r")
	consoletest.WaitFor(t, tm, "the console does not")
	if screen := consoletest.Snapshot(t, tm); strings.Contains(screen, "quorum") {
		t.Errorf("a worker's reboot warns of the quorum:\n%s", screen)
	}
}

// A rollback of the image reboots the node, so it warns as a reboot does.
func TestKubernetesWarnsBeforeImageRollback(t *testing.T) {
	o := consoletest.Options()
	o.Actions = true
	o.Tabs = []console.Tab{kubernetes.New(statusOf{controlPlane})}
	tm := consoletest.Start(t, console.New(kubernetesNode(), o))
	consoletest.WaitFor(t, tm, "5 Kubernetes")
	consoletest.Press(tm, "5")
	consoletest.WaitFor(t, tm, "CrashLoopBackOff")
	consoletest.Press(tm, "a")
	consoletest.Press(tm, "i")
	consoletest.WaitFor(t, tm, "quorum")
	consoletest.Snapshot(t, tm)
}

// The tab shows only on images with the Kubernetes layer.
func TestKubernetesTabNeedsTheLayer(t *testing.T) {
	if kubernetes.New(statusOf{controlPlane}).Applies(consoletest.Normal().InfoResp) {
		t.Error("the tab applies to an image without the layer")
	}
}

// The tab reads the status after the node's first read, then every 5 seconds while shown and
// every 30 while hidden, and none while the console is idle.
func TestKubernetesTabReads(t *testing.T) {
	tab := kubernetes.New(statusOf{controlPlane})
	node := console.NodeMsg{Info: console.Source[*nodev1.InfoResponse]{Value: kubernetesNode().InfoResp, At: consoletest.Now}}
	_, cmd := tab.Update(node)
	if cmd == nil {
		t.Fatal("no read after the node's")
	}
	tab.Update(cmd())
	if _, cmd := tab.Update(node); cmd != nil {
		t.Error("a second read after the node's")
	}
	for _, tc := range []struct {
		tick console.TickMsg
		read bool
	}{
		{console.TickMsg{Shown: true, Now: consoletest.Now.Add(4 * time.Second)}, false},
		{console.TickMsg{Shown: false, Now: consoletest.Now.Add(29 * time.Second)}, false},
		{console.TickMsg{Shown: true, Idle: true, Now: consoletest.Now.Add(time.Hour)}, false},
		{console.TickMsg{Shown: true, Now: consoletest.Now.Add(5 * time.Second)}, true},
	} {
		if _, cmd := tab.Update(tc.tick); (cmd != nil) != tc.read {
			t.Errorf("%+v: read %v, want %v", tc.tick, cmd != nil, tc.read)
		}
	}
}

// statusFails is a Kubernetes layer whose status cannot be read.
type statusFails struct{}

func (statusFails) Status(context.Context, *connect.Request[kubernetesv1.StatusRequest]) (*connect.Response[kubernetesv1.StatusResponse], error) {
	return nil, connect.NewError(connect.CodeUnavailable, errors.New("the API server does not answer"))
}

// While the status cannot be read, the node may be a control plane, and a reboot warns as on one.
func TestKubernetesWarnsWithoutStatus(t *testing.T) {
	o := consoletest.Options()
	o.Actions = true
	o.Tabs = []console.Tab{kubernetes.New(statusFails{})}
	tm := consoletest.Start(t, console.New(kubernetesNode(), o))
	consoletest.WaitFor(t, tm, "5 Kubernetes")
	consoletest.Press(tm, "5")
	consoletest.WaitFor(t, tm, "does not answer")
	consoletest.Press(tm, "a")
	consoletest.Press(tm, "r")
	consoletest.WaitFor(t, tm, "may be a control plane")
	consoletest.Snapshot(t, tm)
}
