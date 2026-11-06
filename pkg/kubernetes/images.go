package kubernetes

import (
	"errors"
	"fmt"

	"github.com/trevex/chalkos/pkg/imageref"
)

// Images are the images the cluster's components run from, as the Cluster document names them:
// the control plane's static pods, the add-ons chalkd applies and the pod sandbox. Flannel is empty
// without flannel.
type Images struct {
	Etcd                  string `json:"etcd"`
	KubeAPIServer         string `json:"kubeAPIServer"`
	KubeControllerManager string `json:"kubeControllerManager"`
	KubeScheduler         string `json:"kubeScheduler"`
	KubeProxy             string `json:"kubeProxy"`
	CoreDNS               string `json:"coredns"`
	Pause                 string `json:"pause"`
	Flannel               string `json:"flannel"`
}

// imageField is an image of Images with the name its config field has.
type imageField struct {
	field, ref string
	// versioned is set for the components whose tag names their Kubernetes version.
	versioned bool
}

// fields are the images of the Cluster document's images block, by their config names.
func (i Images) fields() []imageField {
	return []imageField{
		{"apiServer", i.KubeAPIServer, true},
		{"controllerManager", i.KubeControllerManager, true},
		{"scheduler", i.KubeScheduler, true},
		{"proxy", i.KubeProxy, true},
		{"etcd", i.Etcd, false},
		{"coredns", i.CoreDNS, false},
		{"pause", i.Pause, false},
	}
}

// validateImages checks that every image is a reference, that the Kubernetes components' tags
// name their versions, that the cluster has a flannel image exactly when it runs flannel, and the
// versions against each other and the kubelet's, with Kubernetes's version skew policy:
//   - the API server is no older than the kubelet, and at most three minors newer;
//   - the controller-manager and the scheduler are no newer than the API server, and at most one
//     minor older;
//   - kube-proxy is no newer than the API server, and at most three minors older.
//
// The policy also keeps kube-proxy within three minors of the kubelet, which the bounds above
// already ensure. The policy compares minor versions; patch releases and suffixes do not count.
func (c Cluster) validateImages() error {
	versions := map[string]imageref.Version{}
	for _, f := range c.Images.fields() {
		if f.ref == "" {
			return fmt.Errorf("images.%s: no image", f.field)
		}
		r, err := imageref.Parse(f.ref)
		if err != nil {
			return fmt.Errorf("images.%s: %w", f.field, err)
		}
		if !f.versioned {
			continue
		}
		v, err := r.Version()
		if err != nil {
			return fmt.Errorf("images.%s: %s: %w", f.field, f.ref, err)
		}
		versions[f.field] = v
	}
	switch {
	case c.Flannel != nil && c.Images.Flannel == "":
		return errors.New("images.flannel: no image")
	case c.Flannel == nil && c.Images.Flannel != "":
		return errors.New("images.flannel: an image, but the cluster runs no flannel")
	case c.Flannel != nil:
		if _, err := imageref.Parse(c.Images.Flannel); err != nil {
			return fmt.Errorf("images.flannel: %w", err)
		}
	}
	kubelet, err := imageref.ParseVersion(c.Version)
	if err != nil {
		return fmt.Errorf("the image's kubelet: %w", err)
	}
	if err := checkSkew(kubelet, versions); err != nil {
		return err
	}
	if api := versions["apiServer"].Release(); c.APIServerVersion != api {
		return fmt.Errorf("apiServerVersion: %q, but images.apiServer names %s", c.APIServerVersion, api)
	}
	return nil
}

// checkSkew checks the components' versions by their config names against each other and the
// kubelet's.
func checkSkew(kubelet imageref.Version, v map[string]imageref.Version) error {
	api := v["apiServer"]
	// In the order of the fields, so that the refusal names the same field every time.
	for _, field := range []string{"apiServer", "controllerManager", "scheduler", "proxy"} {
		if version := v[field]; version.Major != kubelet.Major {
			return fmt.Errorf("images.%s: %s is of Kubernetes %d, the image's kubelet %s of Kubernetes %d", field, version, version.Major, kubelet, kubelet.Major)
		}
	}
	// minors is how many minors a is newer than b; negative when it is older.
	minors := func(a, b imageref.Version) int { return a.Minor - b.Minor }
	switch d := minors(api, kubelet); {
	case d < 0:
		return fmt.Errorf("images.apiServer: %s is older than the image's kubelet %s; a kubelet must not be newer than the API server, so upgrade the control plane first", api, kubelet.MinorString())
	case d > 3:
		return fmt.Errorf("images.apiServer: %s is %d minors newer than the image's kubelet %s; a kubelet may be at most three minors older than the API server, so upgrade the nodes' images first", api, d, kubelet.MinorString())
	}
	for _, field := range []string{"controllerManager", "scheduler"} {
		switch d := minors(v[field], api); {
		case d > 0:
			return fmt.Errorf("images.%s: %s is newer than images.apiServer %s; it must not be newer than the API server", field, v[field], api)
		case d < -1:
			return fmt.Errorf("images.%s: %s is %d minors older than images.apiServer %s; it may be at most one minor older", field, v[field], -d, api)
		}
	}
	proxy := v["proxy"]
	switch d := minors(proxy, api); {
	case d > 0:
		return fmt.Errorf("images.proxy: %s is newer than images.apiServer %s; kube-proxy must not be newer than the API server", proxy, api)
	case d < -3:
		return fmt.Errorf("images.proxy: %s is %d minors older than images.apiServer %s; kube-proxy may be at most three minors older", proxy, -d, api)
	}
	return nil
}

// APIServerVersion is the Kubernetes version images.apiServer's tag names.
func (i Images) APIServerVersion() (imageref.Version, error) {
	r, err := imageref.Parse(i.KubeAPIServer)
	if err != nil {
		return imageref.Version{}, fmt.Errorf("images.apiServer: %w", err)
	}
	v, err := r.Version()
	if err != nil {
		return imageref.Version{}, fmt.Errorf("images.apiServer: %s: %w", i.KubeAPIServer, err)
	}
	return v, nil
}
