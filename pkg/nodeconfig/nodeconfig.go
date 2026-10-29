// Package nodeconfig applies the running image's config slot on the node, at boot in
// chalkos-config.service and after every ApplyConfig. It:
//   - writes the identity the config translates to as /run/chalkos/node.json, which the node's
//     Kubernetes side reads, and sets the hostname;
//   - writes the config's documents for the units granted config kinds;
//   - has the image's layers write what they derive from the documents, such as the Kubernetes
//     cluster file;
//   - at boot, renders the image's outputs, such as networkd's units and chrony's time servers.
package nodeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/uki"
)

// Layer writes what an image layer derives from the running slot's documents, such as the
// Kubernetes cluster file chalkd's Kubernetes side reads.
type Layer interface {
	// WriteConfig writes it from docs; nil for a slot without documents.
	WriteConfig(docs config.Documents) error
}

// Loader applies config slots. It holds where it reads the slots and the image's contracts, and
// where it writes the identity, documents, credentials and outputs. Tests point the paths at
// temporary directories.
type Loader struct {
	// Slots is the directory of the node's config slots on STATE, one per image, and Cmdline the
	// running kernel's command line, whose usrhash= names the running image and so its slot.
	Slots, Cmdline string
	// RunDir receives node.json, config/ and credentials/.
	RunDir string
	// Services is the image's map of units to the config kinds they are granted, and Effects
	// what its deliveries do to them.
	Services, Effects string
	// Schemas are the image's schemas, which tell the secretRef fields of its documents.
	Schemas     string
	SetHostname func(string) error
	// Layers are the image's layers, each of which writes what it derives from the documents
	// after them.
	Layers []Layer
	// Outputs is the image's outputs, which Render renders below its root. NodeCertificate is the
	// node certificate on STATE, whose subject is the node's name, which renderers read.
	Outputs         string
	Render          outputs.Renderer
	NodeCertificate string
}

// Default returns the Loader of the running node, with its real paths.
func Default() Loader {
	return Loader{
		RunDir:          "/run/chalkos",
		Services:        "/etc/chalkos/services.json",
		Effects:         "/etc/chalkos/effects.json",
		Slots:           "/state/config",
		Cmdline:         "/proc/cmdline",
		Schemas:         "/etc/chalkos/schemas",
		SetHostname:     func(name string) error { return syscall.Sethostname([]byte(name)) },
		Outputs:         outputs.DefaultPath,
		Render:          outputs.Default(),
		NodeCertificate: "/state/chalkd/node.pem",
	}
}

// identityFields holds the fields of the identity the loader needs.
type identityFields struct {
	Hostname string `json:"hostname"`
}

// Slot is the running image's config slot: the one below Slots named by the root hash of the store
// the kernel command line's usrhash= boots.
func (l Loader) Slot() (string, error) {
	hash, err := uki.RunningHash(l.Cmdline)
	if err != nil {
		return "", err
	}
	return slot.Dir(l.Slots, hash), nil
}

// Load applies the running image's config slot: the identity it holds, then its documents for
// the granted units, then the outputs rendered from them.
//
//   - A node without any slot, such as the installer, is not installed: Load applies nothing.
//   - A node whose slots all belong to other images, such as one booted into an image no install
//     or upgrade wrote config for, fails without applying any. Its health check then fails, so a
//     counted boot falls back to an image that has config.
func (l Loader) Load() error {
	slots, err := slot.List(l.Slots)
	if err != nil {
		return fmt.Errorf("list the config slots: %w", err)
	}
	if len(slots) == 0 {
		return nil
	}
	stored, err := l.readSlot()
	if err != nil {
		return err
	}
	if err := l.Apply(stored.Identity); err != nil {
		return err
	}
	if err := l.writeConfig(stored); err != nil {
		return err
	}
	return l.renderAtBoot(stored.Documents)
}

// readSlot reads the running image's slot, which must exist.
func (l Loader) readSlot() (slot.Stored, error) {
	dir, err := l.Slot()
	if err != nil {
		return slot.Stored{}, err
	}
	stored, found, err := slot.Read(dir)
	if err != nil {
		return slot.Stored{}, fmt.Errorf("read the config slot %s: %w", dir, err)
	}
	if !found {
		return slot.Stored{}, fmt.Errorf("STATE holds no config for the running image %s: no install or upgrade wrote config for it", filepath.Base(dir))
	}
	return stored, nil
}

// LoadConfig writes the running image's slot for the units that read it: its documents, Secrets
// excepted, below RunDir/config, and for each granted unit the values the secretRefs of its kinds
// name below RunDir/credentials/<unit>. The image's layers then write what they derive from the
// documents.
func (l Loader) LoadConfig() error {
	stored, err := l.readSlot()
	if err != nil {
		return err
	}
	return l.writeConfig(stored)
}

