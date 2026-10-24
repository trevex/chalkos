package chalkbuild

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/imagesign"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/ociimage"
)

func signCommand() *cobra.Command {
	var key, cert string
	cmd := &cobra.Command{
		Use:     "sign <image dir or layout> <out dir>",
		Short:   "Sign a copy of an image directory or an OCI image layout for Secure Boot",
		Long:    signLong,
		Example: signExample,
		Args:    cli.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sign(cmd.Context(), cmd.OutOrStdout(), key, cert, args)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&key, "key", "", "PEM private key of the Secure Boot db signer")
	fs.StringVar(&cert, "cert", "", "PEM certificate of the Secure Boot db signer, which the signed copy is checked against")
	cmd.MarkFlagRequired("key")
	cmd.MarkFlagRequired("cert")
	return cmd
}

// sign signs a copy of the image directory or OCI image layout args name: for a directory, it
// prints each disk image it signed; for a layout, the new manifest's digest.
func sign(ctx context.Context, stdout io.Writer, key, cert string, args []string) error {
	if key == "" || cert == "" {
		return cli.Usagef("sign needs --key and --cert")
	}
	// SignDirectory and SignLayout refuse when out's parent does not exist; an inventory's own
	// layout need not have made it yet, so chalkbuild makes it, as chalklab sign --out does.
	if err := os.MkdirAll(filepath.Dir(args[1]), 0o755); err != nil {
		return err
	}
	if ociimage.IsLayout(args[0]) {
		digest, err := imagesign.SignLayout(ctx, args[0], args[1], key, cert)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "signed %s, manifest %s\n", args[1], digest)
		return nil
	}
	signed, err := imagesign.SignDirectory(ctx, args[0], args[1], key, cert)
	if err != nil {
		return err
	}
	for _, name := range signed {
		fmt.Fprintf(stdout, "signed %s\n", filepath.Join(args[1], name))
	}
	return nil
}
