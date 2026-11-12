package chalkctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/internal/poll"
	"github.com/trevex/chalkos/pkg/secureboot"
)

func (a *app) securebootNewCommand() *cobra.Command {
	var name string
	cmd := a.command(&cobra.Command{
		Use:     "new <dir>",
		Short:   "Create Secure Boot keys: PK, KEK and db",
		Long:    securebootNewLong,
		Example: securebootNewExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.securebootNew(pos[0], name) })
	cmd.Flags().StringVar(&name, "name", "", "name the certificates' common names begin with (default the directory's base name)")
	return cmd
}

func (a *app) securebootNew(dir, name string) error {
	if name == "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		name = filepath.Base(abs)
	}
	if err := secureboot.NewKeys(dir, name); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "wrote Secure Boot keys to %s: PK, KEK and db, each a .key and a .crt, and owner.txt\n", dir)
	fmt.Fprintf(a.stdout, "sign images with: chalkbuild sign --key %s --cert %s <in> <out>\n", filepath.Join(dir, "db.key"), filepath.Join(dir, "db.crt"))
	fmt.Fprintf(a.stdout, "keep %s offline and backed up: whoever holds PK.key or KEK.key controls which images this firmware boots\n", dir)
	return nil
}

type securebootEnrollFlags struct {
	secrets          layer.SecretsFlags
	config, address  string
	pin              pinFlags
	keys             string
	microsoft, force bool
	optionROMs       bool
	dbHashes         []string
	timeout          time.Duration
}

func (a *app) securebootEnrollCommand() *cobra.Command {
	var f securebootEnrollFlags
	cmd := a.command(&cobra.Command{
		Use:     "enroll",
		Short:   "Enrol Secure Boot keys in the firmware of a node in maintenance mode whose firmware is in Setup Mode",
		Long:    securebootEnrollLong,
		Example: securebootEnrollExample,
		Args:    cli.NoArgs,
	}, func(a *app, ctx context.Context, pos []string) error { return a.securebootEnroll(ctx, f) })
	fs := cmd.Flags()
	fs.StringVar(&f.keys, "keys", "", "directory of the keys, as chalkctl secureboot new writes it")
	fs.BoolVar(&f.microsoft, "microsoft", false, "also enrol Microsoft's KEK CAs and its UEFI and option ROM CAs, which sign the firmware of graphics and network cards and other boot loaders")
	fs.BoolVar(&f.optionROMs, "option-roms", false, "also enrol in db the SHA-256 digests of the option ROMs the node's firmware loaded, as its TPM event log lists them")
	fs.StringArrayVar(&f.dbHashes, "db-hash", nil, "also enrol in db this SHA-256 Authenticode digest of an image, 64 hex digits, optionally after sha256: (repeatable)")
	fs.BoolVar(&f.force, "force", false, "enrol the keys even if Secure Boot would refuse the boot loader or UKI the node booted, or an option ROM it loaded, under them")
	fs.StringVar(&f.address, "address", "", "address of the node's chalkd, host or host:port")
	f.secrets.Register(fs)
	fs.StringVar(&f.config, "config", "", "client file of an admin to authenticate with instead of the secrets file")
	f.pin.register(fs)
	fs.DurationVar(&f.timeout, "timeout", 10*time.Minute, "how long to wait for the node to boot again with Secure Boot on")
	cmd.MarkFlagRequired("keys")
	cmd.MarkFlagRequired("address")
	return cmd
}

