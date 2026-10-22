package chalkbuild

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/ociimage"
)

func ociCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "oci <image dir> <layout dir>",
		Short:   "Write an image directory as an OCI image layout",
		Long:    ociLong,
		Example: ociExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			return writeOCI(cmd.Context(), cmd.OutOrStdout(), args)
		},
	}
}

// writeOCI writes the OCI image layout of the image directory args name and prints its
// manifest's digest.
func writeOCI(ctx context.Context, stdout io.Writer, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: chalkbuild oci <image dir> <layout dir>")
	}
	if err := os.MkdirAll(filepath.Dir(args[1]), 0o755); err != nil {
		return err
	}
	img, closeImage, err := ociimage.FromDirectory(ctx, args[0])
	if err != nil {
		return err
	}
	defer closeImage()
	digest, err := ociimage.WriteLayout(ctx, img, args[1])
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s, manifest %s\n", args[1], digest)
	return nil
}

func pushCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "push <layout dir> <reference>",
		Short:   "Push an OCI image layout to a registry",
		Long:    pushLong,
		Example: pushExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			return push(cmd.Context(), cmd.OutOrStdout(), args)
		},
	}
}

// push pushes the image of the layout args name to the reference they name and prints the
// pushed manifest's reference by digest.
func push(ctx context.Context, stdout io.Writer, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: chalkbuild push <layout dir> <reference>")
	}
	img, err := ociimage.ReadLayout(args[0])
	if err != nil {
		return err
	}
	ref, err := ociimage.Push(ctx, img, args[1])
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, ref)
	return nil
}
