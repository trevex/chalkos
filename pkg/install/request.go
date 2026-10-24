package install

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/identity"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// Request is what both flows install.
type Request struct {
	// Section is the identity's storage section.
	Section storage.Section
	// Kubernetes is the identity's Kubernetes section; nil on a node whose image has no Kubernetes.
	Kubernetes *identity.Kubernetes
	// NodeCertificate, the node certificate followed by the node CA's, and NodeKey are what chalkd
	// serves once the node is installed; CA holds the OS CAs, which issue the client certificates
	// it accepts and the node CA. All are PEM.
	NodeCertificate, NodeKey, CA []byte
	// FallbackSecret is enrolled as the second keyslot of every encrypted volume.
	FallbackSecret string
	// KubernetesShare is the node's share of the Kubernetes secrets (JSON); empty on a node whose
	// image has no Kubernetes.
	KubernetesShare []byte
	// Config is the node's config as the config slot of the image installed keeps it, with the
	// identity it translates to as delivered (JSON), which the slot keeps as it is.
	Config slot.Stored
	// RootHash is the root hash of the running image's store, whose slot an install in place
	// writes; an install from parts writes the slot of the image it installs.
	RootHash []byte
	// Images is the provenance record STATE starts with, as chalkd keeps it in
	// chalkd/images.json; nil writes none.
	Images []byte
}

func (r Request) validate() error {
	if len(r.Config.Identity) == 0 {
		return errors.New("no identity")
	}
	if _, ok := r.Section.Disks[storage.SystemDisk]; !ok {
		return errors.New("the identity's storage section has no system disk")
	}
	if err := r.Section.Validate(); err != nil {
		return err
	}
	switch r.Section.Encryption {
	case storage.EncryptionTPM2, storage.EncryptionNone:
	default:
		return fmt.Errorf("the identity's storage section has the unknown encryption policy %q", r.Section.Encryption)
	}
	if r.Section.Fallback != storage.FallbackNone && r.FallbackSecret == "" {
		return fmt.Errorf("the node's fallback is %s, but no fallback secret was sent", r.Section.Fallback)
	}
	if _, err := pki.ValidateOSCABundle(string(r.CA)); err != nil {
		return fmt.Errorf("CA certificate: %w", err)
	}
	// A node whose certificate does not chain through the node CA to the OS CA could not be
	// reached once installed. The node's clock may not be set yet, so dates are not checked.
	if _, err := r.nodeFile(); err != nil {
		return err
	}
	if len(r.KubernetesShare) > 0 {
		share, err := kpki.ParseShare(r.KubernetesShare)
		if err != nil {
			return err
		}
		var nodeName string
		if r.Kubernetes != nil {
			nodeName = r.Kubernetes.NodeName
		}
		// A worker share names the node its kubelet certificate is for; a node must never run a
		// kubelet certificate issued for another node.
		if err := share.ValidateFor(nodeName); err != nil {
			return err
		}
		if share.NodeCA != nil {
			if err := pki.ValidateNodeCA(*share.NodeCA, string(r.CA)); err != nil {
				return fmt.Errorf("the share's node CA: %w", err)
			}
		}
	}
	return nil
}

// nodeFile is the file chalkd loads its node certificate from.
func (r Request) nodeFile() ([]byte, error) {
	cred, err := pki.VerifyNode(string(r.NodeCertificate), string(r.NodeKey), string(r.CA), time.Time{})
	if err != nil {
		return nil, err
	}
	return pki.NodeFile(cred, string(r.CA))
}

// PartsRequest installs from the installer: the node's image's parts are streamed and written to
// a target disk, which is laid out from the image's definitions.
type PartsRequest struct {
	Request
	// Target is the disk the node is installed onto.
	Target storage.Ref
	// Image describes the node's image, whose store, hash tree, UKI and boot loader follow in
	// Parts, in this order.
	Image upgrade.Header
	Parts io.Reader
	// SystemDefinitions are the node's image's repart definitions of the system region, by
	// file name. The installer's own may differ: each image chooses its partition sizes.
	SystemDefinitions map[string]string
	// WipeDisk allows replacing whatever the target holds, including an installed node.
	WipeDisk bool
}
