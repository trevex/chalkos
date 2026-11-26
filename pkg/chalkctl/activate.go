package chalkctl

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/statusview"
)

type nodeActivateFlags struct {
	node  nodeFlags
	image string
}

func (a *app) nodeActivateCommand() *cobra.Command {
	f := nodeActivateFlags{node: nodeFlags{needs: "node activate"}}
	cmd := a.command(&cobra.Command{
		Use:         "activate <node>",
		Short:       "Make the image staged on a node boot next",
		Long:        nodeActivateLong,
		Example:     nodeActivateExample,
		Args:        cli.ExactArgs(1),
		Annotations: layer.Meta{Credentials: layer.Operator}.Annotations(),
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeActivate(ctx, f, pos[0]) })
	fs := cmd.Flags()
	f.node.RegisterClient(fs)
	fs.StringVar(&f.image, "image", "", "image to activate, as upgrade --image staged it: a raw image with repart-output.json next to it, or an image directory (default the node's image in the inventory)")
	return cmd
}

// nodeActivate makes the node boot its staged image next, naming it by the root hash of the
// inventory's image or --image's, so the node refuses an unexpected one.
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
		_, md, err := t.cluster.image(name)
		if err != nil {
			return err
		}
		if root, err = hex.DecodeString(md.RootHash); err != nil {
			return fmt.Errorf("the root hash %q of %s's image: %w", md.RootHash, name, err)
		}
		revision, tag = md.Revision, md.Tag
	}
	image := statusview.ImageName(root, revision, tag)
	var resp *nodev1.ActivateImageResponse
	if err := callNode(t, layer.Fetch(&resp, func(conn *client.Conn) (*nodev1.ActivateImageResponse, error) {
		return conn.ActivateImage(client.Unbounded(ctx), &nodev1.ActivateImageRequest{RootHash: root})
	})); err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, activatedLine(name, image, resp))
	if !resp.AlreadyInstalled {
		fmt.Fprintf(a.stdout, "%s: chalkctl node reboot %s boots it\n", name, name)
	}
	return nil
}

// imageRoot reads the root hash, revision and tag of the image at path, as upgrade --image takes
// it. Opening the image checks its hash tree against repart's root hash.
func imageRoot(ctx context.Context, path string, stderr io.Writer) (root []byte, revision, tag string, err error) {
	p, raw, _, err := openParts(path, false)
	if err != nil {
		return nil, "", "", err
	}
	defer p.Close()
	revision, tag = provenance(filepath.Dir(raw), p.Store.RootHash, stderr)
	return p.Store.RootHash, revision, tag, nil
}
