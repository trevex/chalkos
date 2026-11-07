package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/types/known/anypb"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/imageref"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/translate"
)

// rolloutFlags are the Kubernetes layer's flags of chalkctl apply-config.
type rolloutFlags struct {
	// allowDowntime is --allow-downtime, allowDowngrade --allow-downgrade.
	allowDowntime, allowDowngrade bool
}

// RolloutFlags registers --allow-downtime and --allow-downgrade of apply-config.
func (p *Plugin) RolloutFlags(fs *pflag.FlagSet) any {
	f := &rolloutFlags{}
	fs.BoolVar(&f.allowDowntime, "allow-downtime", false, "deliver a new API server version to a control plane of a cluster with one or two, whose API may be unavailable while that control plane's API server restarts")
	fs.BoolVar(&f.allowDowngrade, "allow-downgrade", false, "deliver an API server one minor release older than the newest the control planes run")
	return f
}

// rolloutFlagsOf are the run's values of the layer's apply-config flags, their defaults when the
// run has none.
func (p *Plugin) rolloutFlagsOf(u layer.Upgrading) rolloutFlags {
	if f, ok := u.Flags(p.Name()).(*rolloutFlags); ok {
		return *f
	}
	return rolloutFlags{}
}

// versions are what a node of the cluster runs that Kubernetes's version skew policy bounds: the
// API server its running config names and its image's kubelet.
type versions struct {
	kind               string
	apiServer, kubelet imageref.Version
}

// Versions reads the node's kind and versions from its status.
func (p *Plugin) Versions(ctx context.Context, conn *client.Conn) (any, error) {
	k, err := Status(ctx, conn)
	if err != nil {
		return nil, err
	}
	return versionsOf(k)
}

// versionsOf reads the versions of a node's Kubernetes status.
func versionsOf(k *kubernetesv1.StatusResponse) (versions, error) {
	api, err := imageref.ParseVersion(k.GetApiServerVersion())
	if err != nil {
		return versions{}, fmt.Errorf("its API server's version: %w", err)
	}
	kubelet, err := imageref.ParseVersion(k.GetKubeletVersion())
	if err != nil {
		return versions{}, fmt.Errorf("its kubelet's version: %w", err)
	}
	return versions{kind: k.GetKind(), apiServer: api, kubelet: kubelet}, nil
}

// apiServerOf is the version of the API server the node's config in the inventory names; false
// for a node whose config holds no Cluster document.
func apiServerOf(c layer.Cluster, name string) (imageref.Version, bool, error) {
	docs, err := c.Documents(name)
	if err != nil {
		return imageref.Version{}, false, err
	}
	v, ok, err := apiServerIn(docs)
	if err != nil {
		return imageref.Version{}, false, fmt.Errorf("the config of %s: %w", name, err)
	}
	return v, ok, nil
}

// apiServerIn is the version of the API server the Cluster document of docs names; false without
// one.
func apiServerIn(docs []config.Document) (imageref.Version, bool, error) {
	for _, d := range docs {
		if d.APIVersion != translate.API || d.Kind != "Cluster" {
			continue
		}
		spec, _ := d.Object["spec"].(map[string]any)
		images, _ := spec["images"].(map[string]any)
		ref, _ := images["apiServer"].(string)
		v, err := k8s.Images{KubeAPIServer: ref}.APIServerVersion()
		return v, err == nil, err
	}
	return imageref.Version{}, false, nil
}

// Changes says how the inventory's config changes the version of the API server the node's
// running config names, patch releases included. A node without a Node document is no part of
// the cluster, and a node whose running config names no API server, as one that joins the
// cluster, runs none the delivery changes.
func (p *Plugin) Changes(c layer.Cluster, name string, running []config.Document) (string, error) {
	if kind, err := kindOf(c, name); err != nil || kind == "" {
		return "", err
	}
	next, ok, err := apiServerOf(c, name)
	if err != nil || !ok {
		return "", err
	}
	now, ok, err := apiServerIn(running)
	switch {
	case err != nil:
		return "", fmt.Errorf("the config %s runs: %w", name, err)
	case !ok || now == next:
		return "", nil
	}
	return fmt.Sprintf("the API server from %s to %s", now.Release(), next.Release()), nil
}

