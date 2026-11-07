// Package translate translates a node's config on an image with Kubernetes: it builds the cluster
// the Node, Cluster and Flannel documents give, adds the node's Kubernetes settings to its
// identity, and reads the Cluster's sandbox image and the Addon documents' objects. chalkd and
// chalkctl add its Translator to the target of an image whose facts include Kubernetes facts.
package translate

import (
	"fmt"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/kubernetes"
)

// API is the apiVersion of the Kubernetes layer's kinds.
const API = "kubernetes.chalkos.dev/v1alpha1"

// Target adds to t the translation of an image with the Kubernetes facts facts, which it claims;
// it leaves the facts of another layer unclaimed. Claimed facts whose image.json does not parse
// are an error.
func Target(facts *anypb.Any, t *config.Target) (claimed bool, err error) {
	img, ok, err := kubernetes.ImageOfPayload(facts)
	if !ok {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	t.Translators = append(t.Translators, Translator(img))
	return true, nil
}

// Translator is the translation of an image with the Kubernetes facts img: it builds the cluster
// the Node, Cluster and Flannel documents give, adds the node's Kubernetes settings from its Node
// document to the identity, the Kubernetes node called name, and refuses Addon documents that do
// not decode.
func Translator(img kubernetes.Image) config.Translator {
	return func(s *config.Set, name string, id *identity.Identity) error {
		// The node's addresses follow the families and nodeIP.validSubnets of the cluster the
		// documents give, which needs a Node and a Cluster document.
		c, err := Cluster(s.Documents(), img)
		if err != nil {
			return fmt.Errorf("the Kubernetes cluster the documents give: %w", err)
		}
		if err := kubelet(s, id, name, c); err != nil {
			return err
		}
		_, err = AddonObjects(s.Documents())
		return err
	}
}

// deref is the string s points to, or "" for nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
