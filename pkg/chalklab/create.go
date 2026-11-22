package chalklab

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/imagepull"
	"github.com/trevex/chalkos/pkg/imagesign"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/internal/poll"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/translate"
	"github.com/trevex/chalkos/pkg/lab"
	"github.com/trevex/chalkos/pkg/pki"
)

// platform is the platform of the images a lab runs.
const platform = "kvm"

// Files of a lab's state directory besides those pkg/lab keeps.
const (
	inventoryFile  = "chalkos.yaml"
	kubeconfigFile = "kubeconfig"
	clientFile     = "chalkctl.json"
)

// labValidity is how long the lab's client file and kubeconfig last: as long as a lab may run,
// beyond chalkctl's shorter defaults, since a lab is a test cluster on the workstation that runs it.
const labValidity = "8760h"

type createFlags struct {
	lab, secrets  string
	guestForwards []string
}

func (a *app) createCommand() *cobra.Command {
	var f createFlags
	cmd := a.command(&cobra.Command{
		Use:     "create",
		Short:   "Create a lab of the cluster's nodes, install them and bootstrap the cluster",
		Long:    createLong,
		Example: createExample,
		Args:    cli.NoArgs,
	}, func(a *app, ctx context.Context, pos []string) error { return a.create(ctx, f) })
	fs := cmd.Flags()
	fs.StringVar(&f.lab, "lab", "", "lab file naming the inventory and the nodes the lab runs (default "+defaultLabFile+" in the current directory)")
	fs.StringVar(&f.secrets, "secrets", "", "the cluster's secrets file, which chalkctl reads to install the nodes")
	cmd.MarkFlagRequired("secrets")
	fs.StringArrayVar(&f.guestForwards, "guest-forward", nil, "GUEST=HOST: make a host address (a local registry, for example) reachable at a guest address of the VMs' user-mode network; may be repeated")
	return cmd
}

func (a *app) create(ctx context.Context, f createFlags) (err error) {
	code, vars := os.Getenv("CHALKLAB_OVMF_CODE"), os.Getenv("CHALKLAB_OVMF_VARS")
	if code == "" || vars == "" {
		return errors.New("CHALKLAB_OVMF_CODE and CHALKLAB_OVMF_VARS name no firmware; run chalklab from its package, which sets them")
	}
	if err := checkKVM(); err != nil {
		return err
	}
	guestForwards, err := parseCreateFlags(&f)
	if err != nil {
		return err
	}
	l, inv, images, err := newLab(ctx, f.lab, code, guestForwards)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.Dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(l.Dir, 0o700); err != nil {
		return err
	}
	// Until the supervisor runs, nothing of the lab runs, so a failed create leaves nothing behind.
	started := false
	defer func() {
		if err != nil && !started {
			os.RemoveAll(l.Dir)
		}
	}()

	fmt.Fprintf(a.stdout, "creating the lab's Secure Boot keys in %s\n", filepath.Join(l.Dir, "keys"))
	if err := a.createKeys(ctx, filepath.Join(l.Dir, "keys"), l.Name, vars); err != nil {
		return err
	}
	if err := a.prepareImages(ctx, l, images); err != nil {
		return err
	}
	if err := prepareDisks(ctx, l); err != nil {
		return err
	}
	// The ports are picked last, so they are most likely still free when the VMs start.
	if err := assignPorts(l); err != nil {
		return err
	}
	if err := l.Write(); err != nil {
		return err
	}
	if err := writeLabInventory(filepath.Join(l.Dir, inventoryFile), inv, l); err != nil {
		return err
	}

	fmt.Fprintf(a.stdout, "starting %s\n", strings.Join(l.NodeNames(), ", "))
	started = true
	if err := a.startSupervisor(ctx, l.Dir); err != nil {
		return fmt.Errorf("%w; chalklab destroy removes what runs of the lab", err)
	}
	if err := a.setUp(ctx, f, l); err != nil {
		return fmt.Errorf("%w; the lab runs on: chalklab status shows it, chalklab destroy removes it", err)
	}
	return nil
}

