package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// Request is what both flows install.
type Request struct {
	// Section is the identity's storage section.
	Section storage.Section
	// NodeCertificate, the node certificate followed by the node CA's, and NodeKey are what chalkd
	// serves once the node is installed; CA holds the OS CAs, which issue the client certificates
	// it accepts and the node CA. All are PEM.
	NodeCertificate, NodeKey, CA []byte
	// FallbackSecret is enrolled as the second keyslot of every encrypted volume.
	FallbackSecret string
	// Layers write the payloads of the image's layers, such as the Kubernetes share, to the STATE
	// mounted at stateDir, once the identity is written; chalkd checked them before the install.
	// An install from the installer takes them from its Check instead.
	Layers []func(stateDir string) error
	// Config is the node's config as the config slot of the image installed keeps it, with the
	// identity it translates to as delivered (JSON), which the slot keeps as it is. An install
	// from the installer takes it from its Check instead.
	Config slot.Stored
	// Images is the provenance record STATE starts with, as chalkd keeps it in
	// chalkd/images.json; nil writes none.
	Images []byte
}

// validate checks what both flows take before they change anything; the config only an install in
// place carries in the request.
func (r Request) validate() error {
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
	// Check has the image's own checker check the node's config once the image's store is in slot
	// A and its UKI on the ESP, before STATE holds anything of the node and before the target
	// boots. st is the store, mounted read-only through dm-verity, and system the image's NixOS
	// system in it, which the UKI boots. It returns the config slot the checker passed, which the
	// install writes in place of Request.Config, and the layers' payloads checked against its
	// identity, in place of Request.Layers. Its error refuses the install. Section is the storage
	// section the target is laid out by, which the checker's identity must give too.
	Check func(ctx context.Context, st Store, system string) (Checked, error)
}

// Store is the store of an image that does not run, as upgrade.Store reads it: by the image's own
// paths, its links followed within the image; Dir is where its /nix/store is on this machine.
type Store interface {
	ReadFile(name string) ([]byte, error)
	Resolve(name string) (string, error)
	Dir() string
}

// Checked is the config an image's checker passed for an install from the installer: the image's
// config slot, with the identity the checker gave, and the writers of the layers' payloads, which
// were checked against that identity.
type Checked struct {
	Config slot.Stored
	Layers []func(stateDir string) error
}
