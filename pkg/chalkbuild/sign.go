package chalkbuild

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/imagesign"
)

func signCommand() *cobra.Command {
	var key, cert string
	cmd := &cobra.Command{
		Use:     "sign <image dir> <out dir>",
		Short:   "Sign a copy of an image directory for Secure Boot",
		Long:    signLong,
		Example: signExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			return sign(cmd.Context(), cmd.OutOrStdout(), key, cert, args)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&key, "key", "", "PEM private key of the Secure Boot db signer")
	fs.StringVar(&cert, "cert", "", "PEM certificate of the Secure Boot db signer, which the signed copy is checked against")
	return cmd
}

// sign signs a copy of the image directory args name and prints each disk image it signed.
func sign(ctx context.Context, stdout io.Writer, key, cert string, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: chalkbuild sign --key KEY --cert CERT <image dir> <out dir>")
	}
	if key == "" || cert == "" {
		return errors.New("chalkbuild sign needs --key and --cert")
	}
	// SignDirectory refuses when out's parent does not exist; an inventory's own layout need not
	// have made it yet, so chalkbuild makes it, as chalklab sign --out does.
	if err := os.MkdirAll(filepath.Dir(args[1]), 0o755); err != nil {
		return err
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