// running is what the cluster's nodes run, by node, as their statuses report it.
type running map[string]versions

// runningOf reads the nodes' versions. A Kubernetes node of the inventory whose versions could
// not be read refuses the check when it is of the kind needed, or of any kind when needed is "":
// the check of a delivery's API server needs every kubelet, that of an image's kubelet only the
// API servers.
func runningOf(c layer.Cluster, all []layer.NodeVersions, needed string) (running, error) {
	r := running{}
	for _, n := range all {
		if n.Err != nil {
			kind, err := kindOf(c, n.Name)
			if err != nil {
				return nil, err
			}
			if kind == "" || needed != "" && kind != needed {
				continue
			}
			what := "every node of the cluster"
			if needed == k8s.KindControlPlane {
				what = "every control plane"
			}
			return nil, fmt.Errorf("the versions %s runs are unknown: %w; the version skew check needs %s", n.Name, n.Err, what)
		}
		if v, ok := n.Versions.(versions); ok {
			r[n.Name] = v
		}
	}
	return r, nil
}

// extreme is the node of kind ("" for any) whose version of what pick reads is the oldest, or
// with newest the newest, in name order on a tie; "" when none is of kind.
func (r running) extreme(kind string, pick func(versions) imageref.Version, newest bool) string {
	best := ""
	for _, name := range slices.Sorted(maps.Keys(r)) {
		v := r[name]
		if kind != "" && v.kind != kind {
			continue
		}
		if best == "" {
			best = name
			continue
		}
		d := pick(v).Minors(pick(r[best]))
		if newest && d > 0 || !newest && d < 0 {
			best = name
		}
	}
	return best
}

func apiServer(v versions) imageref.Version { return v.apiServer }
func kubelet(v versions) imageref.Version   { return v.kubelet }

