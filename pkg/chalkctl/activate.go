package chalkctl

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/internal/cli"
)

type nodeActivateFlags struct {
	node nodeFlags
	// image is the value of --image: the image whose root hash to activate, as upgrade --image
	// names it.
	image string
}

func (a *app) nodeActivateCommand() *cobra.Command {
	f := nodeActivateFlags{node: nodeFlags{needs: "node activate"}}
	cmd := a.command(&cobra.Command{
		Use:     "activate <node>",
		Short:   "Make the image staged on a node boot next",
		Long:    nodeActivateLong,
		Example: nodeActivateExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeActivate(ctx, f, pos[0]) })
	fs := cmd.Flags()
	f.node.registerClient(fs)
	fs.StringVar(&f.image, "image", "", "image to activate, as upgrade --image staged it: a raw image with repart-output.json next to it, or an image directory (default the node's image in the inventory)")
	return cmd
}

// nodeActivate has the node activate its staged image, the one chalkctl upgrade --until stage
// staged: the image the inventory names for the node or the one --image names, by its root hash,
// so a staged image the operator did not expect is refused.
func (a *app) nodeActivate(ctx context.Context, f nodeActivateFlags, name string) error {
	if f.image != "" {
		// The image names the root hash, so the command needs no node's config.
		f.node.needs = ""
	}
	t, err := a.clientTarget(ctx, f.node, name)
	if err != nil {
		return err
	}
	var root []byte
	var revision, tag string
	if f.image != "" {
		if root, revision, tag, err = imageRoot(ctx, f.image, a.stderr); err != nil {
			return err
		}
	} else {
		nc, err := t.cluster.config(name)
		if err != nil {
			return err
		}
		md, err := nc.Resolve()
		if err != nil {
			return err
		}
		if root, err = hex.DecodeString(md.RootHash); err != nil {
			return fmt.Errorf("the root hash %q of %s's image: %w", md.RootHash, name, err)
		}
		revision, tag = md.Revision, md.Tag
	}
	image := imageName(root, revision, tag)
	var resp *nodev1.ActivateImageResponse
	if err := callNode(t, func(conn *client.Conn) error {
		res, err := conn.ActivateImage(ctx, connect.NewRequest(&nodev1.ActivateImageRequest{RootHash: root}))
		if err == nil {
			resp = res.Msg
		}
		return err
	}); err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, activatedLine(name, image, resp))
	if !resp.AlreadyInstalled {
		fmt.Fprintf(a.stdout, "%s: chalkctl node reboot %s boots it\n", name, name)
	}
	return nil
}

// imageRoot reads the root hash of the image at path, as upgrade --image takes it, and the
// revision and tag its metadata.json gives it. Opening the image checks its hash tree against the
// root hash repart reported.
func imageRoot(ctx context.Context, path string, stderr io.Writer) (root []byte, revision, tag string, err error) {
	raw, _, err := imageFiles(path)
	if err != nil {
		return nil, "", "", err
	}
	parts, err := image.ReadPartitions(filepath.Join(filepath.Dir(raw), "repart-output.json"))
	if err != nil {
		return nil, "", "", err
	}
	p, err := image.OpenParts(ctx, raw, parts, false)
	if err != nil {
		return nil, "", "", err
	}
	defer p.Close()
	revision, tag = provenance(filepath.Dir(raw), p.Store.RootHash, stderr)
	return p.Store.RootHash, revision, tag, nil
}