func (a *app) securebootEnroll(ctx context.Context, f securebootEnrollFlags) error {
	if f.timeout <= 0 {
		return cli.Usagef("--timeout must be positive")
	}
	var hashes [][]byte
	for _, s := range f.dbHashes {
		h, err := hex.DecodeString(strings.TrimPrefix(s, "sha256:"))
		if err != nil || len(h) != sha256.Size {
			return cli.Usagef("--db-hash %s is not a SHA-256 digest of 64 hex digits", s)
		}
		hashes = addHash(hashes, h)
	}
	keys, err := secureboot.LoadEnrolmentKeys(f.keys)
	if err != nil {
		return err
	}
	// A node in maintenance mode whose image carries an OS CA needs a client certificate.
	var cert *tls.Certificate
	if f.secrets.Path != "" || f.config != "" {
		creds, err := a.loadCredentials(ctx, f.secrets, f.config)
		if err != nil {
			return err
		}
		cert = creds.cert
	}
	conn, err := f.pin.dial(f.address, "--address", cert)
	if err != nil {
		return err
	}
	defer conn.Close()
	info, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
	if err != nil {
		if client.RefusesCertificate(ctx, err, f.address, cert) {
			return fmt.Errorf("reach %s: the node trusts another OS CA than the secrets file or client file, so it refuses chalkctl's certificate", f.address)
		}
		return fmt.Errorf("reach %s: %w", f.address, err)
	}
	before := conn.Fingerprint()
	if f.pin.insecure {
		fmt.Fprintf(a.stderr, "chalkctl: the node's certificate fingerprint is %s\n", before)
	}
	if info.Msg.Mode != nodev1.Mode_MODE_MAINTENANCE {
		return fmt.Errorf("the node at %s is installed; Secure Boot keys are enrolled in maintenance mode, before the install", f.address)
	}
	if f.optionROMs {
		drivers, err := conn.SecureBootDrivers(ctx, connect.NewRequest(&nodev1.SecureBootDriversRequest{}))
		if err != nil {
			return fmt.Errorf("list the option ROMs: %w", err)
		}
		switch {
		case drivers.Msg.NoEventLog != "":
			fmt.Fprintf(a.stderr, "chalkctl: the node lists no option ROMs: %s\n", drivers.Msg.NoEventLog)
		case len(drivers.Msg.Drivers) == 0:
			fmt.Fprintln(a.stdout, "option ROMs the firmware measured: none")
		default:
			fmt.Fprintln(a.stdout, "option ROMs the firmware measured:")
		}
		for _, d := range drivers.Msg.Drivers {
			fmt.Fprintf(a.stdout, "  sha256:%x %s\n", d.Sha256, d.DevicePath)
			hashes = addHash(hashes, d.Sha256)
		}
	}
	req, err := enrolmentRequest(keys, f.microsoft, hashes, f.force, time.Now())
	if err != nil {
		return err
	}
	resp, err := conn.EnrollSecureBoot(ctx, connect.NewRequest(req))
	if err != nil {
		return fmt.Errorf("enroll the keys: %w", err)
	}
	if resp.Msg.DriversUnchecked != "" {
		fmt.Fprintf(a.stderr, "chalkctl: the node did not check its option ROMs against the new db: %s\n", resp.Msg.DriversUnchecked)
	}
	for _, db := range []struct {
		name string
		list []string
	}{{"db before", resp.Msg.DbBefore}, {"db after", resp.Msg.DbAfter}, {"KEK before", resp.Msg.KekBefore}, {"KEK after", resp.Msg.KekAfter}} {
		if len(db.list) == 0 {
			fmt.Fprintf(a.stdout, "%s: empty\n", db.name)
			continue
		}
		fmt.Fprintf(a.stdout, "%s:\n", db.name)
		for _, s := range db.list {
			fmt.Fprintf(a.stdout, "  %s\n", s)
		}
	}
	fmt.Fprintln(a.stdout, "the node reboots with Secure Boot enforced")
	after, err := a.waitSecureBoot(ctx, f.address, cert, before, f.timeout)
	if err != nil {
		return err
	}
	// The node made a new maintenance certificate when it booted again; the connection that read
	// it verified nothing, so its fingerprint is to be compared with the node's console.
	fmt.Fprintf(a.stderr, "chalkctl: the node's certificate fingerprint is now %s; compare it with the node's console and install with --fingerprint %s\n", after, after)
	fmt.Fprintln(a.stdout, "Secure Boot is on; install the node with chalkctl install")
	return nil
}

