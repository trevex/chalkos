package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
)

type installFlags struct {
	node                nodeCommand
	pin                 pinning
	image, passwordFile string
	wipeDisk, force     bool
}

func (a *app) installCommand() *cobra.Command {
	var f installFlags
	cmd := a.command(&cobra.Command{
		Use:     "install <node>",
		Short:   "Install a node that waits in maintenance mode",
		Long:    installLong,
		Example: installExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.install(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.register(fs)
	f.pin.register(fs)
	fs.StringVar(&f.image, "image", "", "image to install when the node runs the installer: a raw image with repart-output.json and repart.d next to it, or an image directory, which chalkctl sends (default the node's image in the inventory: from its directory, or from a registry, which the installer pulls)")
	fs.BoolVar(&f.wipeDisk, "wipe-disk", false, "let the installer replace whatever the target disk holds, including an installed node; without it, the installer continues an earlier install of the node or takes a disk on which blkid finds no signature, which counts as empty even when it holds data")
	fs.StringVar(&f.passwordFile, "password-file", "", "file holding the password of a node whose fallback is a password")
	fs.BoolVar(&f.force, "force", false, "install the node without asking the control planes whether its name is revoked")
	return cmd
}

func (a *app) install(ctx context.Context, f installFlags, pos []string) error {
	n, p := f.node, f.pin
	n.needs = "install"
	t, err := a.target(ctx, n, pos[0])
	if err != nil {
		return err
	}
	cert := t.creds.cert
	conn, err := p.dial(t.addr, t.source, cert)
	if err != nil {
		return err
	}
	if p.insecure {
		// An insecure connection's fingerprint vouches only for that connection. The secrets go
		// over a new one pinned to the fingerprint printed here.
		if _, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{})); err != nil {
			return maintenanceError(ctx, t, err)
		}
		fp := conn.Fingerprint()
		fmt.Fprintf(a.stderr, "chalkctl: the node's certificate fingerprint is %s\n", fp)
		if conn, err = client.Dial(t.addr, client.Options{Verify: client.ByFingerprint(fp), Certificate: cert, Source: t.source}); err != nil {
			return err
		}
	}
	info, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
	if err != nil {
		return maintenanceError(ctx, t, err)
	}
	if info.Msg.Mode != nodev1.Mode_MODE_MAINTENANCE {
		return fmt.Errorf("%s at %s is installed already", t.name, t.addr)
	}
	if !f.force {
		if err := installNotRevoked(ctx, t); err != nil {
			return err
		}
	}

	header := &nodev1.InstallHeader{CaCertificate: []byte(t.secrets.OSCABundle()), WipeDisk: f.wipeDisk}
	var img *diskImage
	var c contract
	if !info.Msg.Installer {
		// The node runs its image already, which must be of the node's platform.
		if err := t.cluster.samePlatform(t.name, info.Msg.Platform); err != nil {
			return err
		}
		if c, err = nodeContract(ctx, conn, t.name); err != nil {
			return err
		}
		header.Target = &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}
	} else {
		if img, c, err = a.installerImage(ctx, t, f.image); err != nil {
			return err
		}
		defer img.Close()
		if err := checkInstallImage(t, img); err != nil {
			return err
		}
		header.Image = img.header
		// The installer runs another image than the one it installs, so it gets that image's
		// contract with the config; an image it pulls brings its contract along.
		if img.source != nil {
			header.Origin = &nodev1.InstallHeader_Source{Source: img.source}
		} else {
			definitions, err := readDefinitions(img.definitions)
			if err != nil {
				return err
			}
			header.Origin = &nodev1.InstallHeader_Contract{Contract: &nodev1.ImageContract{
				SystemDefinitions: definitions, Schemas: c.files, KubernetesImage: c.kubernetesImage,
			}}
		}
	}
	d, err := t.cluster.deliveryFor(t.name, c)
	if err != nil {
		return err
	}
	header.Config = d.Text
	names, err := t.cluster.certNames(t.name)
	if err != nil {
		return err
	}
	nodeCert, err := pki.IssueNode(t.secrets.NodeCA, names, time.Now())
	if err != nil {
		return err
	}
	header.NodeCertificate, header.NodeKey = []byte(nodeCert.Certificate), []byte(nodeCert.Key)
	fallback := d.identity.Storage.Fallback
	if header.FallbackSecret, err = a.fallbackSecret(ctx, t, fallback, f.passwordFile, true); err != nil {
		return err
	}
	if header.KubernetesShare, err = kubernetesShare(t, time.Now()); err != nil {
		return err
	}
	if img == nil {
		fmt.Fprintf(a.stdout, "installing %s in place\n", t.name)
	} else {
		ref := d.identity.Storage.Disks[storage.SystemDisk].Ref
		header.Target = &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{
			Path: ref.Path, Model: ref.Selector.Model, Serial: ref.Selector.Serial,
			Wwn: ref.Selector.WWN, Size: ref.Selector.Size, Type: ref.Selector.Type,
		}}
		if src := img.source.GetOci().GetReference(); src != "" {
			fmt.Fprintf(a.stdout, "installing %s onto %s: %s %s, which the installer pulls from %s\n", t.name, ref, img.header.GetImageId(), img.name(), src)
		} else {
			fmt.Fprintf(a.stdout, "installing %s onto %s from %s: %s %s, sending %d bytes of store, hash tree, UKI and boot loader\n", t.name, ref, img.path, img.header.GetImageId(), img.name(), img.size())
		}
	}

	stream := conn.Install(ctx)
	if err := stream.Send(&nodev1.InstallRequest{Message: &nodev1.InstallRequest_Header{Header: header}}); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if img != nil && img.source == nil {
		parts, done, err := img.stream()
		if err != nil {
			return err
		}
		defer done()
		if err := sendChunks(parts, func(c *nodev1.ImageChunk) error {
			return stream.Send(&nodev1.InstallRequest{Message: &nodev1.InstallRequest_Chunk{Chunk: c}})
		}); err != nil {
			return err
		}
	}
	if _, err := stream.CloseAndReceive(); err != nil {
		return fmt.Errorf("install %s: %w", t.name, err)
	}
	fmt.Fprintf(a.stdout, "%s is installed and reboots\n", t.name)
	if fallback == "recovery-key" {
		fmt.Fprintf(a.stdout, "chalkctl recovery-key %s prints the key that unlocks it when its TPM fails\n", t.name)
	}
	return nil
}

