package kubernetes

import (
	"errors"
	"io/fs"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/ociimage"
)

// ImageOf is the Kubernetes part of an image's contract, read from a registry: the kubelet
// release and the pod network its image.json names. ok is false for an image without the
// Kubernetes layer.
func ImageOf(c ociimage.Contract) (img k8s.Image, ok bool, err error) {
	data, err := c.ReadFile(k8s.ContractFactsFile)
	if errors.Is(err, fs.ErrNotExist) {
		return k8s.Image{}, false, nil
	}
	if err != nil {
		return k8s.Image{}, false, err
	}
	img, err = k8s.ParseImage(data)
	return img, err == nil, err
}
