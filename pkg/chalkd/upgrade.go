package chalkd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"path/filepath"
	"runtime"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// Upgrade installs the image an operator streams with the node's config for it. It checks that
// the image is of the node's platform, whatever its variant, that it boots the store it carries
// and, before it becomes the next boot, that the schemas in the image accept the config, but
// cannot tell an image to trust from another: without Secure Boot, an operator can run any image
// as root, with STATE and VAR unsealed, as their keys are sealed to PCR 7 alone. Secure Boot is
// what limits upgrades to images signed for db.
//
// An operator may upgrade, but only an admin may deliver config with the upgrade, as ApplyConfig
// needs: config names the node's Addons, networks and volumes, and what an operator may not
// deliver to the running image it must not deliver to the next one either. Without config the
// node carries the running image's over.
//
// An image the header names in a registry is pulled by the digest it names, with the credentials
// of the config's Registry of its host, and checked against the header as a streamed one.
func (s *Server) Upgrade(ctx context.Context, stream *connect.ClientStream[nodev1.UpgradeRequest]) (*connect.Response[nodev1.UpgradeResponse], error) {
	if !s.upgrading.TryLock() {
		return nil, failed(connect.CodeAborted, "an upgrade or a config delivery is running")
	}
	defer s.upgrading.Unlock()
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return nil, err
		}
		return nil, failed(connect.CodeInvalidArgument, "the upgrade request has no header")
	}
	h := stream.Msg().GetHeader()
	if h == nil {
		return nil, failed(connect.CodeInvalidArgument, "the first upgrade message must be the header")
	}
	if role, _ := ctx.Value(roleKey{}).(string); (h.GetConfig() != "" || h.GetFallbackSecret() != "") && !pki.Allows(role, pki.RoleAdmin) {
		return nil, failed(connect.CodePermissionDenied, "%s with config or a fallback secret needs the %s role; without them the node carries its own config over", stream.Spec().Procedure, pki.RoleAdmin)
	}
	img := h.GetImage()
	switch {
	case img == nil:
		return nil, failed(connect.CodeInvalidArgument, "the header names no image")
	case img.GetBootLoader() != nil:
		return nil, failed(connect.CodeInvalidArgument, "an upgrade carries no boot loader: it leaves the node's as it is")
	}
	header := imageHeader(img)
	parts := chunks(stream)
	if src := h.GetSource(); src != nil {
		ref := src.GetOci().GetReference()
		if ref == "" {
			return nil, failed(connect.CodeInvalidArgument, "the header's source names no image in a registry")
		}
		// The parts come from the registry.
		if err := noChunks(stream, "an upgrade"); err != nil {
			return nil, err
		}
		pull, err := s.pullParts(ctx, ref, header, h.GetConfig())
		if err != nil {
			code := connect.CodeInvalidArgument
			var ce *connect.Error
			if errors.As(err, &ce) {
				code = ce.Code()
			}
			return nil, failed(code, "upgrade: %v", plain(err))
		}
		defer pull.Close()
		parts = pull
		log.Printf("upgrading to %q with the root hash %x, pulled from %s", img.GetImageId(), img.GetRootHash(), ref)
	} else {
		log.Printf("upgrading to %q with the root hash %x", img.GetImageId(), img.GetRootHash())
	}
	res, err := s.installImage(ctx, header, h.GetConfig(), h.GetFallbackSecret(), parts)
	if err != nil {
		log.Printf("upgrade to %x failed: %v", img.GetRootHash(), err)
		// A refusal retired the image, so its config slot goes too.
		s.recordImages(nil)
		// An image staged already is sent again for its config alone, and a refusal of that config
		// in the recheck retires the image with it; apply-config --staged would have refused it and
		// kept the image. A recheck that failed to mount or read the store retired the image too,
		// but refused no config, so the answer names no apply-config --staged. Other failures, also
		// of a resend, came before the recheck or wrote the image anew, and name no staged image.
		var retired *upgrade.StagedRetiredError
		if errors.As(err, &retired) {
			var check *upgrade.CheckError
			if !errors.As(retired.Err, &check) {
				return nil, failed(connect.CodeFailedPrecondition, "upgrade: %v; the image staged to boot next was retired with it", err)
			}
			return nil, failed(connect.CodeFailedPrecondition, "upgrade: %v; the image staged to boot next was retired with it: chalkctl apply-config --staged %s changes a staged image's config without retiring the image on a refusal", err, s.nodeName())
		}
		return nil, failed(connect.CodeFailedPrecondition, "upgrade: %v", err)
	}
	s.recordImages(&header)
	resp := &nodev1.UpgradeResponse{AlreadyInstalled: res.AlreadyInstalled, AlreadyStaged: res.AlreadyStaged, Entry: res.Entry}
	if res.AlreadyInstalled {
		log.Printf("%x runs already", img.GetRootHash())
		return connect.NewResponse(resp), nil
	}
	if res.AlreadyStaged {
		log.Printf("%x boots next already, as %s; its config is replaced", img.GetRootHash(), res.Entry)
	}
	if h.Reboot {
		log.Printf("rebooting into %x", img.GetRootHash())
		resp.Rebooting = true
		s.rebootSoon()
	}
	return connect.NewResponse(resp), nil
}

