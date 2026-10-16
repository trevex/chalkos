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

	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/imagesign"
	"github.com/trevex/chalkos/pkg/inventory"
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

// listFlag is a flag that may be repeated.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }
func (l *listFlag) Type() string       { return "strings" }

type createFlags struct {
	lab, secrets  string
	guestForwards listFlag
}

func (a *app) createCommand() *cobra.Command {
	var f createFlags
	cmd := a.command(&cobra.Command{
		Use:     "create",
		Short:   "Create a lab of the cluster's nodes, install them and bootstrap the cluster",
		Long:    createLong,
		Example: createExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.create(ctx, f, pos) })
	fs := cmd.Flags()
	fs.StringVar(&f.lab, "lab", "", "lab file naming the inventory and the nodes the lab runs (default "+defaultLabFile+" in the current directory)")
	fs.StringVar(&f.secrets, "secrets", "", "the cluster's secrets file, which chalkctl reads to install the nodes")
	fs.Var(&f.guestForwards, "guest-forward", "GUEST=HOST: make a host address (a local registry, for example) reachable at a guest address of the VMs' user-mode network; may be repeated")
	return cmd
}

func (a *app) create(ctx context.Context, f createFlags, pos []string) (err error) {
	if len(pos) != 0 {
		return errors.New("usage: chalklab create --secrets FILE [--lab chalklab.yaml] [--guest-forward GUEST=HOST]")
	}
	code, vars := os.Getenv("CHALKLAB_OVMF_CODE"), os.Getenv("CHALKLAB_OVMF_VARS")
	if code == "" || vars == "" {
		return errors.New("CHALKLAB_OVMF_CODE and CHALKLAB_OVMF_VARS name no firmware; run chalklab from its package, which sets them")
	}
	if err := checkKVM(); err != nil {
		return err
	}
	// chalklab runs chalkctl several times, so the secrets file is a file, which every run reads.
	if f.secrets == "" || f.secrets == "-" {
		return errors.New("chalklab create needs the cluster's secrets file: pass --secrets FILE")
	}
	if f.secrets, err = filepath.Abs(f.secrets); err != nil {
		return err
	}
	if _, err := os.Stat(f.secrets); err != nil {
		return fmt.Errorf("the secrets file: %w", err)
	}
	var guestForwards []lab.GuestForward
	for _, fw := range f.guestForwards {
		guest, host, ok := strings.Cut(fw, "=")
		if !ok || guest == "" || host == "" {
			return fmt.Errorf("--guest-forward %q is not GUEST=HOST", fw)
		}
		guestForwards = append(guestForwards, lab.GuestForward{Guest: guest, Host: host})
	}
	path := f.lab
	if path == "" {
		path = defaultLabFile
	}
	lf, err := readLabFile(path)
	if err != nil {
		return err
	}
	inv, err := inventory.Load(lf.inventory)
	if err != nil {
		return err
	}
	name, _, err := inv.Cluster()
	if err != nil {
		return fmt.Errorf("%s: %w", lf.inventory, err)
	}
	nodes, images, err := labNodes(inv, lf)
	if err != nil {
		return err
	}
	dir, err := lab.StateDir(name)
	if err != nil {
		return err
	}
	l := &lab.Lab{Cluster: name, FirmwareCode: code, FirmwareVars: filepath.Join(dir, "keys", "OVMF_VARS.fd"), GuestForwards: guestForwards, Nodes: nodes}
	if err := l.CheckSockets(dir); err != nil {
		return err
	}
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("the lab of %s exists in %s; chalklab destroy removes it", name, dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	// Until the supervisor runs, nothing of the lab runs, so a failed create leaves nothing behind.
	started := false
	defer func() {
		if err != nil && !started {
			os.RemoveAll(dir)
		}
	}()

	fmt.Fprintf(a.stdout, "creating the lab's Secure Boot keys in %s\n", filepath.Join(dir, "keys"))
	if err := a.createKeys(ctx, filepath.Join(dir, "keys"), name, vars); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(dir, "images"), 0o700); err != nil {
		return err
	}
	for _, n := range l.Nodes {
		raw := filepath.Join(dir, "images", n.Name+".raw")
		fmt.Fprintf(a.stdout, "copying the image of %s from %s\n", n.Name, images[n.Name])
		if err := copyImage(ctx, images[n.Name], raw); err != nil {
			return fmt.Errorf("the image of %s: %w", n.Name, err)
		}
		if err := signImage(ctx, raw, filepath.Join(dir, "images", n.Name+".json"), filepath.Join(dir, "keys")); err != nil {
			return fmt.Errorf("sign the image of %s: %w", n.Name, err)
		}
	}
	for _, n := range l.Nodes {
		if err := os.Mkdir(filepath.Join(dir, n.Name), 0o700); err != nil {
			return err
		}
		if err := lab.CreateOverlay(ctx, filepath.Join(dir, "images", n.Name+".raw"), lab.DiskPath(dir, n, 0), n.Disks[0].Size); err != nil {
			return err
		}
		for i, d := range n.Disks[1:] {
			if err := lab.CreateDisk(ctx, lab.DiskPath(dir, n, i+1), d.Size); err != nil {
				return err
			}
		}
	}
	// The ports are picked last, so they are most likely still free when the VMs start.
	for i := range l.Nodes {
		n := &l.Nodes[i]
		if n.Kind == identity.KindControlPlane {
			if n.APIPort, err = lab.FreePort(); err != nil {
				return err
			}
		}
		if n.ChalkdPort, err = lab.FreePort(); err != nil {
			return err
		}
	}
	if err := l.Write(dir); err != nil {
		return err
	}
	if err := writeLabInventory(filepath.Join(dir, inventoryFile), inv, l); err != nil {
		return err
	}

	fmt.Fprintf(a.stdout, "starting %s\n", strings.Join(nodeNames(l), ", "))
	started = true
	if err := a.startSupervisor(ctx, dir); err != nil {
		return fmt.Errorf("%w; chalklab destroy removes what runs of the lab", err)
	}
	if err := a.setUp(ctx, f, dir, l); err != nil {
		return fmt.Errorf("%w; the lab runs on: chalklab status shows it, chalklab destroy removes it", err)
	}
	return nil
}

