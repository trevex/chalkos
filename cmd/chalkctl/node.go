package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
)

// chunkSize is how much of an image one install or upgrade message carries.
const chunkSize = 1 << 20

// nodeCommand holds what commands addressing one node share.
type nodeCommand struct {
	cluster  clusterFlags
	secrets  secretFlags
	endpoint string
	// config is the client file of commands that a client file may run.
	config string
}

func (n *nodeCommand) register(fs *flag.FlagSet) {
	n.cluster.register(fs)
	n.secrets.register(fs)
	fs.StringVar(&n.endpoint, "endpoint", "", "address of the node's chalkd, host or host:port (default the node's first static address)")
}

// registerClient registers the flags of a command that a client file may run.
func (n *nodeCommand) registerClient(fs *flag.FlagSet) {
	n.register(fs)
	fs.StringVar(&n.config, "config", "", "client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, when there is no secrets file)")
}

// target is a node of the cluster with the credentials chalkctl reaches it with. secrets holds
// the secrets file for the commands that need it, and nothing for a client file.
type target struct {
	cluster *cluster
	name    string
	node    manifest.Node
	creds   *credentials
	secrets pki.Secrets
	addr    string
}

// target addresses a node with the secrets file.
func (a *app) target(ctx context.Context, n nodeCommand, name string) (*target, error) {
	c, err := a.loadCluster(ctx, n.cluster)
	if err != nil {
		return nil, err
	}
	creds, err := a.loadSecretCredentials(ctx, n.secrets, n.cluster.flake)
	if err != nil {
		return nil, err
	}
	return targetIn(c, n, name, creds)
}

// clientTarget addresses a node with the secrets file or a client file.
func (a *app) clientTarget(ctx context.Context, n nodeCommand, name string) (*target, error) {
	creds, err := a.loadCredentials(ctx, n.secrets, n.config, n.cluster.flake)
	if err != nil {
		return nil, err
	}
	c, err := a.clusterFor(ctx, n.cluster, creds)
	if err != nil {
		return nil, err
	}
	return targetIn(c, n, name, creds)
}

func targetIn(c *cluster, n nodeCommand, name string, creds *credentials) (*target, error) {
	node, err := c.node(name)
	if err != nil {
		return nil, err
	}
	addr, err := endpoint(n.endpoint, name, node.Identity)
	if err != nil {
		return nil, err
	}
	t := &target{cluster: c, name: name, node: node, creds: creds, addr: addr}
	if creds.secrets != nil {
		t.secrets = *creds.secrets
	}
	return t, nil
}

// dialInstalled connects to an installed node, verifying it by the OS CA and its name.
func dialInstalled(t *target) (*client.Conn, error) {
	return dialNode(t, false)
}

// dialNode connects to an installed node; ignoreValidity accepts a node certificate that expired,
// for chalkctl node renew alone.
func dialNode(t *target, ignoreValidity bool) (*client.Conn, error) {
	roots, err := t.creds.roots()
	if err != nil {
		return nil, err
	}
	return client.Dial(t.addr, client.Options{CA: roots, ServerName: t.name, Certificate: t.creds.cert, IgnoreValidity: ignoreValidity})
}

// pinning verifies a node in maintenance mode, which serves a self-signed certificate.
type pinning struct {
	fingerprint string
	insecure    bool
}

func (p *pinning) register(fs *flag.FlagSet) {
	fs.StringVar(&p.fingerprint, "fingerprint", "", "SHA-256 fingerprint the node prints on its console in maintenance mode")
	fs.BoolVar(&p.insecure, "insecure", false, "accept any certificate and print the node's fingerprint")
}

func (p pinning) set() bool { return p.fingerprint != "" || p.insecure }

func (p pinning) dial(addr string, cert *tls.Certificate) (*client.Conn, error) {
	if p.fingerprint != "" && p.insecure {
		return nil, errors.New("pass either --fingerprint or --insecure")
	}
	if !p.set() {
		return nil, errors.New("a node in maintenance mode is verified by its certificate's fingerprint; pass --fingerprint FP, or --insecure to accept any certificate")
	}
	return client.Dial(addr, client.Options{Fingerprint: p.fingerprint, Insecure: p.insecure, Certificate: cert})
}