// Where an image holds the schemas of its kinds and its Kubernetes facts, below its system.
const (
	imageSchemas = "/etc/chalkos/schemas"
	imageFacts   = "/etc/chalkos/kubernetes/image.json"
)

// imageStore is the store of an image an upgrade wrote, read as upgrade.Store reads it: by the
// image's own paths.
type imageStore interface {
	ReadFile(name string) ([]byte, error)
	ReadDir(name string) ([]fs.DirEntry, error)
}

// imageContract reads what an image translates config with from its store: its schemas and
// image.json, whose content the image's root hash covers. system is the image's NixOS system in
// st, and platform is the image's.
func imageContract(st imageStore, system, platform string) (*schema.Schemas, config.Target, error) {
	entries, err := st.ReadDir(system + imageSchemas)
	if err != nil {
		return nil, config.Target{}, fmt.Errorf("the image's schemas: %w", err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if files[e.Name()], err = st.ReadFile(system + imageSchemas + "/" + e.Name()); err != nil {
			return nil, config.Target{}, fmt.Errorf("the image's schemas: %w", err)
		}
	}
	schemas, err := schema.LoadFiles(files)
	if err != nil {
		return nil, config.Target{}, fmt.Errorf("the image's schemas: %w", err)
	}
	facts, err := st.ReadFile(system + imageFacts)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, config.Target{}, fmt.Errorf("the image's Kubernetes facts: %w", err)
	}
	t, err := imageTarget(platform, facts)
	return schemas, t, err
}

// stageConfig writes the config slot of the image an upgrade installs, whose store is st with the
// root hash, through writeSlot. The config is the one text delivers, or else the running slot's
// documents as they were delivered. secret is the fallback secret a new encrypted volume is
// enrolled with. It runs once the store is written to the inactive slot, before the image becomes
// the next boot, and again for an image that boots next already.
func (s *Server) stageConfig(ctx context.Context, st imageStore, system, platform string, rootHash []byte, text, secret string) error {
	// ApplyConfig writes the slots and changes the machine under the same lock.
	s.mu.Lock()
	defer s.mu.Unlock()
	_, running, found, err := s.runningSlot()
	if err != nil {
		return plain(err)
	}
	docs := running.Delivered
	if text != "" {
		if docs, err = config.Parse(strings.NewReader(text)); err != nil {
			return fmt.Errorf("parse the config: %w", err)
		}
	} else if !found || len(docs) == 0 {
		return errors.New("the node holds no config to carry over to the image; upgrade with the config from an inventory, which needs the admin role")
	}
	if err := s.writeSlot(ctx, st, system, platform, rootHash, docs, secret); err != nil {
		if text == "" {
			return fmt.Errorf("the node's config does not suit the image: %w; upgrade with config the image accepts, from an inventory, which needs the admin role", plain(err))
		}
		return plain(err)
	}
	return nil
}

// writeSlot writes docs, config as delivered, to the config slot of an image that does not run,
// whose store is st with the root hash. system is the image's NixOS system in st, platform its
// platform and secret the fallback secret. The caller holds s.mu.
//
// The config is checked and translated against the schemas and image.json in st, exactly as the
// image translates it at boot, and against the machine as every slot write is. A config
// chalkos-config could not apply would leave boot counting to fall back. writeSlot refuses:
//   - config the image's rules refuse, such as one that changes what a node holding a share was
//     set up for;
//   - storage changes that take a reset or a reinstall.
//
// Other storage changes, such as a new volume, are made now and recorded in storage.json. Every
// boot sets up the volumes storage.json records, so the running image mounts such a volume too
// although its config does not name it; its slot counts as removing the volume only once it is
// written again.
func (s *Server) writeSlot(ctx context.Context, st imageStore, system, platform string, rootHash []byte, docs []config.Document, secret string) error {
	_, running, _, err := s.runningSlot()
	if err != nil {
		return err
	}
	name, err := s.name()
	if err != nil {
		return err
	}
	schemas, t, err := imageContract(st, system, platform)
	if err != nil {
		return failed(connect.CodeInvalidArgument, "%v", err)
	}
	next, err := translateSlot(name, docs, schemas, t)
	if err != nil {
		return failed(connect.CodeInvalidArgument, "the config does not suit the image: %v", err)
	}
	d, err := parseIdentity(string(next.Identity))
	if err != nil {
		return err
	}
	// The rules are the image's, which the config is for; the facts the running image's, which
	// read this node. The running slot is the baseline, as for ApplyConfig.
	set, err := compileImageRules(schemas)
	if err != nil {
		return err
	}
	f, err := s.factsFor(ctx, next.Documents, running.Documents, secret)
	if err != nil {
		return err
	}
	if err := s.checkRules(set, name, next.Documents, running.Documents, f); err != nil {
		return s.elsewhere(err, d.section, rootHash)
	}
	mc, err := s.machineChanges(d.section)
	if err != nil {
		return err
	}
	if err := s.applyMachine(ctx, mc, secret); err != nil {
		return err
	}
	if err := slot.Write(slot.Dir(s.Loader.Slots, rootHash), next); err != nil {
		return failed(connect.CodeInternal, "store the image's config: %v", err)
	}
	log.Printf("the config %s of %x is stored", next.Version, rootHash)
	return nil
}

