package kubernetes

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/trevex/chalkos/pkg/imageref"
)

// CNI providers an image is built with.
const (
	CNIFlannel = "flannel"
	CNINone    = "none"
)

// Image is what a node's image fixes about Kubernetes, as /etc/chalkos/kubernetes/image.json
// records it: the kubelet's release and the pod network it ships. The images of the control plane,
// the add-ons and the pod sandbox come from the node's config.
type Image struct {
	// Version is the kubelet's release, such as 1.37.1.
	Version string `json:"version"`
	// CNI is the pod network the image ships: CNIFlannel or CNINone.
	CNI string `json:"cni"`
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

// Validate checks that the image names the kubelet's release and a known pod network.
func (img Image) Validate() error {
	if img.Version == "" {
		return errors.New("no Kubernetes version")
	}
	if _, err := imageref.ParseVersion(img.Version); err != nil {
		return fmt.Errorf("the kubelet's version: %w", err)
	}
	switch img.CNI {
	case CNIFlannel, CNINone:
	default:
		return fmt.Errorf("unknown CNI provider %q", img.CNI)
	}
	return nil
}
