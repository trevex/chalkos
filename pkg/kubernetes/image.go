package kubernetes

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// CNI providers an image is built with.
const (
	CNIFlannel = "flannel"
	CNINone    = "none"
)

// Image is what a node's image fixes about Kubernetes, as /etc/chalkos/kubernetes/image.json
// records it: the release, the images its components run from, and the pod network it ships.
// Everything else about the cluster comes from the node's config.
type Image struct {
	// Version is the Kubernetes release, such as 1.37.1.
	Version string `json:"version"`
	// CNI is the pod network the image ships: CNIFlannel or CNINone.
	CNI    string `json:"cni"`
	Images Images `json:"images"`
}

// ReadImage reads and checks the image's Kubernetes facts.
func ReadImage(path string) (Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Image{}, err
	}
	img, err := ParseImage(data)
	if err != nil {
		return Image{}, fmt.Errorf("%s: %w", path, err)
	}
	return img, nil
}

// ParseImage parses and checks an image.json.
func ParseImage(data []byte) (Image, error) {
	var img Image
	if err := json.Unmarshal(data, &img); err != nil {
		return Image{}, fmt.Errorf("parse: %w", err)
	}
	return img, img.Validate()
}

// Validate checks that the image names a release, the images of the control plane, kube-proxy
// and CoreDNS, and a flannel image exactly when flannel is its pod network.
func (img Image) Validate() error {
	if img.Version == "" {
		return errors.New("no Kubernetes version")
	}
	for _, i := range []struct{ name, ref string }{
		{"etcd", img.Images.Etcd},
		{"kubeAPIServer", img.Images.KubeAPIServer},
		{"kubeControllerManager", img.Images.KubeControllerManager},
		{"kubeScheduler", img.Images.KubeScheduler},
		{"kubeProxy", img.Images.KubeProxy},
		{"coredns", img.Images.CoreDNS},
	} {
		if i.ref == "" {
			return fmt.Errorf("no %s image", i.name)
		}
	}
	switch img.CNI {
	case CNIFlannel:
		if img.Images.Flannel == "" {
			return errors.New("no flannel image, but its CNI provider is flannel")
		}
	case CNINone:
		if img.Images.Flannel != "" {
			return errors.New("a flannel image, but its CNI provider is none")
		}
	default:
		return fmt.Errorf("unknown CNI provider %q", img.CNI)
	}
	return nil
}