// enrolmentRequest signs the updates of db, KEK and PK of the keys at now: db and KEK appended to
// what the firmware holds, db signed by the KEK and KEK by the PK, and PK, which ends Setup Mode,
// signed by itself. With microsoft, Microsoft's CAs are added to db and KEK; db also gets the
// SHA-256 digests of images, hashes.
func enrolmentRequest(k secureboot.Keys, microsoft bool, hashes [][]byte, force bool, now time.Time) (*nodev1.EnrollSecureBootRequest, error) {
	db := append(secureboot.X509SignatureLists(k.Owner, k.DB.Cert), secureboot.SHA256SignatureList(k.Owner, hashes...)...)
	kek := secureboot.X509SignatureLists(k.Owner, k.KEK.Cert)
	if microsoft {
		db = append(db, secureboot.X509SignatureLists(secureboot.MicrosoftOwner, secureboot.MicrosoftDB()...)...)
		kek = append(kek, secureboot.X509SignatureLists(secureboot.MicrosoftOwner, secureboot.MicrosoftKEK()...)...)
	}
	sign := func(name string, vendor [16]byte, attrs uint32, data []byte, signer secureboot.KeyPair) ([]byte, error) {
		u, err := secureboot.SignUpdate(secureboot.Update{Name: name, Vendor: vendor, Attributes: attrs, Data: data}, now, signer.Cert, signer.Key)
		if err != nil {
			return nil, fmt.Errorf("sign the update of %s: %w", name, err)
		}
		return u, nil
	}
	req := &nodev1.EnrollSecureBootRequest{Force: force}
	var err error
	if req.Db, err = sign("db", secureboot.ImageSecurityDatabase, secureboot.AttrAppend, db, k.KEK); err != nil {
		return nil, err
	}
	if req.Kek, err = sign("KEK", secureboot.GlobalVariable, secureboot.AttrAppend, kek, k.PK); err != nil {
		return nil, err
	}
	if req.Pk, err = sign("PK", secureboot.GlobalVariable, secureboot.AttrReplace, secureboot.X509SignatureLists(k.Owner, k.PK.Cert), k.PK); err != nil {
		return nil, err
	}
	return req, nil
}

// waitSecureBoot waits until the node at addr booted again and answers with Secure Boot on, and
// returns the fingerprint of its certificate then. The node serves a new certificate in
// maintenance mode after a reboot, whose fingerprint chalkctl cannot know, so it accepts any; a
// node that answers with another certificate than before booted again. Some firmware reports
// Secure Boot on as soon as PK is written, so the boot that wrote the keys does not count; a new
// boot without Secure Boot on stops the wait.
func (a *app) waitSecureBoot(ctx context.Context, addr string, cert *tls.Certificate, before string, timeout time.Duration) (string, error) {
	var fingerprint string
	err := poll.Until(ctx, "the node to boot again with Secure Boot on", timeout, a.poll(), func() error {
		conn, err := client.Dial(addr, client.Options{Verify: client.AnyCertificate(), Certificate: cert, Source: "--address"})
		if err != nil {
			return err
		}
		defer conn.Close()
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		info, err := conn.Info(callCtx, connect.NewRequest(&nodev1.InfoRequest{}))
		if err != nil {
			return err
		}
		fingerprint = conn.Fingerprint()
		switch state := info.Msg.SecureBoot; {
		case fingerprint == before:
			return fmt.Errorf("the node has not rebooted yet; it reports %s", state)
		case state != nodev1.SecureBoot_SECURE_BOOT_ENABLED:
			return poll.Permanent{Err: fmt.Errorf("the node booted again without Secure Boot on, it reports %s: check the firmware setup", state)}
		}
		return nil
	})
	return fingerprint, err
}

// addHash appends the digest to the digests unless they hold it already.
func addHash(hashes [][]byte, h []byte) [][]byte {
	if slices.ContainsFunc(hashes, func(o []byte) bool { return bytes.Equal(o, h) }) {
		return hashes
	}
	return append(hashes, h)
}