// fallbackSecret returns the secret enrolled as the second keyslot of the node's encrypted
// volumes: its derived recovery key, or the operator's password. A password typed on the
// terminal is asked twice when confirm is set.
func (a *app) fallbackSecret(ctx context.Context, t *target, passwordFile string, confirm bool) (string, error) {
	switch t.node.Identity.Storage.Fallback {
	case storage.FallbackNone:
		return "", nil
	case "recovery-key":
		if t.creds.secrets == nil {
			return "", fmt.Errorf("node %s unlocks with its recovery key when its TPM fails, which only the secrets file gives; pass --secrets", t.name)
		}
		return pki.RecoveryKey(t.secrets.RecoverySecret, t.cluster.manifest.Cluster.Name, t.name)
	case "password":
		if passwordFile != "" {
			data, err := os.ReadFile(passwordFile)
			if err != nil {
				return "", err
			}
			// Only the line ending that ends the file's line, LF or CRLF; the rest belongs to
			// the password.
			password := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
			if password == "" {
				return "", fmt.Errorf("%s is empty", passwordFile)
			}
			return password, nil
		}
		return a.askPassword(ctx, t.name, confirm)
	}
	return "", fmt.Errorf("node %s has the unknown fallback %q", t.name, t.node.Identity.Storage.Fallback)
}

func (a *app) askPassword(ctx context.Context, node string, confirm bool) (string, error) {
	read := func(prompt string) (string, error) {
		p, err := a.readSecret(ctx, prompt)
		if errors.Is(err, errNoTerminal) {
			return "", fmt.Errorf("node %s unlocks with a password when its TPM fails and there is no terminal to ask for it; pass --password-file", node)
		}
		return string(p), err
	}
	password, err := read(fmt.Sprintf("Password that unlocks %s when its TPM fails: ", node))
	if err != nil {
		return "", err
	}
	if password == "" {
		return "", errors.New("the password is empty")
	}
	if confirm {
		again, err := read("Repeat the password: ")
		if err != nil {
			return "", err
		}
		if again != password {
			return "", errors.New("the passwords differ")
		}
	}
	return password, nil
}

// fallbackFor returns the fallback secret when delivering the identity enrolls it: on an
// encrypted volume the node does not have yet, or on the volume reset recreates. The node's
// status says which volumes it has. A password typed on the terminal is confirmed: the node
// checks it against its existing fallback keyslot only when it has one.
func (a *app) fallbackFor(ctx context.Context, conn *client.Conn, t *target, passwordFile, reset string) (string, error) {
	if t.node.Identity.Storage.Fallback == storage.FallbackNone {
		return "", nil
	}
	resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return "", fmt.Errorf("ask %s for its volumes: %w", t.name, err)
	}
	var existing []string
	for _, v := range resp.Msg.Volumes {
		existing = append(existing, v.Name)
	}
	if !fallbackNeeded(t.node.Identity.Storage, existing, reset) {
		return "", nil
	}
	return a.fallbackSecret(ctx, t, passwordFile, true)
}

// fallbackNeeded tells whether the section enrolls the fallback keyslot on a volume besides the
// existing ones, or on the volume reset recreates.
func fallbackNeeded(section storage.Section, existing []string, reset string) bool {
	if section.Fallback == storage.FallbackNone {
		return false
	}
	for name, v := range section.Volumes {
		if v.Encryption == storage.EncryptionTPM2 && (name == reset || !slices.Contains(existing, name)) {
			return true
		}
	}
	return false
}

// identityJSON is the identity as the node records it; Status compares its version.
func identityJSON(n manifest.Node) (string, error) {
	data, err := json.Marshal(n.Identity)
	return string(data), err
}

