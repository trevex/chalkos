package chalkctl

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
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
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
)

// chunkSize is how much of an image one install or upgrade message carries.
const chunkSize = 1 << 20

// nodeCommand holds what commands addressing one node share.
type nodeCommand struct {
	cluster clusterFlags
	secrets secretFlags
	address string
	// config is the client file of commands that a client file may run.
	config string
	// needs names the command when it needs the nodes' config, which only an inventory gives: it
	// is refused without one before any credentials are read.
	needs string
}

func (n *nodeCommand) register(fs *pflag.FlagSet) {
	n.cluster.register(fs)
	n.secrets.register(fs)
	fs.StringVar(&n.address, "address", "", "address of the node's chalkd, host or host:port (default the node's address in the inventory)")
}

// registerClient registers the flags of a command that a client file may run.
func (n *nodeCommand) registerClient(fs *pflag.FlagSet) {
	n.register(fs)
	fs.StringVar(&n.config, "config", "", "client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)")
}

// target is a node of the cluster with the credentials chalkctl reaches it with. secrets holds
// the secrets file for the commands that need it, and nothing for a client file.
type target struct {
	cluster *cluster
	name    string
	creds   *credentials
	secrets pki.Secrets
	addr    string
	// source says where addr comes from, for errors reaching it.
	source string
}

// target addresses a node with the secrets file.
func (a *app) target(ctx context.Context, n nodeCommand, name string) (*target, error) {
	c, err := n.loadCluster()
	if err != nil {
		return nil, err
	}
	creds, err := a.loadSecretCredentials(ctx, n.secrets)
	if err != nil {
		return nil, err
	}
	return targetIn(c, n, name, creds)
}

// clientTarget addresses a node with the secrets file or a client file.
func (a *app) clientTarget(ctx context.Context, n nodeCommand, name string) (*target, error) {
	c, err := n.loadCluster()
	if err != nil {
		return nil, err
	}
	creds, err := a.loadCredentials(ctx, n.secrets, n.config)
	if err != nil {
		return nil, err
	}
	if err := c.checkCredentials(creds); err != nil {
		return nil, err
	}
	return targetIn(c, n, name, creds)
}