// labNodes are the VMs of the lab file's nodes, in name order, with each node's image directory:
// its Image's, which must hold a kvm image. A VM gets the MAC address the lab file gives it, and
// the machine the lab file gives it or else its type's: 3072 MiB for a control plane, 2048 MiB
// otherwise, 2 CPUs and one disk of 16G.
func labNodes(inv *inventory.Inventory, lf *labFile) ([]lab.LabNode, map[string]string, error) {
	images := map[string]string{}
	macs := map[string]string{}
	var nodes []lab.LabNode
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
			return nil, nil, fmt.Errorf("chalklab runs %s images, but the Image %s of %s is built for %s", platform, nc.ImageName, name, md.Platform)
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
		n := lab.LabNode{Name: name, Kind: nc.Type, MAC: mac, MemoryMB: 2048, CPUs: 2, Disks: []lab.LabDisk{{Size: "16G"}}}
		if nc.Type == identity.KindControlPlane {
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
				n.Disks = append(n.Disks, lab.LabDisk{Size: d.Size, Serial: d.Serial})
			}
		}
		nodes = append(nodes, n)
		images[name] = nc.ImageDir
	}
	return nodes, images, nil
}

// writeLabInventory writes the inventory chalkctl reaches the lab with: the lab file's inventory
// with its paths absolute, so it reads the same from the lab's state directory, and its nodes those
// the lab runs, at their forwarded ports.
func writeLabInventory(path string, inv *inventory.Inventory, l *lab.Lab) error {
	spec := inv.Spec
	spec.Nodes = map[string]inventory.Node{}
	for _, n := range l.Nodes {
		node := inv.Spec.Nodes[n.Name]
		node.Address = "127.0.0.1:" + strconv.Itoa(n.ChalkdPort)
		spec.Nodes[n.Name] = node
	}
	data, err := inventory.Encode(inv.Name, spec)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func nodeNames(l *lab.Lab) []string {
	var names []string
	for _, n := range l.Nodes {
		names = append(names, n.Name)
	}
	return names
}

// imageFiles finds the raw image in an image directory, or takes the file given, and the
// partition table describing it: repart-output.json next to a raw image, or an ISO's .json.
func imageFiles(path string) (raw, partitions string, err error) {
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
	if _, err := os.Stat(raw + ".json"); err == nil {
		return raw, raw + ".json", nil
	}
	return raw, filepath.Join(filepath.Dir(raw), "repart-output.json"), nil
}

// copyImage copies the raw image of an image directory without materialising its holes, and its
// partition table to a .json file next to the copy.
func copyImage(ctx context.Context, src, dst string) error {
	raw, partitions, err := imageFiles(src)
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
	return imagesign.SignImage(ctx, raw, esp.Offset, filepath.Join(keys, "db.key"), filepath.Join(keys, "db.crt"))
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
	deadline := time.After(2 * time.Minute)
	for !lab.Ready(dir) {
		select {
		case err := <-exited:
			out, _ := os.ReadFile(log.Name())
			return fmt.Errorf("the supervisor ended: %v: %s", err, bytes.TrimSpace(out))
		case <-deadline:
			return fmt.Errorf("the supervisor did not start the VMs in 2 minutes; see %s", log.Name())
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

// maintenanceRE is chalkd's line on the console in maintenance mode.
var maintenanceRE = regexp.MustCompile(`maintenance mode.*certificate fingerprint ([0-9a-f]{64})`)

// setUp writes an admin client file, installs each node in place, bootstraps the first control
// plane, and writes a kubeconfig that reaches the lab through its forwarded ports. chalkctl reads
// the lab's inventory, which names the nodes at their forwarded ports, and the secrets file, and
// checks images against the lab's db certificate, which signs them. The wait for the bootstrap
// polls with the client file, so an encrypted secrets file is decrypted once per command rather
// than once per poll.
func (a *app) setUp(ctx context.Context, f createFlags, dir string, l *lab.Lab) error {
	inventory := filepath.Join(dir, inventoryFile)
	cluster := []string{"--inventory", inventory, "--secrets", f.secrets}
	if err := a.chalkctl(ctx, a.stdout, append([]string{"config", "new", "--name", "chalklab", "--role", "admin", "--out", filepath.Join(dir, clientFile)}, cluster...)...); err != nil {
		return err
	}
	for _, n := range l.Nodes {
		addr := "127.0.0.1:" + strconv.Itoa(n.ChalkdPort)
		fmt.Fprintf(a.stdout, "waiting for %s to come up in maintenance mode\n", n.Name)
		fp, err := maintenanceFingerprint(ctx, l, dir, n)
		if err != nil {
			return err
		}
		// QEMU accepts the forwarded port before chalkd listens behind it.
		if err := chalkdAnswers(ctx, addr, fp); err != nil {
			return fmt.Errorf("%s: %w", n.Name, err)
		}
		fmt.Fprintf(a.stdout, "%s runs in maintenance mode with the certificate fingerprint %s\n", n.Name, fp)
		if err := a.chalkctl(ctx, a.stdout, append([]string{"install", n.Name, "--fingerprint", fp, "--sign-cert", filepath.Join(dir, "keys", "db.crt")}, cluster...)...); err != nil {
			return fmt.Errorf("install %s: %w", n.Name, err)
		}
	}

	var cp *lab.LabNode
	for i, n := range l.Nodes {
		if n.Kind == identity.KindControlPlane {
			cp = &l.Nodes[i]
			break
		}
	}
	if cp != nil {
		fmt.Fprintf(a.stdout, "waiting for %s to wait for its bootstrap\n", cp.Name)
		if err := a.waitBootstrap(ctx, cp.Name, []string{"--inventory", inventory, "--config", filepath.Join(dir, clientFile)}); err != nil {
			return err
		}
		if err := a.chalkctl(ctx, a.stdout, append([]string{"bootstrap", cp.Name}, cluster...)...); err != nil {
			return err
		}
		if err := a.chalkctl(ctx, a.stdout, append([]string{"kubeconfig", "--out", filepath.Join(dir, kubeconfigFile), "--server", "https://127.0.0.1:" + strconv.Itoa(cp.APIPort)}, cluster...)...); err != nil {
			return err
		}
	}

	a.printNext(dir, l, cp != nil)
	return nil
}

// printNext prints the commands that use the lab: the kubeconfig when a control plane was
// bootstrapped, and the client file as $CHALKOSCONFIG, which chalkctl reads when no --config or
// --secrets is given, so the commands after it name neither. The lab's Image documents name the
// unsigned builds, of which the lab signed copies, so an upgrade signs the images again.
func (a *app) printNext(dir string, l *lab.Lab, kubernetes bool) {
	inventory := filepath.Join(dir, inventoryFile)
	fmt.Fprintf(a.stdout, "\nthe lab of %s runs %s; next:\n", l.Cluster, strings.Join(nodeNames(l), ", "))
	if kubernetes {
		fmt.Fprintf(a.stdout, "  export KUBECONFIG=%s\n", filepath.Join(dir, kubeconfigFile))
	}
	fmt.Fprintf(a.stdout, "  export CHALKOSCONFIG=%s\n", filepath.Join(dir, clientFile))
	if kubernetes {
		fmt.Fprintln(a.stdout, "  kubectl get nodes")
	}
	fmt.Fprintf(a.stdout, "  chalkctl status %s --inventory %s\n  chalklab status\n  chalklab destroy\n", l.Nodes[0].Name, inventory)
	keys := filepath.Join(dir, "keys")
	// One or two control planes lose etcd's quorum while one reboots, which an upgrade accepts
	// only with --allow-downtime.
	controlPlanes := 0
	for _, n := range l.Nodes {
		if n.Kind == identity.KindControlPlane {
			controlPlanes++
		}
	}
	var downtime string
	if controlPlanes == 1 || controlPlanes == 2 {
		downtime = " --allow-downtime"
	}
	fmt.Fprintf(a.stdout, "The lab's Image documents name unsigned images, which the lab's firmware refuses: upgrade with\nthe lab's db key, or with --image of an image chalklab sign signed:\n  chalkctl upgrade --inventory %s%s \\\n    --sign-key %s --sign-cert %s\n",
		inventory, downtime, filepath.Join(keys, "db.key"), filepath.Join(keys, "db.crt"))
}

// maintenanceFingerprint waits for a node's chalkd to come up in maintenance mode, and returns
// the fingerprint of the certificate it prints on the console. It reads the console log alone, so
// QMP stays free for others.
func maintenanceFingerprint(ctx context.Context, l *lab.Lab, dir string, n lab.LabNode) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	c := l.VMConfig(dir, n)
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
	for {
		d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
			conn.Close()
			if len(certs) > 0 && pki.Fingerprint(certs[0].Raw) == fingerprint {
				return nil
			}
			return fmt.Errorf("chalkd at %s serves another certificate than the one its console names", addr)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("chalkd at %s did not answer: %w", addr, err)
		case <-time.After(2 * time.Second):
		}
	}
}

// waitBootstrap waits until the control plane's status says it waits for its bootstrap, and prints
// the line that says so, which names the cluster's endpoint.
func (a *app) waitBootstrap(ctx context.Context, name string, cluster []string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var last bytes.Buffer
	for {
		last.Reset()
		if err := a.chalkctl(ctx, &last, append([]string{"status", name}, cluster...)...); err == nil {
			for line := range strings.Lines(last.String()) {
				if strings.Contains(line, "waiting for bootstrap") {
					fmt.Fprintf(a.stdout, "%s: %s", name, line)
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s did not wait for its bootstrap: %w; its last status:\n%s", name, ctx.Err(), last.String())
		case <-time.After(5 * time.Second):
		}
	}
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