// nodeError puts the node's name where the node's own messages say <node>.
func nodeError(err error, name string) error {
	if err == nil {
		return nil
	}
	return errors.New(strings.ReplaceAll(err.Error(), "<node>", name))
}

func (a *app) install(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	var n nodeCommand
	var p pinning
	n.register(fs)
	p.register(fs)
	imagePath := fs.String("image", "", "role image to install when the node runs the installer: a raw image with repart-output.json and repart.d next to it, or the directory nix build produces (default: build the node's role image)")
	signKey := fs.String("sign-key", "", "PEM key of the Secure Boot db signer, to sign the image's UKI and boot loader")
	signCert := fs.String("sign-cert", "", "PEM certificate of the Secure Boot db signer")
	wipe := fs.Bool("wipe-disk", false, "let the installer replace whatever the target disk holds, including an installed node; without it, the installer continues an earlier install of the node's role or takes a disk on which blkid finds no signature, which counts as empty even when it holds data")
	passwordFile := fs.String("password-file", "", "file holding the password of a node whose fallback is a password")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: chalkctl install <node> [flags]")
	}
	t, err := a.target(ctx, n, pos[0])
	if err != nil {
		return err
	}
	cert := t.creds.cert
	conn, err := p.dial(t.addr, cert)
	if err != nil {
		return err
	}
	if p.insecure {
		// An insecure connection's fingerprint vouches only for that connection. The secrets go
		// over a new one pinned to the fingerprint printed here.
		if _, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{})); err != nil {
			return maintenanceError(t, err)
		}
		fp := conn.Fingerprint()
		fmt.Fprintf(a.stderr, "chalkctl: the node's certificate fingerprint is %s\n", fp)
		if conn, err = client.Dial(t.addr, client.Options{Fingerprint: fp, Certificate: cert}); err != nil {
			return err
		}
	}
	info, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
	if err != nil {
		return maintenanceError(t, err)
	}
	if info.Msg.Mode != nodev1.Mode_MODE_MAINTENANCE {
		return fmt.Errorf("%s at %s is installed already", t.name, t.addr)
	}

	id, err := identityJSON(t.node)
	if err != nil {
		return err
	}
	nodeCert, err := pki.IssueNode(t.secrets.NodeCA, nodeNames(t), time.Now())
	if err != nil {
		return err
	}
	fallback, err := a.fallbackSecret(ctx, t, *passwordFile, true)
	if err != nil {
		return err
	}
	share, err := kubernetesShare(t, time.Now())
	if err != nil {
		return err
	}
	header := &nodev1.InstallHeader{
		Identity:        id,
		NodeCertificate: []byte(nodeCert.Certificate),
		NodeKey:         []byte(nodeCert.Key),
		CaCertificate:   []byte(t.secrets.OSCABundle()),
		FallbackSecret:  fallback,
		WipeDisk:        *wipe,
		KubernetesShare: share,
	}

	var img *diskImage
	if !info.Msg.Installer {
		// The node runs its image already, which must be of the node's platform.
		if err := samePlatform(t, info.Msg.Platform); err != nil {
			return err
		}
		header.Target = &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}
		fmt.Fprintf(a.stdout, "installing %s in place\n", t.name)
	} else {
		ref := t.node.Identity.Storage.Disks[storage.SystemDisk].Ref
		header.Target = &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{
			Path: ref.Path, Model: ref.Selector.Model, Serial: ref.Selector.Serial,
			Wwn: ref.Selector.WWN, Size: ref.Selector.Size, Type: ref.Selector.Type,
		}}
		path := *imagePath
		if path == "" {
			if path, err = a.buildImage(ctx, t.cluster, t.node.Role, t.node.Platform); err != nil {
				return err
			}
		}
		if img, err = openImage(ctx, path, *signKey, *signCert, true); err != nil {
			return err
		}
		defer img.Close()
		if header.SystemDefinitions, err = readDefinitions(img.definitions); err != nil {
			return err
		}
		if err := checkInstallImage(t, img, *signCert); err != nil {
			return err
		}
		header.Image = img.header
		fmt.Fprintf(a.stdout, "installing %s onto %s from %s: %s %s, sending %d bytes of store, hash tree, UKI and boot loader\n", t.name, ref, path, img.info.ID(), img.info.Version(), img.size())
	}

	stream := conn.Install(ctx)
	if err := stream.Send(&nodev1.InstallRequest{Message: &nodev1.InstallRequest_Header{Header: header}}); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if img != nil {
		parts, done, err := img.parts()
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
	if t.node.Identity.Storage.Fallback == "recovery-key" {
		fmt.Fprintf(a.stdout, "chalkctl recovery-key %s prints the key that unlocks it when its TPM fails\n", t.name)
	}
	return nil
}

