// Command chalkctl builds, signs, installs, and operates chalkos clusters.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"chalkos/internal/imagesign"
)

const usage = `usage: chalkctl <command> [flags]

commands:
  sign    sign the boot loader and UKIs of a chalkos disk image for Secure Boot`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "sign":
		err = runSign(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "chalkctl:", err)
		os.Exit(1)
	}
}

func runSign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	image := fs.String("image", "", "raw disk image to sign in place")
	repartJSON := fs.String("repart-json", "", "repart-output.json describing the image's partitions")
	key := fs.String("key", "", "PEM private key of the Secure Boot db signer")
	cert := fs.String("cert", "", "PEM certificate of the Secure Boot db signer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, f := range []struct{ name, value string }{
		{"--image", *image}, {"--repart-json", *repartJSON}, {"--key", *key}, {"--cert", *cert},
	} {
		if f.value == "" {
			return errors.New("sign: " + f.name + " is required")
		}
	}

	parts, err := imagesign.ReadPartitions(*repartJSON)
	if err != nil {
		return err
	}
	esp, err := imagesign.FindPartition(parts, "esp")
	if err != nil {
		return err
	}
	return imagesign.SignImage(context.Background(), *image, esp, *key, *cert)
}