// installerImage is the image an installer writes and its contract: the image the path names,
// or without one the node's image in the inventory. That image comes from its directory, which
// must hold the build its metadata.json names, or from a registry, which the installer pulls by
// the digest chalkctl resolved.
func (a *app) installerImage(ctx context.Context, t *target, path string) (*diskImage, contract, error) {
	fromInventory := path == ""
	if fromInventory {
		nc, err := t.cluster.inventoryImage(t.name)
		if err != nil {
			return nil, contract{}, err
		}
		if nc.ImageOCI != "" {
			img, err := ociImage(nc, true)
			if err != nil {
				return nil, contract{}, err
			}
			return img, *img.contract, nil
		}
		path = nc.ImageDir
	}
	img, err := openImage(ctx, path, true, a.stderr)
	if err != nil {
		return nil, contract{}, err
	}
	if fromInventory {
		if err := t.cluster.sameBuild(t.name, img); err != nil {
			img.Close()
			return nil, contract{}, err
		}
	}
	c, err := imageContract(filepath.Dir(img.definitions))
	if err != nil {
		img.Close()
		return nil, contract{}, err
	}
	c.platform = img.Info.Platform()
	return img, c, nil
}

// maintenanceError explains why a node in maintenance mode did not answer. A node in maintenance
// mode whose image carries an OS CA accepts clients of that OS CA alone; it refuses chalkctl's
// certificate when the image was built from a secrets.pub.json before the OS CA rotated.
func maintenanceError(ctx context.Context, t *target, err error) error {
	if client.RefusesCertificate(ctx, err, t.addr, t.creds.cert) {
		return fmt.Errorf("reach %s at %s: the maintenance image trusts another OS CA than the secrets file, so it refuses chalkctl's certificate; build the image and the installer media again from the current secrets.pub.json", t.name, t.addr)
	}
	return fmt.Errorf("reach %s at %s: %w", t.name, t.addr, err)
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