// maintenanceError explains why a node in maintenance mode did not answer. A node in maintenance
// mode whose image carries an OS CA accepts clients of that OS CA alone; it refuses chalkctl's
// certificate when the image was built from a secrets.pub.json before the OS CA rotated.
func maintenanceError(t *target, err error) error {
	if refusesCertificate(t.addr, t.creds.cert) {
		return fmt.Errorf("reach %s at %s: the maintenance image trusts another OS CA than the secrets file, so it refuses chalkctl's certificate; build the image and the installer media again from the current secrets.pub.json", t.name, t.addr)
	}
	return fmt.Errorf("reach %s at %s: %w", t.name, t.addr, err)
}

// refusesCertificate reports whether the chalkd at addr refuses cert for an OS CA it does not
// know. A server refuses a client certificate with an alert after the handshake, which an RPC may
// report as a broken connection; reading from a connection of its own receives the alert itself.
// Nothing is sent over the connection, so the server is not verified.
func refusesCertificate(addr string, cert *tls.Certificate) bool {
	if cert == nil {
		return false
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, client.Port)
	}
	cfg := &tls.Config{InsecureSkipVerify: true, Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"}}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, cfg)
	if err == nil {
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err = conn.Read(make([]byte, 1))
	}
	return err != nil && strings.Contains(err.Error(), "remote error: tls: unknown certificate authority")
}

// readDefinitions reads a role image's repart.d files.
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

func (a *app) disks(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("disks", flag.ContinueOnError)
	var n nodeCommand
	var p pinning
	n.registerClient(fs)
	p.register(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	var conn *client.Conn
	switch {
	case len(pos) == 1:
		t, err := a.clientTarget(ctx, n, pos[0])
		if err != nil {
			return err
		}
		if p.set() {
			conn, err = p.dial(t.addr, t.creds.cert)
		} else {
			conn, err = dialInstalled(t)
		}
		if err != nil {
			return err
		}
	case len(pos) == 0 && n.endpoint != "":
		// A node not in the cluster definition yet: only maintenance mode, with a certificate when
		// a secrets file or a client file is given, for an image with an OS CA.
		var cert *tls.Certificate
		if n.secrets.path != "" || n.config != "" {
			creds, err := a.loadCredentials(ctx, n.secrets, n.config, n.cluster.flake)
			if err != nil {
				return err
			}
			cert = creds.cert
		}
		if conn, err = p.dial(n.endpoint, cert); err != nil {
			return err
		}
	default:
		return errors.New("usage: chalkctl disks (<node> | --endpoint ADDR) [--fingerprint FP | --insecure]")
	}
	resp, err := conn.Disks(ctx, connect.NewRequest(&nodev1.DisksRequest{}))
	if err != nil {
		return err
	}
	if p.insecure {
		fmt.Fprintf(a.stderr, "chalkctl: the node's certificate fingerprint is %s\n", conn.Fingerprint())
	}
	w := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "DEVICE\tSIZE\tTYPE\tMODEL\tSERIAL\tWWN\tUSE")
	for _, d := range resp.Msg.Disks {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.Device, size(d.Size), d.Type, d.Model, d.Serial, d.Wwn, d.Usage)
		for _, part := range d.Partitions {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t\t\t\n", part.Device, size(part.Size), part.Label, part.Content)
		}
	}
	return w.Flush()
}

