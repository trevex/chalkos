package chalkctl

import (
	"context"
	"encoding/hex"
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
)

func (a *app) nodeActivateCommand() *cobra.Command {
	f := nodeFlags{needs: "node activate"}
	cmd := a.command(&cobra.Command{
		Use:     "activate <node>",
		Short:   "Make the image staged on a node boot next",
		Long:    nodeActivateLong,
		Example: nodeActivateExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeActivate(ctx, f, pos[0]) })
	f.registerClient(cmd.Flags())
	return cmd
}

// nodeActivate has the node activate its staged image, the one chalkctl upgrade --until stage
// staged: the image the inventory names for the node, by its root hash, so a staged image the
// operator did not expect is refused.
func (a *app) nodeActivate(ctx context.Context, f nodeFlags, name string) error {
	t, err := a.clientTarget(ctx, f, name)
	if err != nil {
		return err
	}
	nc, err := t.cluster.config(name)
	if err != nil {
		return err
	}
	md, err := nc.Resolve()
	if err != nil {
		return err
	}
	root, err := hex.DecodeString(md.RootHash)
	if err != nil {
		return fmt.Errorf("the root hash %q of %s's image: %w", md.RootHash, name, err)
	}
	image := imageName(root, md.Revision, md.Tag)
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