// parseCreateFlags makes the secrets file's path absolute and parses the guest forwards. chalklab
// runs chalkctl several times, so the secrets file is a file, which every run reads.
func parseCreateFlags(f *createFlags) ([]lab.GuestForward, error) {
	if f.secrets == "" || f.secrets == "-" {
		return nil, errors.New("chalklab create needs the cluster's secrets file: pass --secrets FILE")
	}
	var err error
	if f.secrets, err = filepath.Abs(f.secrets); err != nil {
		return nil, err
	}
	if _, err := os.Stat(f.secrets); err != nil {
		return nil, fmt.Errorf("the secrets file: %w", err)
	}
	var guestForwards []lab.GuestForward
	for _, fw := range f.guestForwards {
		guest, host, ok := strings.Cut(fw, "=")
		if !ok || guest == "" || host == "" {
			return nil, fmt.Errorf("--guest-forward %q is not GUEST=HOST", fw)
		}
		guestForwards = append(guestForwards, lab.GuestForward{Guest: guest, Host: host})
	}
	return guestForwards, nil
}

// newLab reads the lab file at path, or the default one, and its inventory, and returns the lab
// they describe with each node's image as the inventory names it. The lab's state directory must
// not exist yet.
func newLab(ctx context.Context, path, code string, guestForwards []lab.GuestForward) (*lab.Lab, *inventory.Inventory, map[string]string, error) {
	if path == "" {
		path = defaultLabFile
	}
	lf, err := readLabFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	inv, err := inventory.Load(lf.inventory, translate.NodeDocuments)
	if err != nil {
		return nil, nil, nil, err
	}
	inv.SetContext(ctx)
	// chalkctl, which installs the nodes, refuses an inventory whose nodes hold different Cluster
	// or Flannel specs, or one holding two Cluster documents; such an inventory is refused before
	// the lab is made.
	documents := func(name string) ([]config.Document, error) {
		nc, err := inv.Node(name)
		if err != nil {
			return nil, err
		}
		return nc.Documents, nil
	}
	if _, err := translate.Endpoint(inv.NodeNames(), documents); err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", lf.inventory, err)
	}
	nodes, images, err := labNodes(inv, lf)
	if err != nil {
		return nil, nil, nil, err
	}
	dir, err := lab.StateDir(lf.name)
	if err != nil {
		return nil, nil, nil, err
	}
	l := &lab.Lab{Dir: dir, Name: lf.name, FirmwareCode: code, FirmwareVars: filepath.Join(dir, "keys", "OVMF_VARS.fd"), GuestForwards: guestForwards, Nodes: nodes}
	if err := l.CheckSockets(); err != nil {
		return nil, nil, nil, err
	}
	if _, err := os.Stat(dir); err == nil {
		return nil, nil, nil, fmt.Errorf("the lab %s exists in %s; chalklab destroy removes it", lf.name, dir)
	}
	return l, inv, images, nil
}

// prepareImages copies each node's image from its image directory, or from the lab's image cache
// for an image in a registry, into the lab and signs the copy with the lab's db key. images are
// the nodes' images as the inventory names them.
func (a *app) prepareImages(ctx context.Context, l *lab.Lab, images map[string]string) error {
	if err := os.Mkdir(filepath.Join(l.Dir, "images"), 0o700); err != nil {
		return err
	}
	for _, n := range l.Nodes {
		dir, err := a.imageDir(ctx, l, n, images[n.Name])
		if err != nil {
			return fmt.Errorf("the image of %s: %w", n.Name, err)
		}
		raw := filepath.Join(l.Dir, "images", n.Name+".raw")
		fmt.Fprintf(a.stdout, "copying the image of %s from %s\n", n.Name, dir)
		if err := copyImage(ctx, dir, raw); err != nil {
			return fmt.Errorf("the image of %s: %w", n.Name, err)
		}
		if err := signImage(ctx, raw, filepath.Join(l.Dir, "images", n.Name+".json"), filepath.Join(l.Dir, "keys")); err != nil {
			return fmt.Errorf("sign the image of %s: %w", n.Name, err)
		}
	}
	return nil
}