// CheckConfig holds the API server the config names to Kubernetes's version skew policy against
// what every node runs, minor releases compared:
//   - the API servers of a cluster stay within one minor of each other;
//   - a minor older than a control plane runs is a downgrade, which --allow-downgrade allows;
//   - no kubelet is newer than the API server, nor more than three minors older;
//   - every node of the run gets the same API server, and a node that is no control plane only
//     once every control plane runs it.
func (p *Plugin) CheckConfig(u layer.Upgrading, c layer.Cluster, named []string, read layer.ReadVersions) error {
	nodes, err := members(c)
	if err != nil {
		return err
	}
	r, err := runningOf(c, read(nodes), "")
	if err != nil {
		return err
	}
	var next imageref.Version
	first := ""
	for _, name := range named {
		v, ok, err := apiServerOf(c, name)
		if err != nil {
			return err
		}
		switch {
		case !ok:
			continue
		case first == "":
			next, first = v, name
		case v != next:
			return fmt.Errorf("the config names the API server %s for %s and %s for %s; deliver one version at a time", next.Release(), first, v.Release(), name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(r)) {
		if v := r[name]; v.apiServer.Major != next.Major || v.kubelet.Major != next.Major {
			return fmt.Errorf("images.apiServer: %s is of Kubernetes %d, and %s runs the API server %s and the kubelet %s", next, next.Major, name, v.apiServer.Release(), v.kubelet.Release())
		}
	}
	if oldest := r.extreme(k8s.KindControlPlane, apiServer, false); oldest != "" {
		if d := next.Minors(r[oldest].apiServer); d > 1 {
			return fmt.Errorf("images.apiServer: %s is %d minors newer than the API server %s that %s runs; a cluster's API servers may be at most one minor apart, so upgrade one minor at a time", next, d, r[oldest].apiServer.MinorString(), oldest)
		}
	}
	if newest := r.extreme(k8s.KindControlPlane, apiServer, true); newest != "" {
		switch d := r[newest].apiServer.Minors(next); {
		case d > 1:
			return fmt.Errorf("images.apiServer: %s is %d minors older than the API server %s that %s runs; a cluster's API servers may be at most one minor apart", next, d, r[newest].apiServer.MinorString(), newest)
		case d > 0 && !p.rolloutFlagsOf(u).allowDowngrade:
			return fmt.Errorf("images.apiServer: %s is older than the API server %s that %s runs; pass --allow-downgrade to downgrade the control plane", next, r[newest].apiServer.MinorString(), newest)
		}
	}
	if newest := r.extreme("", kubelet, true); newest != "" && r[newest].kubelet.Minors(next) > 0 {
		return fmt.Errorf("images.apiServer: %s is older than the kubelet %s that %s runs; a kubelet must not be newer than the API server", next, r[newest].kubelet.MinorString(), newest)
	}
	if oldest := r.extreme("", kubelet, false); oldest != "" {
		if d := next.Minors(r[oldest].kubelet); d > 3 {
			return fmt.Errorf("images.apiServer: %s is %d minors newer than the kubelet %s that %s runs; a kubelet may be at most three minors older than the API server, so upgrade the nodes' images first", next, d, r[oldest].kubelet.MinorString(), oldest)
		}
	}
	// Control planes go first: a node that is no control plane gets the version once every control
	// plane outside the run runs it.
	var behind []string
	for _, name := range slices.Sorted(maps.Keys(r)) {
		if v := r[name]; v.kind == k8s.KindControlPlane && v.apiServer != next && !slices.Contains(named, name) {
			behind = append(behind, name)
		}
	}
	if len(behind) > 0 {
		for _, name := range named {
			if v, ok := r[name]; ok && v.kind != k8s.KindControlPlane {
				return fmt.Errorf("%s is no control plane, and the control planes %s do not run the API server %s yet; deliver the config to them first", name, strings.Join(behind, ", "), next.Release())
			}
		}
	}
	return nil
}

// CheckImages refuses an image whose kubelet would be newer than the oldest API server the
// cluster's control planes run, minor releases compared. Only the images of the cluster's nodes
// that carry a kubelet are checked, against the control planes alone: an upgrade of nodes outside
// the cluster reaches none of its nodes.
func (p *Plugin) CheckImages(c layer.Cluster, images map[string][]*anypb.Any, read layer.ReadVersions) error {
	kubelets := map[string]imageref.Version{}
	for _, name := range slices.Sorted(maps.Keys(images)) {
		kind, err := kindOf(c, name)
		if err != nil {
			return err
		}
		// Without an inventory no node's kind is known, so every image is checked.
		if kind == "" && !c.Partial() {
			continue
		}
		for _, f := range images[name] {
			img, ok, err := k8s.ImageOfPayload(f)
			if !ok {
				continue
			}
			if err != nil {
				return fmt.Errorf("%s: the image's image.json: %w", name, err)
			}
			if kubelets[name], err = imageref.ParseVersion(img.Version); err != nil {
				return fmt.Errorf("%s: the image's kubelet: %w", name, err)
			}
		}
	}
	if len(kubelets) == 0 {
		return nil
	}
	var controlPlanes []string
	if !c.Partial() {
		names, err := nodesOf(c, k8s.KindControlPlane, "")
		if err != nil || len(names) == 0 {
			return err
		}
		controlPlanes = names
	}
	r, err := runningOf(c, read(controlPlanes), k8s.KindControlPlane)
	if err != nil {
		return err
	}
	oldest := r.extreme(k8s.KindControlPlane, apiServer, false)
	if oldest == "" {
		return nil
	}
	api := r[oldest].apiServer
	for _, name := range slices.Sorted(maps.Keys(kubelets)) {
		switch v := kubelets[name]; {
		case v.Major != api.Major:
			return fmt.Errorf("%s: the image's kubelet %s is of Kubernetes %d, and the API server %s that %s runs, the oldest of the cluster, of Kubernetes %d; a kubelet and its API server are of one major release", name, v.Release(), v.Major, api.Release(), oldest, api.Major)
		case v.Minors(api) > 0:
			return fmt.Errorf("%s: the image's kubelet %s would be newer than the API server %s that %s runs, the oldest of the cluster; upgrade the control plane first: name the new release's images in the Cluster document and deliver it with chalkctl apply-config", name, v.Release(), api.Release(), oldest)
		}
	}
	return nil
}

// members are the nodes of the inventory with a Node document, whose versions the checks read;
// nil, which reads every node chalkctl knows of, for a cluster without an inventory, whose nodes'
// kinds are known only from what they run.
func members(c layer.Cluster) ([]string, error) {
	if c.Partial() {
		return nil, nil
	}
	names := []string{}
	for _, name := range c.NodeNames() {
		kind, err := kindOf(c, name)
		if err != nil {
			return nil, err
		}
		if kind != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// BeforeDelivery waits for etcd to keep its quorum without a control plane whose API server
// restarts, so the cluster's other API servers keep serving meanwhile.
func (p *Plugin) BeforeDelivery(ctx context.Context, u layer.Upgrading, name string, role layer.UpgradeRole) error {
	if !role.Coordinator {
		return nil
	}
	return quorum(ctx, u, p.rolloutFlagsOf(u).allowDowntime, name, false)
}

// Delivered checks that a node of the cluster has its Node Ready, or held back by its container
// network alone, and on a control plane that its static pods run the images its config names and
// are Ready. A node that is no part of the cluster yet, such as a control plane waiting for the
// bootstrap, has nothing to check. The kubelet's own health is the node's: the trial's
// confirmation waits for it.
func (p *Plugin) Delivered(ctx context.Context, conn *client.Conn) error {
	k, err := Status(ctx, conn)
	if err != nil {
		return err
	}
	return deliveredOf(k)
}

// deliveredOf is Delivered of the node's Kubernetes status.
func deliveredOf(k *kubernetesv1.StatusResponse) error {
	if !inCluster(k) {
		return nil
	}
	switch ready := k.GetNodeReady(); {
	case ready == "True":
	case ready == "False" && networkOnly(k.GetNodeReadyReason(), k.GetNodeReadyMessage()):
		// A cluster created without a CNI has its Nodes NotReady until one is installed; the static
		// pods use the host's network and come up all the same.
	case k.GetNodeReadyMessage() != "":
		return fmt.Errorf("its Node is not Ready: %s: %s", ready, k.GetNodeReadyMessage())
	default:
		return fmt.Errorf("its Node is not Ready: %s", ready)
	}
	if k.GetKind() == k8s.KindControlPlane && !k.GetControlPlanePodsCurrent() {
		return errors.New("its control plane: " + k.GetControlPlanePods())
	}
	return nil
}

// networkOnly reports whether the reason and the message the kubelet gives a Node's Ready
// condition of False hold the node back for its container network alone, as on a cluster without
// a CNI yet. The kubelet's message names each cause, the network's with its NetworkReady=false
// marker, and joins several causes in brackets, so one cause with that marker is the network's
// alone.
func networkOnly(reason, message string) bool {
	return reason == "KubeletNotReady" && !strings.HasPrefix(message, "[") && strings.Contains(message, "NetworkReady=false")
}

// Summary groups the cluster's nodes by the API server their running config names.
func (p *Plugin) Summary(c layer.Cluster, read layer.ReadVersions) []string {
	nodes, err := members(c)
	if err != nil {
		return []string{"the cluster's nodes: " + err.Error()}
	}
	by := map[string][]string{}
	for _, n := range read(nodes) {
		key := "unknown"
		if v, ok := n.Versions.(versions); ok && n.Err == nil {
			key = v.apiServer.Release()
		} else if n.Err == nil {
			continue
		}
		by[key] = append(by[key], n.Name)
	}
	var lines []string
	for _, key := range slices.Sorted(maps.Keys(by)) {
		lines = append(lines, fmt.Sprintf("API server %s: %s", key, strings.Join(by[key], ", ")))
	}
	return lines
}