// loadCluster loads the cluster the command's flags name, and refuses a partial one when the
// command needs the nodes' config.
func (n nodeCommand) loadCluster() (*cluster, error) {
	c, err := loadCluster(n.cluster)
	if err != nil {
		return nil, err
	}
	if n.needs != "" {
		if err := c.requireNodes(n.needs); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func targetIn(c *cluster, n nodeCommand, name string, creds *credentials) (*target, error) {
	if err := c.node(name); err != nil {
		return nil, err
	}
	addr, source, err := c.address(n.address, name)
	if err != nil {
		return nil, err
	}
	t := &target{cluster: c, name: name, creds: creds, addr: addr, source: source}
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
	return client.Dial(t.addr, client.Options{CA: roots, ServerName: t.name, Certificate: t.creds.cert, IgnoreValidity: ignoreValidity, Source: t.source})
}

// pinning verifies a node in maintenance mode, which serves a self-signed certificate.
type pinning struct {
	fingerprint string
	insecure    bool
}

func (p *pinning) register(fs *pflag.FlagSet) {
	fs.StringVar(&p.fingerprint, "fingerprint", "", "SHA-256 fingerprint the node prints on its console in maintenance mode")
	fs.BoolVar(&p.insecure, "insecure", false, "accept any certificate and print the node's fingerprint")
}

func (p pinning) set() bool { return p.fingerprint != "" || p.insecure }

func (p pinning) dial(addr, source string, cert *tls.Certificate) (*client.Conn, error) {
	if p.fingerprint != "" && p.insecure {
		return nil, errors.New("pass either --fingerprint or --insecure")
	}
	if !p.set() {
		return nil, errors.New("a node in maintenance mode is verified by its certificate's fingerprint; pass --fingerprint FP, or --insecure to accept any certificate")
	}
	return client.Dial(addr, client.Options{Fingerprint: p.fingerprint, Insecure: p.insecure, Certificate: cert, Source: source})
}

// fallbackSecret returns the secret enrolled as the second keyslot of the node's encrypted
// volumes: its derived recovery key, or the operator's password. A password typed on the
// terminal is asked twice when confirm is set.
func (a *app) fallbackSecret(ctx context.Context, t *target, fallback, passwordFile string, confirm bool) (string, error) {
	switch fallback {
	case storage.FallbackNone:
		return "", nil
	case "recovery-key":
		if t.creds.secrets == nil {
			return "", fmt.Errorf("node %s unlocks with its recovery key when its TPM fails, which only the secrets file gives; pass --secrets", t.name)
		}
		return pki.RecoveryKey(t.secrets.RecoverySecret, t.cluster.name, t.name)
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
	return "", fmt.Errorf("node %s has the unknown fallback %q", t.name, fallback)
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

// fallbackFor returns the fallback secret when delivering the config enrolls it: on an encrypted
// volume the node does not have yet, or on the volume reset recreates. section is the storage the
// config gives the node; the node's status says which volumes it has. A password typed on the
// terminal is confirmed: the node checks it against its existing fallback keyslot only when it
// has one.
func (a *app) fallbackFor(ctx context.Context, conn *client.Conn, t *target, section storage.Section, passwordFile, reset string) (string, error) {
	if section.Fallback == storage.FallbackNone {
		return "", nil
	}
	resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return "", fmt.Errorf("ask %s for its volumes: %w", t.name, err)
	}
	return a.fallbackOf(ctx, resp.Msg, t, section, passwordFile, reset)
}

// fallbackOf is fallbackFor with the node's status at hand.
func (a *app) fallbackOf(ctx context.Context, st *nodev1.StatusResponse, t *target, section storage.Section, passwordFile, reset string) (string, error) {
	if section.Fallback == storage.FallbackNone {
		return "", nil
	}
	var existing []string
	for _, v := range st.Volumes {
		existing = append(existing, v.Name)
	}
	if !fallbackNeeded(section, existing, reset) {
		return "", nil
	}
	return a.fallbackSecret(ctx, t, section.Fallback, passwordFile, true)
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

// nodeError puts the node's name where the node's own messages say <node>.
func nodeError(err error, name string) error {
	if err == nil {
		return nil
	}
	return errors.New(strings.ReplaceAll(err.Error(), "<node>", name))
}

type installFlags struct {
	node                                   nodeCommand
	pin                                    pinning
	image, signKey, signCert, passwordFile string
	wipeDisk                               bool
}

func (a *app) installCommand() *cobra.Command {
	var f installFlags
	cmd := a.command(&cobra.Command{
		Use:     "install <node>",
		Short:   "Install a node that waits in maintenance mode",
		Long:    installLong,
		Example: installExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.install(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.register(fs)
	f.pin.register(fs)
	fs.StringVar(&f.image, "image", "", "image to install when the node runs the installer: a raw image with repart-output.json and repart.d next to it, or an image directory (default the directory the node's Image names)")
	fs.StringVar(&f.signKey, "sign-key", "", "PEM key of the Secure Boot db signer, to sign the image's UKI and boot loader")
	fs.StringVar(&f.signCert, "sign-cert", "", "PEM certificate of the Secure Boot db signer, to check the image's signatures against before it is sent")
	fs.BoolVar(&f.wipeDisk, "wipe-disk", false, "let the installer replace whatever the target disk holds, including an installed node; without it, the installer continues an earlier install of the node or takes a disk on which blkid finds no signature, which counts as empty even when it holds data")
	fs.StringVar(&f.passwordFile, "password-file", "", "file holding the password of a node whose fallback is a password")
	return cmd
}

func (a *app) install(ctx context.Context, f installFlags, pos []string) error {
	n, p := f.node, f.pin
	if len(pos) != 1 {
		return errors.New("usage: chalkctl install <node> [flags]")
	}
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
			return maintenanceError(t, err)
		}
		fp := conn.Fingerprint()
		fmt.Fprintf(a.stderr, "chalkctl: the node's certificate fingerprint is %s\n", fp)
		if conn, err = client.Dial(t.addr, client.Options{Fingerprint: fp, Certificate: cert, Source: t.source}); err != nil {
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

	header := &nodev1.InstallHeader{CaCertificate: []byte(t.secrets.OSCABundle()), WipeDisk: f.wipeDisk}
	var img *diskImage
	var c contract
	path := f.image
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
		fromInventory := path == ""
		if fromInventory {
			if path, err = t.cluster.imageDir(t.name); err != nil {
				return err
			}
		}
		if img, err = openImage(ctx, path, f.signKey, f.signCert, true, a.stderr); err != nil {
			return err
		}
		defer img.Close()
		if fromInventory {
			if err := t.cluster.sameBuild(t.name, img); err != nil {
				return err
			}
		}
		if header.SystemDefinitions, err = readDefinitions(img.definitions); err != nil {
			return err
		}
		if err := checkInstallImage(t, img, f.signCert); err != nil {
			return err
		}
		// The installer runs another image than the one it installs, so it gets that image's
		// contract with the config.
		if c, err = imageContract(filepath.Dir(img.definitions)); err != nil {
			return err
		}
		c.platform = img.info.Platform()
		header.Image, header.Schemas, header.KubernetesImage = img.header, c.files, c.kubernetesImage
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
		fmt.Fprintf(a.stdout, "installing %s onto %s from %s: %s %s, sending %d bytes of store, hash tree, UKI and boot loader\n", t.name, ref, path, img.info.ID(), img.name(), img.size())
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
	if fallback == "recovery-key" {
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

// nodePinFlags are the flags of a command that reaches an installed node or, with a fingerprint
// or --insecure, a node in maintenance mode.
type nodePinFlags struct {
	node nodeCommand
	pin  pinning
}

func (a *app) disksCommand() *cobra.Command {
	var f nodePinFlags
	cmd := a.command(&cobra.Command{
		Use:     "disks [<node>]",
		Short:   "List a node's disks",
		Long:    disksLong,
		Example: disksExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.disks(ctx, f.node, f.pin, pos) })
	f.node.registerClient(cmd.Flags())
	f.pin.register(cmd.Flags())
	return cmd
}

func (a *app) disks(ctx context.Context, n nodeCommand, p pinning, pos []string) error {
	var conn *client.Conn
	var err error
	switch {
	case len(pos) == 1:
		t, err := a.clientTarget(ctx, n, pos[0])
		if err != nil {
			return err
		}
		if p.set() {
			conn, err = p.dial(t.addr, t.source, t.creds.cert)
		} else {
			conn, err = dialInstalled(t)
		}
		if err != nil {
			return err
		}
	case len(pos) == 0 && n.address != "":
		// A node not in the inventory yet: only maintenance mode, with a certificate when a secrets
		// file or a client file is given, for an image with an OS CA.
		var cert *tls.Certificate
		if n.secrets.path != "" || n.config != "" {
			creds, err := a.loadCredentials(ctx, n.secrets, n.config)
			if err != nil {
				return err
			}
			cert = creds.cert
		}
		if conn, err = p.dial(n.address, "--address", cert); err != nil {
			return err
		}
	default:
		return errors.New("usage: chalkctl disks (<node> | --address ADDR) [--fingerprint FP | --insecure]")
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

type applyConfigFlags struct {
	node                          nodeCommand
	passwordFile                  string
	kubernetesShare, staged, diff bool
}

func (a *app) applyConfigCommand() *cobra.Command {
	var f applyConfigFlags
	cmd := a.command(&cobra.Command{
		Use:     "apply-config <node>",
		Short:   "Deliver a node's config from the inventory",
		Long:    applyConfigLong,
		Example: applyConfigExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.applyConfig(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.register(fs)
	registerPasswordFile(fs, &f.passwordFile)
	fs.BoolVar(&f.kubernetesShare, "kubernetes-share", false, "also deliver a new Kubernetes share, such as a new kubelet certificate for a worker")
	fs.BoolVar(&f.staged, "staged", false, "deliver the config for the image staged to boot next instead, checked against the image its Image names")
	fs.BoolVar(&f.diff, "diff", false, "print what the config changes in the node's config, document by document and field by field, and change nothing")
	return cmd
}

// registerPasswordFile registers --password-file.
func registerPasswordFile(fs *pflag.FlagSet, p *string) {
	fs.StringVar(p, "password-file", "", "file holding the password of a node whose fallback is a password")
}

func (a *app) applyConfig(ctx context.Context, f applyConfigFlags, pos []string) error {
	n := f.node
	if len(pos) != 1 {
		return errors.New("usage: chalkctl apply-config <node> [--kubernetes-share | --staged]")
	}
	if f.staged && f.kubernetesShare {
		return errors.New("--staged delivers config alone; deliver the Kubernetes share to the running image")
	}
	n.needs = "apply-config"
	t, err := a.target(ctx, n, pos[0])
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	// The staged image is the one the node's Image names, as after an upgrade without a reboot; the
	// node checks the config against the staged image itself again, and refuses it without one.
	var c contract
	if f.staged {
		c, err = t.cluster.imageContractOf(t.name)
	} else {
		c, err = nodeContract(ctx, conn, t.name)
	}
	if err != nil {
		return err
	}
	d, err := t.cluster.deliveryFor(t.name, c)
	if err != nil {
		return err
	}
	st, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return err
	}
	if !f.staged && st.Msg.Boot.GetStaged() != nil {
		version := st.Msg.StagedConfigVersion
		if version == "" {
			version = "none"
		}
		fmt.Fprintf(a.stderr, "%s has a staged image with its own config (version %s); apply-config --staged updates it\n", t.name, version)
	}
	if f.staged && st.Msg.Boot.GetStaged() == nil {
		return fmt.Errorf("%s: no image is staged to boot next; apply the config to the running image", t.name)
	}
	nodeDocs, err := slotConfig(ctx, conn, f.staged)
	if err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
	}
	changes := diffConfig(nodeDocs, d.Documents)
	fmt.Fprintf(a.stdout, "%s: %s\n", t.name, changes.summary())
	if f.diff {
		changes.write(a.stdout)
		return nil
	}
	// The rules that compare the config with the node's are checked against the config the node
	// serves before anything is sent, as the node checks them on delivery: against the running
	// slot's, also for the staged image, whose slot the diff compares with.
	baseline := nodeDocs
	if f.staged {
		if baseline, err = slotConfig(ctx, conn, false); err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
	}
	if err := checkRules(c, d.documents, baseline, t.name); err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
	}
	fallback, err := a.fallbackOf(ctx, st.Msg, t, d.identity.Storage, f.passwordFile, "")
	if err != nil {
		return err
	}
	var share []byte
	if f.kubernetesShare {
		if share, err = kubernetesShare(t, time.Now()); err != nil {
			return err
		}
		if share == nil {
			return fmt.Errorf("%s is no Kubernetes node: its Node names no type", t.name)
		}
	}
	resp, err := conn.ApplyConfig(ctx, connect.NewRequest(&nodev1.ApplyConfigRequest{Config: d.Text, FallbackSecret: fallback, KubernetesShare: share, Staged: f.staged}))
	if err != nil {
		return nodeError(err, t.name)
	}
	for _, c := range resp.Msg.Changes {
		fmt.Fprintf(a.stdout, "volume %s: %s\n", c.Volume, c.Reason)
	}
	for _, u := range resp.Msg.RestartedUnits {
		fmt.Fprintf(a.stdout, "restarted %s\n", u)
	}
	if f.staged {
		fmt.Fprintf(a.stdout, "%s boots config %s with its staged image\n", t.name, d.Version)
		return nil
	}
	fmt.Fprintf(a.stdout, "%s runs config %s\n", t.name, d.Version)
	return nil
}

func (a *app) resetVolumeCommand() *cobra.Command {
	var n nodeCommand
	var passwordFile string
	cmd := a.command(&cobra.Command{
		Use:     "reset <node> <volume>",
		Short:   "Wipe and recreate one volume",
		Long:    resetVolumeLong,
		Example: resetVolumeExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.resetVolume(ctx, n, passwordFile, pos) })
	n.registerClient(cmd.Flags())
	registerPasswordFile(cmd.Flags(), &passwordFile)
	return cmd
}

func (a *app) resetVolume(ctx context.Context, n nodeCommand, passwordFile string, pos []string) error {
	if len(pos) != 2 {
		return errors.New("usage: chalkctl storage reset <node> <volume>")
	}
	n.needs = "storage reset"
	t, err := a.clientTarget(ctx, n, pos[0])
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	c, err := nodeContract(ctx, conn, t.name)
	if err != nil {
		return err
	}
	d, err := t.cluster.deliveryFor(t.name, c)
	if err != nil {
		return err
	}
	fallback, err := a.fallbackFor(ctx, conn, t, d.identity.Storage, passwordFile, pos[1])
	if err != nil {
		return err
	}
	if _, err := conn.ResetVolume(ctx, connect.NewRequest(&nodev1.ResetVolumeRequest{Volume: pos[1], Config: d.Text, FallbackSecret: fallback})); err != nil {
		return nodeError(err, t.name)
	}
	fmt.Fprintf(a.stdout, "volume %s of %s was wiped and created again\n", pos[1], t.name)
	return nil
}

func (a *app) statusCommand() *cobra.Command {
	var n nodeCommand
	cmd := a.command(&cobra.Command{
		Use:     "status <node>",
		Short:   "Show an installed node's status",
		Long:    statusLong,
		Example: statusExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.status(ctx, n, pos) })
	n.registerClient(cmd.Flags())
	return cmd
}

func (a *app) status(ctx context.Context, n nodeCommand, pos []string) error {
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
	state := t.cluster.configState(t.name, s.ConfigVersion, false)
	version := s.ConfigVersion
	if version == "" {
		// A node installed before config documents runs an identity alone.
		version = "none"
	}
	fmt.Fprintf(a.stdout, "config %s (%s)\n", version, state)
	if s.Boot.GetStaged() != nil {
		staged := s.StagedConfigVersion
		if staged == "" {
			staged = "none"
		}
		fmt.Fprintf(a.stdout, "staged config %s (%s)\n", staged, t.cluster.configState(t.name, s.StagedConfigVersion, true))
	}
	fmt.Fprintln(a.stdout, t.cluster.platformLine(t.name, s.Platform))
	for _, line := range bootLines(s.Boot) {
		fmt.Fprintln(a.stdout, line)
	}
	if line := t.cluster.imageLine(t.name, s.Boot); line != "" {
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
// and one it fell back from, with the lines that image logged last.
func bootLines(b *nodev1.BootStatus) []string {
	if b == nil {
		return nil
	}
	running := bootImageName(b.Running)
	if b.Error != "" {
		return []string{fmt.Sprintf("image %s; its boot is unknown: %s", running, b.Error)}
	}
	line := fmt.Sprintf("image %s, booted from %s", running, b.Entry)
	if !b.Blessed {
		line += ", not found healthy yet"
	}
	lines := []string{line}
	if b.Staged != nil {
		lines = append(lines, fmt.Sprintf("upgrade to %s installed; it boots next", bootImageName(b.Staged)))
	}
	if b.Failed != nil {
		lines = append(lines, fmt.Sprintf("upgrade to %s failed: rolled back to %s", bootImageName(b.Failed), running))
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

type logsFlags struct {
	nodePinFlags
	follow bool
	unit   string
}

func (a *app) logsCommand() *cobra.Command {
	var f logsFlags
	cmd := a.command(&cobra.Command{
		Use:     "logs <node>",
		Short:   "Show a node's journal",
		Long:    logsLong,
		Example: logsExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.logs(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.registerClient(fs)
	f.pin.register(fs)
	fs.BoolVarP(&f.follow, "follow", "f", false, "keep printing new entries")
	fs.StringVar(&f.unit, "unit", "", "show only this unit's entries")
	return cmd
}

func (a *app) logs(ctx context.Context, f logsFlags, pos []string) error {
	n, p := f.node, f.pin
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
	stream, err := conn.Logs(ctx, connect.NewRequest(&nodev1.LogsRequest{Unit: f.unit, Follow: f.follow}))
	if err != nil {
		return err
	}
	out := bufio.NewWriter(a.stdout)
	defer out.Flush()
	for stream.Receive() {
		fmt.Fprintln(out, stream.Msg().Line)
		if f.follow {
			out.Flush()
		}
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func (a *app) rebootCommand() *cobra.Command {
	var f nodePinFlags
	cmd := a.command(&cobra.Command{
		Use:     "reboot <node>",
		Short:   "Reboot a node",
		Long:    rebootLong,
		Example: rebootExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.reboot(ctx, f.node, f.pin, pos) })
	f.node.registerClient(cmd.Flags())
	f.pin.register(cmd.Flags())
	return cmd
}

func (a *app) reboot(ctx context.Context, n nodeCommand, p pinning, pos []string) error {
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
		return p.dial(t.addr, t.source, t.creds.cert)
	}
	return dialInstalled(t)
}

// imageFiles finds the raw image in an image directory or takes the given file; the image's
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