// writeConfig writes a slot's documents as LoadConfig does. It does not check them against the
// image's schemas again: the slot is the image's own, written only once they passed. A slot without
// documents, an identity alone, gets empty directories, so its units load their credentials all
// the same.
func (l Loader) writeConfig(stored slot.Stored) error {
	services, err := effects.ReadServices(l.Services)
	if err != nil {
		return err
	}
	// A unit of no kinds loads no credentials, so it gets no directory.
	kinds := map[string][]string{}
	for unit, c := range services {
		if len(c.Kinds) > 0 {
			kinds[unit] = c.Kinds
		}
	}
	found := len(stored.Documents) > 0
	var schemas *schema.Schemas
	if found {
		if schemas, err = schema.LoadSchemas(l.Schemas); err != nil {
			return fmt.Errorf("read the image's schemas: %w", err)
		}
	}
	if err := slot.WriteRuntime(l.RunDir, stored.Documents, schemas, kinds); err != nil {
		return err
	}
	var docs config.Documents
	if found {
		docs = stored.Documents
	}
	var errs []error
	for _, layer := range l.Layers {
		errs = append(errs, layer.WriteConfig(docs))
	}
	return errors.Join(errs...)
}

// Apply writes what the identity determines and sets the hostname.
func (l Loader) Apply(data []byte) error {
	var id identityFields
	if err := json.Unmarshal(data, &id); err != nil {
		return fmt.Errorf("parse the identity: %w", err)
	}
	if err := atomicfile.WriteFile(filepath.Join(l.RunDir, "node.json"), data, 0o644); err != nil {
		return err
	}
	if id.Hostname != "" {
		if err := l.SetHostname(id.Hostname); err != nil {
			return fmt.Errorf("set the hostname: %w", err)
		}
	}
	return nil
}

// RenderOutputs renders and checks every output of the image from docs, a config set with its
// defaults, for the node called name, and stages those that pass; schemas tell the secretRef
// fields of docs, the image's own when nil. It changes nothing a service reads.
func (l Loader) RenderOutputs(ctx context.Context, name string, docs []config.Document, schemas *schema.Schemas) (*outputs.Staged, []outputs.Failure, error) {
	defs, schemas, err := l.outputs(docs, schemas)
	if err != nil {
		return nil, nil, err
	}
	staged, failures := l.Render.Render(ctx, defs, outputs.Input{Node: name, Documents: docs, Schemas: schemas})
	return staged, failures, nil
}

// OutputStates are the states of the outputs the node serves now, which docs, the config they
// were rendered from, references Secrets of; schemas are as for RenderOutputs.
func (l Loader) OutputStates(docs []config.Document, schemas *schema.Schemas) (map[string]outputs.State, error) {
	defs, schemas, err := l.outputs(docs, schemas)
	if err != nil {
		return nil, err
	}
	states := map[string]outputs.State{}
	for name, d := range defs {
		if states[name], err = outputs.Live(d, outputs.Input{Documents: docs, Schemas: schemas}); err != nil {
			return nil, fmt.Errorf("output %s: %w", name, err)
		}
	}
	return states, nil
}

// outputs reads the image's outputs, and the image's schemas in place of nil ones when there are
// outputs and documents to render them from.
func (l Loader) outputs(docs []config.Document, schemas *schema.Schemas) (map[string]outputs.Definition, *schema.Schemas, error) {
	defs, err := outputs.Read(l.Outputs, l.Render.Root)
	if err != nil {
		return nil, nil, err
	}
	if schemas == nil && len(defs) > 0 && len(docs) > 0 {
		if schemas, err = schema.LoadSchemas(l.Schemas); err != nil {
			return nil, nil, fmt.Errorf("read the image's schemas: %w", err)
		}
	}
	return defs, schemas, nil
}

// renderAtBoot renders the outputs of the slot's documents and swaps in every one that passes,
// since /run starts empty: the renderers are pure, so the files are those the delivery checked.
// An output that fails fails the boot's config, which fails its health check; the status names
// it. On an image with outputs, a node certificate that cannot be read fails it too, since the
// outputs render for the node it names.
func (l Loader) renderAtBoot(docs []config.Document) error {
	if l.Outputs == "" {
		return nil
	}
	defs, err := outputs.Read(l.Outputs, l.Render.Root)
	if err != nil || len(defs) == 0 {
		return err
	}
	name, err := l.nodeName()
	if err != nil {
		return fmt.Errorf("the node's name for its outputs: %w", err)
	}
	staged, failures, err := l.RenderOutputs(context.Background(), name, docs, nil)
	if err != nil {
		return err
	}
	defer staged.Discard()
	errs := []error{staged.Swap()}
	if len(failures) > 0 {
		errs = append(errs, outputs.RecordFailures(l.Render.Root, failures), outputs.Join(failures))
	}
	return errors.Join(errs...)
}

// nodeName is the subject of the node certificate, the name chalkd renders outputs for.
func (l Loader) nodeName() (string, error) {
	data, err := os.ReadFile(l.NodeCertificate)
	if err != nil {
		return "", err
	}
	cert, err := pki.FirstCertificate(data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", l.NodeCertificate, err)
	}
	if cert.Subject.CommonName == "" {
		return "", fmt.Errorf("%s names no node", l.NodeCertificate)
	}
	return cert.Subject.CommonName, nil
}