// plain is err without the code a connect error carries, for an upgrade's error, which has its own.
func plain(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return errors.New(ce.Message())
	}
	return err
}

// imageHeader is the image an install or upgrade header describes.
func imageHeader(h *nodev1.ImageHeader) upgrade.Header {
	return upgrade.Header{
		ImageID: h.GetImageId(), Platform: h.GetPlatform(), Architecture: h.GetArchitecture(), RootHash: h.GetRootHash(),
		StoreSize: int64(h.GetStore().GetSize()), VeritySize: int64(h.GetHashTree().GetSize()), UKISize: int64(h.GetUki().GetSize()),
		StoreSHA256: h.GetStore().GetSha256(), VeritySHA256: h.GetHashTree().GetSha256(), UKISHA256: h.GetUki().GetSha256(),
		BootLoaderSize: int64(h.GetBootLoader().GetSize()), BootLoaderSHA256: h.GetBootLoader().GetSha256(),
		Provenance: upgrade.Provenance{Revision: h.GetRevision(), Tag: h.GetTag()},
	}
}

// recordImages forgets every image no UKI on the ESP boots any more: the record of installed
// images keeps the others alone, and the config slots of the others and of the running image
// alone, so a retired image's slot goes in the same step as its provenance. installed, when set,
// is the image an upgrade installed or found installed, whose provenance the record keeps. Both
// only shrink and a boot loads its own image's slot alone, so failing to forget fails no upgrade;
// the next one forgets again.
func (s *Server) recordImages(installed *upgrade.Header) {
	entries, err := esp.Entries(s.Paths.ESP)
	if err != nil {
		log.Printf("forget the images the ESP no longer boots: %v", err)
		return
	}
	var keep [][]byte
	for _, e := range entries {
		if e.RootHash != nil {
			keep = append(keep, e.RootHash)
		}
	}
	running, err := uki.RunningHash(s.Paths.Cmdline)
	if err != nil {
		log.Printf("keep the running image's config: %v", err)
		return
	}
	s.mu.Lock()
	err = slot.Retire(s.Loader.Slots, append(keep, running))
	s.mu.Unlock()
	if err != nil {
		log.Printf("retire the config slots of images the ESP no longer boots: %v", err)
	}
	path := filepath.Join(s.Paths.StateDir, upgrade.ImagesRecord)
	images := upgrade.ReadImages(path)
	if installed != nil {
		images = images.Record(installed.RootHash, installed.Provenance, keep)
	} else {
		images = images.Keep(keep)
	}
	data, err := images.Encode()
	if err == nil {
		err = atomicfile.WriteFile(path, data, 0o644)
	}
	if err != nil {
		log.Printf("record the image's provenance: %v", err)
	}
}

// installImage installs an upgrade's image on the node's boot disk, with the config text delivers
// for it and the fallback secret new encrypted volumes are enrolled with.
func (s *Server) installImage(ctx context.Context, h upgrade.Header, text, secret string, image io.Reader) (upgrade.Result, error) {
	if s.InstallImage != nil {
		return s.InstallImage(ctx, h, text, secret, image)
	}
	n, err := s.upgradeNode()
	if err != nil {
		return upgrade.Result{}, err
	}
	n.CheckStore = func(st upgrade.Store) error {
		return s.stageConfig(ctx, st, st.Toplevel, h.Platform, h.RootHash, text, secret)
	}
	return n.Install(ctx, h, image)
}

// upgradeNode is the node an upgrade changes. It reads and changes the boot disk's partition
// table under s.mu, which ApplyConfig and ResetVolume change the boot disk's partitions under.
func (s *Server) upgradeNode() (*upgrade.Node, error) {
	n, err := s.upgradeNodeLocked()
	if err != nil {
		return nil, err
	}
	n.TableLock = &s.mu
	return n, nil
}

// upgradeNodeLocked is upgradeNode for a caller that holds s.mu.
func (s *Server) upgradeNodeLocked() (*upgrade.Node, error) {
	boot, err := s.Host.ResolvePath(s.Paths.BootDisk)
	if err != nil {
		return nil, fmt.Errorf("find the disk the node booted from: %w", err)
	}
	return &upgrade.Node{
		Run:           s.Run,
		Disk:          boot.Device,
		ESP:           s.Paths.ESP,
		EFIVars:       s.Paths.EFIVars,
		Cmdline:       s.Paths.Cmdline,
		OSRelease:     readOSRelease(s.Paths.OSRelease),
		Architecture:  uki.GoArchitecture(runtime.GOARCH),
		OpenPartition: storeslot.OpenPartition,
	}, nil
}