func size(bytes uint64) string {
	units := []string{"B", "K", "M", "G", "T", "P"}
	v := float64(bytes)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + units[i]
}

func (a *app) applyIdentity(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("apply-identity", flag.ContinueOnError)
	var n nodeCommand
	n.register(fs)
	passwordFile := fs.String("password-file", "", "file holding the password of a node whose fallback is a password")
	withShare := fs.Bool("kubernetes-share", false, "also deliver a new Kubernetes share, such as a new kubelet certificate for a worker")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: chalkctl apply-identity <node> [--kubernetes-share]")
	}
	t, err := a.target(ctx, n, pos[0])
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	id, err := identityJSON(t.node)
	if err != nil {
		return err
	}
	fallback, err := a.fallbackFor(ctx, conn, t, *passwordFile, "")
	if err != nil {
		return err
	}
	var share []byte
	if *withShare {
		if share, err = kubernetesShare(t, time.Now()); err != nil {
			return err
		}
		if share == nil {
			return fmt.Errorf("the role of %s has no Kubernetes", t.name)
		}
	}
	resp, err := conn.ApplyIdentity(ctx, connect.NewRequest(&nodev1.ApplyIdentityRequest{Identity: id, FallbackSecret: fallback, KubernetesShare: share}))
	if err != nil {
		return nodeError(err, t.name)
	}
	for _, c := range resp.Msg.Changes {
		fmt.Fprintf(a.stdout, "volume %s: %s\n", c.Volume, c.Reason)
	}
	for _, u := range resp.Msg.RestartedUnits {
		fmt.Fprintf(a.stdout, "restarted %s\n", u)
	}
	fmt.Fprintf(a.stdout, "%s runs identity %s\n", t.name, identity.IdentityVersion([]byte(id)))
	return nil
}

func (a *app) resetVolume(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("storage reset", flag.ContinueOnError)
	var n nodeCommand
	n.registerClient(fs)
	passwordFile := fs.String("password-file", "", "file holding the password of a node whose fallback is a password")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New("usage: chalkctl storage reset <node> <volume>")
	}
	t, err := a.clientTarget(ctx, n, pos[0])
	if err != nil {
		return err
	}
	if t.cluster.partial {
		return errors.New("storage reset creates the volume as the node's identity defines it, which only the cluster definition gives; pass --flake or --manifest")
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	id, err := identityJSON(t.node)
	if err != nil {
		return err
	}
	fallback, err := a.fallbackFor(ctx, conn, t, *passwordFile, pos[1])
	if err != nil {
		return err
	}
	if _, err := conn.ResetVolume(ctx, connect.NewRequest(&nodev1.ResetVolumeRequest{Volume: pos[1], Identity: id, FallbackSecret: fallback})); err != nil {
		return nodeError(err, t.name)
	}
	fmt.Fprintf(a.stdout, "volume %s of %s was wiped and created again\n", pos[1], t.name)
	return nil
}