// imageDir is the directory of a node's image: the directory the inventory names, or for an image
// in a registry the lab's image cache, images/sha256-<hex> in its state directory, into which it
// pulls the image by the digest labNodes resolved unless the cache holds it. named is the image as
// the inventory names it, by tag or by digest.
func (a *app) imageDir(ctx context.Context, l *lab.Lab, n lab.Node, named string) (string, error) {
	if filepath.IsAbs(n.Image) {
		return n.Image, nil
	}
	_, digest, _ := strings.Cut(n.Image, "@")
	dir := filepath.Join(l.Dir, "images", strings.Replace(digest, ":", "-", 1))
	p, err := imagepull.Directory(ctx, n.Image, dir)
	if err != nil {
		return "", err
	}
	if p.Present {
		fmt.Fprintf(a.stdout, "using cached %s\n", p.Reference)
	} else {
		fmt.Fprintf(a.stdout, "pulled %s as %s\n", named, p.Reference)
	}
	return dir, nil
}

// prepareDisks creates each node's disks: the first an overlay on the node's image, the others
// empty.
func prepareDisks(ctx context.Context, l *lab.Lab) error {
	for _, n := range l.Nodes {
		if err := os.Mkdir(filepath.Join(l.Dir, n.Name), 0o700); err != nil {
			return err
		}
		if err := lab.CreateOverlay(ctx, filepath.Join(l.Dir, "images", n.Name+".raw"), l.DiskPath(n, 0), n.Disks[0].Size); err != nil {
			return err
		}
		for i, d := range n.Disks[1:] {
			if err := lab.CreateDisk(ctx, l.DiskPath(n, i+1), d.Size); err != nil {
				return err
			}
		}
	}
	return nil
}

// assignPorts picks a free port on 127.0.0.1 for each node's chalkd, for a control plane's API
// server, and for each further port the lab file forwards.
func assignPorts(l *lab.Lab) error {
	var err error
	for i := range l.Nodes {
		n := &l.Nodes[i]
		if n.Kind == kubernetes.KindControlPlane {
			if n.APIPort, err = lab.FreePort(); err != nil {
				return err
			}
		}
		if n.ChalkdPort, err = lab.FreePort(); err != nil {
			return err
		}
		for j := range n.Forwards {
			if n.Forwards[j].Host, err = lab.FreePort(); err != nil {
				return err
			}
		}
	}
	return nil
}

// labNodes returns the VMs of the lab file's nodes in name order, each with its image from the
// inventory, which must be a kvm image, and each node's image as the inventory names it. A VM's
// image is the image's directory, or its reference in a registry pinned to the digest its tag
// resolves to, once per reference. The lab file gives a VM its MAC address and machine, else its
// type's defaults.
func labNodes(inv *inventory.Inventory, lf *labFile) ([]lab.Node, map[string]string, error) {
	images := map[string]string{}
	macs := map[string]string{}
	var nodes []lab.Node
	for _, name := range slices.Sorted(maps.Keys(lf.nodes)) {
		m := lf.nodes[name]
		nc, err := inv.Node(name)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", lf.path, err)
		}
		md, err := nc.Resolve()
		if err != nil {
			return nil, nil, err
		}
		if md.Platform != platform {
			return nil, nil, fmt.Errorf("chalklab runs %s images, but the image %s of %s is built for %s", platform, nc.ImageName, name, md.Platform)
		}
		image, named := nc.ImageDir, nc.ImageDir
		if nc.ImageOCI != "" {
			a, err := nc.Artifact()
			if err != nil {
				return nil, nil, err
			}
			image, named = a.Reference, nc.ImageOCI
		}
		hw, err := net.ParseMAC(m.MAC)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: the MAC address of %s: %w", lf.path, name, err)
		}
		mac := hw.String()
		// The I/G bit of the first octet marks a group address, which is no valid address of a NIC.
		if hw[0]&1 != 0 {
			return nil, nil, fmt.Errorf("%s: the MAC address %s of %s is a multicast address; a NIC's address has the lowest bit of its first octet clear, such as 52:54:00:…", lf.path, mac, name)
		}
		if other, ok := macs[mac]; ok {
			return nil, nil, fmt.Errorf("%s gives %s and %s the same MAC address %s", lf.path, other, name, mac)
		}
		macs[mac] = name
		kind := translate.NodeType(nc.Documents)
		n := lab.Node{Name: name, Kind: kind, Image: image, MAC: mac, MemoryMB: 2048, CPUs: 2, Disks: []lab.Disk{{Size: "16G"}}}
		if kind == kubernetes.KindControlPlane {
			n.MemoryMB = 3072
		}
		if m.Memory != 0 {
			n.MemoryMB = m.Memory
		}
		if m.CPUs != 0 {
			n.CPUs = m.CPUs
		}
		if len(m.Disks) > 0 {
			n.Disks = nil
			for _, d := range m.Disks {
				n.Disks = append(n.Disks, lab.Disk{Size: d.Size, Serial: d.Serial})
			}
		}
		for _, p := range m.Forwards {
			switch {
			case p == lab.ChalkdPort:
				return nil, nil, fmt.Errorf("%s: chalklab forwards port %d of %s to its chalkd already", lf.path, p, name)
			case p == 6443 && kind == kubernetes.KindControlPlane:
				return nil, nil, fmt.Errorf("%s: chalklab forwards port %d of %s to its API server already", lf.path, p, name)
			}
			n.Forwards = append(n.Forwards, lab.Forward{Guest: p})
		}
		nodes = append(nodes, n)
		images[name] = named
	}
	return nodes, images, nil
}

