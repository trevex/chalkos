package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/sdk"
	"github.com/trevex/chalkos/pkg/storage"
)

type installFlags struct {
	node                nodeFlags
	pin                 pinFlags
	image, passwordFile string
	wipeDisk            bool
	ignoreRevoked       bool
}

func (a *app) installCommand() *cobra.Command {
	f := installFlags{node: nodeFlags{needs: "install"}}
	cmd := a.command(&cobra.Command{
		Use:         "install <node>",
		Short:       "Install a node that waits in maintenance mode",
		Long:        installLong,
		Example:     installExample,
		Args:        cli.ExactArgs(1),
		Annotations: layer.Meta{Credentials: layer.SecretsFile, Confirms: installConfirms}.Annotations(),
	}, func(a *app, ctx context.Context, pos []string) error { return a.install(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.Register(fs)
	f.pin.register(fs)
	fs.StringVar(&f.image, "image", "", "image to install when the node runs the installer: a raw image with repart-output.json and repart.d next to it, or an image directory, which chalkctl sends (default the node's image in the inventory: from its directory, or from a registry, which the installer pulls)")
	fs.BoolVar(&f.wipeDisk, "wipe-disk", false, "let the installer replace whatever the target disk holds, including an installed node; without it, the installer continues an earlier install of the node or takes a disk on which blkid finds no signature, which counts as empty even when it holds data")
	registerPasswordFile(fs, &f.passwordFile)
	fs.BoolVar(&f.ignoreRevoked, "ignore-revoked", false, "install the node without asking whether its name is revoked")
	return cmd
}

func (a *app) install(ctx context.Context, f installFlags, pos []string) error {
	p := f.pin
	t, err := a.target(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	cert := t.creds.cert
	if err := p.validate(); err != nil {
		return err
	}
	trust := sdk.Trust{Fingerprint: p.fingerprint, Insecure: p.insecure, Source: t.source}
	conn, info, fp, err := sdk.DialMaintenance(ctx, t.addr, trust, cert)
	if p.insecure && fp != "" {
		// An insecure connection's fingerprint vouches only for the connection sdk.DialMaintenance
		// pinned it to. The secrets go over that one alone; print it whether or not the node
		// turns out installed already.
		fmt.Fprintf(a.stderr, "chalkctl: the node's certificate fingerprint is %s\n", fp)
	}
	switch {
	case errors.Is(err, sdk.ErrInstalled):
		// Warn of the installed node's release, as for any node that answers.
		t.nodes.release.check(t.name, info.GetChalkosVersion())
		return fmt.Errorf("%s at %s is installed already", t.name, t.addr)
	case errors.Is(err, sdk.ErrOSCARefused):
		return fmt.Errorf("reach %s at %s: the maintenance image trusts another OS CA than the secrets file, so it refuses chalkctl's certificate; build the image and the installer media again from the current secrets.pub.json", t.name, t.addr)
	case err != nil:
		return fmt.Errorf("reach %s at %s: %w", t.name, t.addr, err)
	}
	defer conn.Close()
	t.nodes.release.check(t.name, info.GetChalkosVersion())
	var before refreshes
	if f.ignoreRevoked {
		before = revokedBefore(ctx, t.nodes, []string{t.name}, true)
	} else {
		for _, d := range compiled[layer.Delivery]() {
			if err := d.AdmitInstall(ctx, t); err != nil {
				return err
			}
		}
	}

	var img *diskImage
	var c sdk.Contract
	var definitions map[string]string
	if !info.Installer {
		// The node runs its image already, which must be of the node's platform.
		if err := t.cluster.samePlatform(t.name, info.Platform); err != nil {
			return err
		}
		if c, err = sdk.NodeContract(ctx, conn, sdkLayers(), t.name); err != nil {
			return err
		}
	} else {
		if img, err = a.installerImage(ctx, t, f.image); err != nil {
			return err
		}
		defer img.Close()
		c = *img.contract
		if err := checkInstallImage(t, img); err != nil {
			return err
		}
		// The installer runs another image than the one it installs, so it gets that image's
		// contract with the config; an image it pulls brings its contract along.
		if img.source == nil {
			if definitions, err = readDefinitions(img.definitions); err != nil {
				return err
			}
		}
	}
	d, docs, err := t.cluster.deliveryFor(ctx, t.name, c)
	if err != nil {
		return err
	}
	a.warnUnpinned(t.name, d)
	r := sdk.InstallRequest{Node: t.name, Documents: docs, Secrets: t.secrets, Layers: sdkLayers()}
	fallback := d.Identity.Storage.Fallback
	if fallback == storage.FallbackPassword {
		if r.Password, err = a.password(ctx, t, f.passwordFile); err != nil {
			return err
		}
	}
	if img == nil {
		// sdk.Install reads the node's contract and checks the config once more; chalkctl checked
		// it above to warn of unpinned images and learn whether the storage needs a password.
		fmt.Fprintf(a.stdout, "installing %s in place\n", t.name)
		err = sdk.Install(ctx, conn, r)
	} else {
		err = a.installOnto(ctx, conn, t, f.wipeDisk, r, d, img, definitions)
	}
	if err == nil {
		fmt.Fprintf(a.stdout, "%s is installed and reboots\n", t.name)
		if fallback == storage.FallbackRecoveryKey {
			fmt.Fprintf(a.stdout, "chalkctl recovery-key %s prints the key that unlocks it when its TPM fails\n", t.name)
		}
	}
	if f.ignoreRevoked {
		// The secrets may have reached the node although its install failed.
		err = errors.Join(err, a.refreshAndRecord(ctx, f.node.Secrets, t.nodes, before, t.secrets.Unrefreshed))
	}
	return err
}

// installOnto has the installer that conn reaches install img onto the node's system disk with
// the request r and the checked config d: from the registry the image's source names, or from
// img's files, which it sends with its contract's repart definitions, definitions.
func (a *app) installOnto(ctx context.Context, conn *client.Conn, t *target, wipeDisk bool, r sdk.InstallRequest, d sdk.Delivery, img *diskImage, definitions map[string]string) error {
	header, err := sdk.NewInstallHeader(r, d)
	if err != nil {
		return err
	}
	header.WipeDisk = wipeDisk
	header.Image = img.header
	if img.source != nil {
		header.Origin = &nodev1.InstallHeader_Source{Source: img.source}
	} else {
		header.Origin = &nodev1.InstallHeader_Contract{Contract: &nodev1.ImageContract{
			SystemDefinitions: definitions, Schemas: img.contract.Image.Schemas.Files(), LayerFacts: img.contract.Facts,
		}}
	}
	ref := d.Identity.Storage.Disks[storage.SystemDisk].Ref
	header.Target = &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{
		Path: ref.Path, Model: ref.Selector.Model, Serial: ref.Selector.Serial,
		Wwn: ref.Selector.WWN, Size: ref.Selector.Size, Type: ref.Selector.Type,
	}}
	if src := img.source.GetOci().GetReference(); src != "" {
		fmt.Fprintf(a.stdout, "installing %s onto %s: %s %s, which the installer pulls from %s\n", t.name, ref, img.header.GetImageId(), img.name(), src)
	} else {
		fmt.Fprintf(a.stdout, "installing %s onto %s from %s: %s %s, sending %s of store, hash tree, UKI and boot loader\n", t.name, ref, img.path, img.header.GetImageId(), img.name(), binarySize(img.size()))
	}
	if wipeDisk {
		if err := a.confirm(ctx, fmt.Sprintf("with --wipe-disk, the installer replaces whatever the disk %s of the machine at %s holds, an installed node included", ref, t.addr), t.name); err != nil {
			return err
		}
	}
	stream, err := conn.Install(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&nodev1.InstallRequest{Message: &nodev1.InstallRequest_Header{Header: header}}); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if img.source == nil {
		parts, done, err := img.stream()
		if err != nil {
			return err
		}
		defer done()
		if err := sendChunks(parts, func(c *nodev1.ImageChunk) error {
			return stream.Send(&nodev1.InstallRequest{Message: &nodev1.InstallRequest_Chunk{Chunk: c}})
		}, a.sendProgress(func(format string, args ...any) { fmt.Fprintf(a.stdout, format+"\n", args...) }, t.name, img.size())); err != nil {
			return err
		}
	}
	if _, err := stream.CloseAndReceive(); err != nil {
		return fmt.Errorf("install %s: %w", t.name, err)
	}
	return nil
}

// installerImage opens path, or else the node's image in the inventory: its directory, which must
// hold the build its metadata.json names, or its OCI reference, which the installer pulls by the
// digest chalkctl resolved.
func (a *app) installerImage(ctx context.Context, t *target, path string) (*diskImage, error) {
	fromInventory := path == ""
	if fromInventory {
		nc, err := t.cluster.inventoryImage(t.name)
		if err != nil {
			return nil, err
		}
		if nc.ImageOCI != "" {
			return ociImage(nc, true)
		}
		path = nc.ImageDir
	}
	img, err := openImage(ctx, path, true, a.stderr)
	if err != nil {
		return nil, err
	}
	if fromInventory {
		if err := t.cluster.sameBuild(t.name, img); err != nil {
			img.Close()
			return nil, err
		}
	}
	return img, nil
}

// readDefinitions reads an image's repart.d files.
func readDefinitions(dir string) (map[string]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.conf"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no repart definitions in %s; the image must come with its repart.d", dir)
	}
	defs := map[string]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		defs[filepath.Base(p)] = string(data)
	}
	return defs, nil
}
