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
)

// rolloutFlags are the Kubernetes layer's flags of chalkctl apply-config.
type rolloutFlags struct {
	// allowDowntime is --allow-downtime, allowDowngrade --allow-downgrade.
	allowDowntime, allowDowngrade bool
}

// RolloutFlags registers --allow-downtime and --allow-downgrade of apply-config.
func (p *Plugin) RolloutFlags(fs *pflag.FlagSet) any {
	f := &rolloutFlags{}
	fs.BoolVar(&f.allowDowntime, "allow-downtime", false, "deliver config that changes the API server's version to a control plane of a cluster with one or two, whose API server is down while its control plane restarts")
	fs.BoolVar(&f.allowDowngrade, "allow-downgrade", false, "deliver config whose API server is a minor release older than the one the control planes run")
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
		return nil, layer.ServiceError(err, p.Name())
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
	for _, d := range docs {
		if d.APIVersion != config.KubernetesAPI || d.Kind != "Cluster" {
			continue
		}
		spec, _ := d.Object["spec"].(map[string]any)
		images, _ := spec["images"].(map[string]any)
		ref, _ := images["apiServer"].(string)
		v, err := k8s.Images{KubeAPIServer: ref}.APIServerVersion()
		if err != nil {
			return imageref.Version{}, false, fmt.Errorf("the config of %s: %w", name, err)
		}
		return v, true, nil
	}
	return imageref.Version{}, false, nil
}

// Changes reports whether the inventory's config changes the version of the API server any of
// the nodes runs, patch releases included.
func (p *Plugin) Changes(c layer.Cluster, nodes []layer.NodeVersions) (bool, error) {
	changed := false
	for _, n := range nodes {
		next, ok, err := apiServerOf(c, n.Name)
		switch {
		case err != nil:
			return false, err
		case !ok:
			continue
		}
		if n.Err != nil {
			return false, fmt.Errorf("%s: %w", n.Name, n.Err)
		}
		if v, _ := n.Versions.(versions); next != v.apiServer {
			changed = true
		}
	}
	return changed, nil
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
func (p *Plugin) CheckConfig(u layer.Upgrading, c layer.Cluster, named []string, all []layer.NodeVersions) error {
	r, err := runningOf(c, all, "")
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
			return fmt.Errorf("the config names the API server %s for %s and %s for %s; deliver one version at a time", next, first, v, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(r)) {
		if v := r[name]; v.apiServer.Major != next.Major || v.kubelet.Major != next.Major {
			return fmt.Errorf("images.apiServer: %s is of Kubernetes %d, and %s runs the API server %s and the kubelet %s", next, next.Major, name, v.apiServer, v.kubelet)
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
				return fmt.Errorf("%s is no control plane, and the control planes %s do not run the API server %s yet; deliver the config to them first", name, strings.Join(behind, ", "), next)
			}
		}
	}
	return nil
}

// CheckImages refuses an image whose kubelet would be newer than the oldest API server the
// cluster's control planes run, minor releases compared.
func (p *Plugin) CheckImages(c layer.Cluster, images map[string][]*anypb.Any, all []layer.NodeVersions) error {
	r, err := runningOf(c, all, k8s.KindControlPlane)
	if err != nil {
		return err
	}
	oldest := r.extreme(k8s.KindControlPlane, apiServer, false)
	if oldest == "" {
		return nil
	}
	api := r[oldest].apiServer
	for _, name := range slices.Sorted(maps.Keys(images)) {
		for _, f := range images[name] {
			img, ok, err := k8s.ImageOfPayload(f)
			if !ok {
				continue
			}
			if err != nil {
				return fmt.Errorf("%s: the image's image.json: %w", name, err)
			}
			v, err := imageref.ParseVersion(img.Version)
			if err != nil {
				return fmt.Errorf("%s: the image's kubelet: %w", name, err)
			}
			if v.Major != api.Major || v.Minors(api) > 0 {
				return fmt.Errorf("%s: the image's kubelet %s would be newer than the API server %s that %s runs, the oldest of the cluster; upgrade the control plane first: name the new release's images in the Cluster document and deliver it with chalkctl apply-config", name, v.Release(), api.Release(), oldest)
			}
		}
	}
	return nil
}

// BeforeDelivery waits for etcd to keep its quorum while a control plane's API server restarts.
func (p *Plugin) BeforeDelivery(ctx context.Context, u layer.Upgrading, name string, role layer.UpgradeRole) error {
	if !role.Coordinator {
		return nil
	}
	return quorum(ctx, u, p.rolloutFlagsOf(u).allowDowntime, name, "restarts its control plane")
}

// Delivered checks that a node of the cluster has its Node Ready and, on a control plane, that its
// static pods run the images its config names and are Ready. A node that is no part of the cluster
// yet, such as a control plane waiting for the bootstrap, has nothing to check.
func (p *Plugin) Delivered(ctx context.Context, conn *client.Conn) error {
	k, err := Status(ctx, conn)
	if err != nil {
		return layer.ServiceError(err, p.Name())
	}
	return deliveredOf(k)
}

// deliveredOf is Delivered of the node's Kubernetes status.
func deliveredOf(k *kubernetesv1.StatusResponse) error {
	if !inCluster(k) {
		return nil
	}
	if k.GetNodeReady() != "True" {
		return fmt.Errorf("its Node is not Ready: %s", k.GetNodeReady())
	}
	if k.GetKind() == k8s.KindControlPlane && !k.GetControlPlanePodsCurrent() {
		return errors.New("its control plane: " + k.GetControlPlanePods())
	}
	return nil
}

// Summary groups the nodes by the API server their running config names.
func (p *Plugin) Summary(all []layer.NodeVersions) []string {
	by := map[string][]string{}
	for _, n := range all {
		key := "unknown"
		if v, ok := n.Versions.(versions); ok && n.Err == nil {
			key = v.apiServer.String()
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