// writeLabInventory writes the inventory chalkctl reaches the lab with: the lab file's inventory
// with its paths absolute, so it reads the same from the lab's state directory, its images in a
// registry pinned to the digests the lab's nodes booted, and its nodes those the lab runs, at
// their forwarded ports.
func writeLabInventory(path string, inv *inventory.Inventory, l *lab.Lab) error {
	spec := inv.Spec
	spec.Images = maps.Clone(inv.Spec.Images)
	spec.Nodes = map[string]inventory.Node{}
	for _, n := range l.Nodes {
		node := inv.Spec.Nodes[n.Name]
		node.Address = "127.0.0.1:" + strconv.Itoa(n.ChalkdPort)
		spec.Nodes[n.Name] = node
		name, source, err := inv.Image(n.Name)
		if err != nil {
			return err
		}
		if source.OCI != "" {
			spec.Images[name] = inventory.Image{Source: inventory.ImageSource{OCI: n.Image}}
		}
	}
	data, err := inventory.Encode(inv.Name, spec)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// rawAndPartitions finds the raw image in an image directory, or takes the file given, and the
// partition table describing it: repart-output.json next to a raw image, or an ISO's .json.
func rawAndPartitions(path string) (raw, partitions string, err error) {
	if raw, err = image.FindRaw(path); err != nil {
		return "", "", err
	}
	if _, err := os.Stat(raw + ".json"); err == nil {
		return raw, raw + ".json", nil
	}
	return raw, filepath.Join(filepath.Dir(raw), "repart-output.json"), nil
}

// copyImage copies the raw image of an image directory without materialising its holes, and its
// partition table to a .json file next to the copy.
func copyImage(ctx context.Context, src, dst string) error {
	raw, partitions, err := rawAndPartitions(src)
	if err != nil {
		return err
	}
	if err := lab.CopySparse(ctx, raw, dst); err != nil {
		return err
	}
	data, err := os.ReadFile(partitions)
	if err != nil {
		return err
	}
	return os.WriteFile(strings.TrimSuffix(dst, ".raw")+".json", data, 0o644)
}

// signImage signs the boot loader and the UKIs on the ESP of a raw image with the lab's db key.
func signImage(ctx context.Context, raw, partitions, keys string) error {
	parts, err := image.ReadPartitions(partitions)
	if err != nil {
		return err
	}
	esp, err := image.FindPartition(parts, "esp")
	if err != nil {
		return err
	}
	return imagesign.SignImage(raw, esp.Offset, filepath.Join(keys, "db.key"), filepath.Join(keys, "db.crt"))
}

// startSupervisor starts the lab's supervisor in a session of its own, so it outlives this
// command and the terminal, and waits until it runs every VM.
func (a *app) startSupervisor(ctx context.Context, dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(self, a.superviseArgs(dir)...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the supervisor: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	errNotReady := errors.New("the lab is not ready")
	err = poll.Until(ctx, "", 2*time.Minute, 100*time.Millisecond, func() error {
		if lab.Ready(dir) {
			return nil
		}
		select {
		case err := <-exited:
			out, _ := os.ReadFile(log.Name())
			return poll.Permanent{Err: fmt.Errorf("the supervisor ended: %v: %s", err, bytes.TrimSpace(out))}
		default:
			return errNotReady
		}
	})
	switch {
	case err == nil || !errors.Is(err, errNotReady):
		return err
	case ctx.Err() != nil:
		return ctx.Err()
	}
	return fmt.Errorf("the supervisor did not start the VMs in 2 minutes; see %s", log.Name())
}

// maintenanceRE is chalkd's line on the console in maintenance mode.
var maintenanceRE = regexp.MustCompile(`maintenance mode.*certificate fingerprint ([0-9a-f]{64})`)

// setUp writes an admin client file, installs every node, bootstraps the first control plane and
// writes a kubeconfig that reaches the lab through its forwarded ports. Polls use the client file,
// so an encrypted secrets file is decrypted once per command rather than per poll. Both files
// last labValidity.
func (a *app) setUp(ctx context.Context, f createFlags, l *lab.Lab) error {
	inventory := filepath.Join(l.Dir, inventoryFile)
	cluster := []string{"--inventory", inventory, "--secrets", f.secrets}
	if err := a.chalkctl(ctx, a.stdout, "client", "new", "--name", "chalklab", "--role", "admin", "--ttl", labValidity, "--out", filepath.Join(l.Dir, clientFile), "--secrets", f.secrets); err != nil {
		return err
	}
	for _, n := range l.Nodes {
		addr := "127.0.0.1:" + strconv.Itoa(n.ChalkdPort)
		fmt.Fprintf(a.stdout, "waiting for %s to come up in maintenance mode\n", n.Name)
		fp, err := maintenanceFingerprint(ctx, l, n)
		if err != nil {
			return err
		}
		// QEMU accepts the forwarded port before chalkd listens behind it.
		if err := chalkdAnswers(ctx, addr, fp); err != nil {
			return fmt.Errorf("%s: %w", n.Name, err)
		}
		fmt.Fprintf(a.stdout, "%s runs in maintenance mode with the certificate fingerprint %s\n", n.Name, fp)
		if err := a.chalkctl(ctx, a.stdout, append([]string{"install", n.Name, "--fingerprint", fp}, cluster...)...); err != nil {
			return fmt.Errorf("install %s: %w", n.Name, err)
		}
	}

	var cp *lab.Node
	for i, n := range l.Nodes {
		if n.Kind == kubernetes.KindControlPlane {
			cp = &l.Nodes[i]
			break
		}
	}
	if cp != nil {
		fmt.Fprintf(a.stdout, "waiting for %s to wait for its bootstrap\n", cp.Name)
		if err := a.waitBootstrap(ctx, cp.Name, []string{"--inventory", inventory, "--config", filepath.Join(l.Dir, clientFile)}); err != nil {
			return err
		}
		if err := a.chalkctl(ctx, a.stdout, append([]string{"bootstrap", cp.Name}, cluster...)...); err != nil {
			return err
		}
		if err := a.chalkctl(ctx, a.stdout, append([]string{"kubeconfig", "--ttl", labValidity, "--out", filepath.Join(l.Dir, kubeconfigFile), "--server", "https://127.0.0.1:" + strconv.Itoa(cp.APIPort)}, cluster...)...); err != nil {
			return err
		}
	}

	a.printNext(l, cp != nil)
	return nil
}

// printNext prints the commands that use the lab, after exporting KUBECONFIG (with Kubernetes),
// CHALKOSCONFIG and CHALKOSINVENTORY, so they name none of the files, and how to upgrade with a
// signed copy of an image.
func (a *app) printNext(l *lab.Lab, withKubernetes bool) {
	fmt.Fprintf(a.stdout, "\nthe lab %s runs %s; next:\n", l.Name, strings.Join(l.NodeNames(), ", "))
	if withKubernetes {
		fmt.Fprintf(a.stdout, "  export KUBECONFIG=%s\n", filepath.Join(l.Dir, kubeconfigFile))
	}
	fmt.Fprintf(a.stdout, "  export CHALKOSCONFIG=%s\n", filepath.Join(l.Dir, clientFile))
	fmt.Fprintf(a.stdout, "  export CHALKOSINVENTORY=%s\n", filepath.Join(l.Dir, inventoryFile))
	if withKubernetes {
		fmt.Fprintln(a.stdout, "  kubectl get nodes")
	}
	fmt.Fprintf(a.stdout, "  chalkctl status %s\n  chalklab status\n  chalklab destroy\n", l.Nodes[0].Name)
	// One or two control planes lose etcd's quorum while one reboots, which an upgrade accepts
	// only with --allow-downtime.
	controlPlanes := 0
	for _, n := range l.Nodes {
		if n.Kind == kubernetes.KindControlPlane {
			controlPlanes++
		}
	}
	var downtime string
	if controlPlanes == 1 || controlPlanes == 2 {
		downtime = " --allow-downtime"
	}
	fmt.Fprintf(a.stdout, "The lab's inventory names unsigned images, which its firmware refuses; upgrade with a copy\nsigned by the lab's db key:\n  chalklab sign <image dir> --out <signed dir>\n  chalkctl upgrade --image <signed dir>%s\n",
		downtime)
}

// maintenanceFingerprint waits for a node's chalkd to come up in maintenance mode, and returns
// the fingerprint of the certificate it prints on the console. It reads the console log alone, so
// QMP stays free for others.
func maintenanceFingerprint(ctx context.Context, l *lab.Lab, n lab.Node) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	c := l.VMConfig(n)
	if !lab.Running(c) {
		return "", fmt.Errorf("the VM of %s does not run; its console is in %s", n.Name, c.ConsolePath())
	}
	console, stop, err := lab.FollowConsole(c.ConsolePath())
	if err != nil {
		return "", err
	}
	defer stop()
	m, err := console.WaitFor(ctx, maintenanceRE)
	if err != nil {
		return "", fmt.Errorf("%s did not come up in maintenance mode: %w; its console is in %s", n.Name, err, c.ConsolePath())
	}
	return m[1], nil
}

// chalkdAnswers waits until chalkd answers at addr with the certificate of the fingerprint. It
// only reads the certificate, which the fingerprint the console printed verifies.
func chalkdAnswers(ctx context.Context, addr, fingerprint string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return poll.UntilDone(ctx, "", 2*time.Second, func() error {
		d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("chalkd at %s did not answer: %w", addr, err)
		}
		certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
		conn.Close()
		if len(certs) > 0 && pki.Fingerprint(certs[0].Raw) == fingerprint {
			return nil
		}
		return poll.Permanent{Err: fmt.Errorf("chalkd at %s serves another certificate than the one its console names", addr)}
	})
}