func (a *app) status(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	var n nodeCommand
	n.registerClient(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: chalkctl status <node>")
	}
	t, err := a.clientTarget(ctx, n, pos[0])
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return err
	}
	s := resp.Msg
	state := "no cluster definition to compare it with"
	if !t.cluster.partial {
		id, err := identityJSON(t.node)
		if err != nil {
			return err
		}
		state = "the cluster definition's"
		if identity.IdentityVersion([]byte(id)) != s.IdentityVersion {
			state = "not the cluster definition's; chalkctl apply-identity " + t.name + " delivers it"
		}
	}
	fmt.Fprintf(a.stdout, "identity %s (%s)\n", s.IdentityVersion, state)
	fmt.Fprintln(a.stdout, platformLine(t, s.Platform))
	for _, line := range bootLines(s.Boot) {
		fmt.Fprintln(a.stdout, line)
	}
	w := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "VOLUME\tDISK\tMOUNT POINT\tSTATE")
	for _, v := range s.Volumes {
		st := "present"
		switch {
		case !v.Present:
			st = "missing"
		case v.Mounted:
			st = "mounted"
		case v.MountPoint != "":
			st = "not mounted"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.Name, v.Disk, v.MountPoint, st)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, d := range s.Disks {
		if d.Error != "" {
			fmt.Fprintf(a.stdout, "disk %s: %s\n", d.Name, d.Error)
		}
	}
	if k := s.Kubernetes; k != nil {
		fmt.Fprintln(a.stdout, kubernetesLine(k))
	}
	if len(s.Certificates) > 0 {
		fmt.Fprintln(a.stdout, "certificates:")
		w := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
		for _, c := range s.Certificates {
			fmt.Fprintln(w, certificateLine(c, t.name))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	if len(s.Trust) > 0 {
		fmt.Fprintln(a.stdout, "trust:")
		w := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
		for _, tr := range s.Trust {
			fmt.Fprintln(w, trustLine(tr))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	if s.Time != nil {
		fmt.Fprintln(a.stdout, timeLine(s.Time))
	}
	for _, u := range s.FailedUnits {
		fmt.Fprintf(a.stdout, "failed unit %s\n", u)
	}
	return nil
}

// bootLines are the status lines of the node's boot: the image it runs, an upgrade it boots next,
// and one it fell back from, with the lines that version logged last.
func bootLines(b *nodev1.BootStatus) []string {
	if b == nil {
		return nil
	}
	if b.Error != "" {
		return []string{fmt.Sprintf("image %s; its boot is unknown: %s", b.Version, b.Error)}
	}
	line := fmt.Sprintf("image %s, booted from %s", b.Version, b.Entry)
	if !b.Blessed {
		line += ", not found healthy yet"
	}
	lines := []string{line}
	if b.Staged != "" {
		lines = append(lines, fmt.Sprintf("upgrade to %s installed; it boots next", b.Staged))
	}
	if b.Failed != "" {
		lines = append(lines, fmt.Sprintf("upgrade to %s failed: rolled back to %s", b.Failed, b.Version))
		for _, l := range b.Journal {
			lines = append(lines, "  "+l)
		}
	}
	return lines
}

// certificateLine is a certificate's line of the status, with tabs between its columns.
func certificateLine(c *nodev1.CertificateStatus, node string) string {
	line := fmt.Sprintf("  %s\texpires %s", c.Name, c.NotAfter.AsTime().UTC().Format(time.DateOnly))
	if c.NotAfter == nil {
		line = fmt.Sprintf("  %s\texpiry unknown", c.Name)
	}
	if c.Problem != "" {
		line += "\t" + strings.ReplaceAll(c.Problem, "<node>", node)
	}
	return line
}

// trustLine is a line of the status's trust section: what is trusted, by the first 16 hex digits
// of each fingerprint, the issuing one marked.
func trustLine(t *nodev1.TrustStatus) string {
	var fps []string
	for _, fp := range t.Fingerprints {
		short := fp[:min(16, len(fp))]
		if fp == t.Issuing {
			short += " (issues)"
		}
		fps = append(fps, short)
	}
	return fmt.Sprintf("  %s\t%s", t.Name, strings.Join(fps, ", "))
}

// timeLine is the status line of the node's clock.
func timeLine(t *nodev1.TimeStatus) string {
	switch {
	case t.Error != "":
		return "time: unknown: " + t.Error
	case !t.Synchronised:
		return "time: not synchronised; certificates are checked against this clock"
	}
	return fmt.Sprintf("time: synchronised to %s, offset %+.6f s", t.Source, t.OffsetSeconds)
}

// kubernetesLine is the status line of the node's Kubernetes side.
func kubernetesLine(k *nodev1.KubernetesStatus) string {
	line := fmt.Sprintf("kubernetes %s: %s", k.Kind, k.State)
	if k.NodeReady != "" {
		line += ", node ready: " + k.NodeReady
	}
	if k.Vip != "" {
		line += ", vip " + k.Vip
	}
	if k.ControlPlane != "" {
		line += ", control plane " + k.ControlPlane
	}
	return line
}

func (a *app) logs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	var n nodeCommand
	var p pinning
	n.registerClient(fs)
	p.register(fs)
	follow := fs.Bool("f", false, "keep printing new entries")
	unit := fs.String("unit", "", "show only this unit's entries")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: chalkctl logs <node> [-f] [--unit U]")
	}
	t, err := a.clientTarget(ctx, n, pos[0])
	if err != nil {
		return err
	}
	conn, err := a.dialEither(t, p)
	if err != nil {
		return err
	}
	stream, err := conn.Logs(ctx, connect.NewRequest(&nodev1.LogsRequest{Unit: *unit, Follow: *follow}))
	if err != nil {
		return err
	}
	out := bufio.NewWriter(a.stdout)
	defer out.Flush()
	for stream.Receive() {
		fmt.Fprintln(out, stream.Msg().Line)
		if *follow {
			out.Flush()
		}
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func (a *app) reboot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reboot", flag.ContinueOnError)
	var n nodeCommand
	var p pinning
	n.registerClient(fs)
	p.register(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: chalkctl reboot <node>")
	}
	t, err := a.clientTarget(ctx, n, pos[0])
	if err != nil {
		return err
	}
	conn, err := a.dialEither(t, p)
	if err != nil {
		return err
	}
	if _, err := conn.Reboot(ctx, connect.NewRequest(&nodev1.RebootRequest{})); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "%s reboots\n", t.name)
	return nil
}

// dialEither connects to a node in maintenance mode when a fingerprint or --insecure is given,
// and to an installed node otherwise.
func (a *app) dialEither(t *target, p pinning) (*client.Conn, error) {
	if p.set() {
		return p.dial(t.addr, t.creds.cert)
	}
	return dialInstalled(t)
}

// imageFiles finds the raw image in an image directory or takes the given file; the role's
// system-region definitions are in repart.d next to it.
func imageFiles(path string) (raw, definitions string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	raw = path
	if info.IsDir() {
		raws, err := filepath.Glob(filepath.Join(path, "*.raw"))
		if err != nil || len(raws) != 1 {
			return "", "", fmt.Errorf("want exactly one .raw image in %s, found %v", path, raws)
		}
		raw = raws[0]
	}
	return raw, filepath.Join(filepath.Dir(raw), "repart.d"), nil
}

// nodeNames are what the node certificate of the target is for: its name, its hostname and its
// static addresses.
func nodeNames(t *target) pki.NodeNames {
	hostnames := []string{t.name}
	if h := t.node.Identity.Hostname; h != "" && h != t.name {
		hostnames = append(hostnames, h)
	}
	return pki.NodeNames{CommonName: t.name, DNSNames: hostnames, IPs: ipAddresses(t.node.Identity.StaticAddresses())}
}

// ipAddresses parses the addresses that are IPs, for the node certificate.
func ipAddresses(addrs []string) []net.IP {
	var ips []net.IP
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			ips = append(ips, ip)
		}
	}
	return ips
}

// samePlatform refuses a node that runs an image of another platform than the cluster definition
// declares: its identity and images would not fit the machine.
func samePlatform(t *target, reported string) error {
	if t.cluster.partial || reported == t.node.Platform {
		return nil
	}
	return fmt.Errorf("%s runs an image built for %s, but the cluster definition declares it on %s; changing a node's platform is a reinstall", t.name, reported, t.node.Platform)
}

// platformLine is the status line of the platform the node runs on, compared with the cluster
// definition's.
func platformLine(t *target, reported string) string {
	switch {
	case t.cluster.partial:
		return "platform " + reported
	case reported != t.node.Platform:
		return fmt.Sprintf("platform %s, but the cluster definition declares %s; changing a node's platform is a reinstall", reported, t.node.Platform)
	}
	return fmt.Sprintf("platform %s (the cluster definition's)", reported)
}