// waitBootstrap waits until the control plane's status says it waits for its bootstrap, and prints
// the line that says so, which names the cluster's endpoint.
func (a *app) waitBootstrap(ctx context.Context, name string, cluster []string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var last bytes.Buffer
	err := poll.UntilDone(ctx, "", 5*time.Second, func() error {
		last.Reset()
		if err := a.chalkctl(ctx, &last, append([]string{"status", name}, cluster...)...); err != nil {
			return err
		}
		for line := range strings.Lines(last.String()) {
			if strings.Contains(line, "waiting for the cluster at") {
				fmt.Fprintf(a.stdout, "%s: %s", name, line)
				return nil
			}
		}
		return errors.New("its status does not say so")
	})
	if err != nil {
		return fmt.Errorf("%s did not wait for its bootstrap: %w; its last status:\n%s", name, err, last.String())
	}
	return nil
}

// kvmDevice is the device the lab's VMs run on.
var kvmDevice = "/dev/kvm"

// checkKVM fails unless this user may run VMs on KVM: without it QEMU would emulate the CPU, far
// too slowly for a cluster.
func checkKVM() error {
	f, err := os.OpenFile(kvmDevice, os.O_RDWR, 0)
	switch {
	case err == nil:
		return f.Close()
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s does not exist: chalklab runs its VMs on KVM, which needs an x86-64 machine with virtualization enabled in its firmware and the kvm module loaded", kvmDevice)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("you may not open %s: chalklab runs its VMs on KVM; join the kvm group, as users.users.<you>.extraGroups = [ \"kvm\" ] does on NixOS, and log in again", kvmDevice)
	}
	return fmt.Errorf("open %s, which chalklab runs its VMs on (KVM): %w", kvmDevice, err)
}
