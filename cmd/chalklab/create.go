package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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

	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/imagesign"
	"github.com/trevex/chalkos/pkg/lab"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

// platform is the platform of the images a lab runs.
const platform = "kvm"

// Files of a lab's state directory besides those pkg/lab keeps.
const (
	manifestFile   = "manifest.json"
	kubeconfigFile = "kubeconfig"
	clientFile     = "chalkctl.json"
)

// listFlag is a flag that may be repeated.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

type createFlags struct {
	flake, cluster, manifest, secrets string
	images, guestForwards             listFlag
	nodes                             string
	cpus, controlPlaneMemory, memory  int
	diskSize                          string
}

func (a *app) create(ctx context.Context, args []string) (err error) {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	var f createFlags
	fs.StringVar(&f.flake, "flake", ".", "directory of the flake that defines the cluster")
	fs.StringVar(&f.cluster, "cluster", "", "cluster to use when the flake defines several")
	fs.StringVar(&f.manifest, "manifest", "", "read the cluster's manifest from this file instead of evaluating the flake; every role needs --image then")
	fs.StringVar(&f.secrets, "secrets", "", "secrets file chalkctl reads (default secrets.age, else secrets.json, in the flake directory)")
	fs.Var(&f.images, "image", "ROLE=DIR: the kvm image of a role, as nix build makes it, instead of building it; may be repeated")
	fs.StringVar(&f.nodes, "nodes", "", "comma-separated nodes to run (default every node of the cluster)")
	fs.IntVar(&f.cpus, "cpus", 2, "virtual CPUs of each VM")
	fs.IntVar(&f.controlPlaneMemory, "controlplane-memory", 3072, "memory of a control plane's VM, in MiB")
	fs.IntVar(&f.memory, "memory", 2048, "memory of the other VMs, in MiB")
	fs.StringVar(&f.diskSize, "disk-size", "16G", "size of each VM's sparse disk")
	fs.Var(&f.guestForwards, "guest-forward", "GUEST=HOST: make a host address, such as a local registry, reachable at a guest address of the VMs' user-mode network; may be repeated")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || f.cpus < 1 || f.controlPlaneMemory < 1 || f.memory < 1 {
		return errors.New("usage: chalklab create [--flake .] [--cluster NAME] [--nodes N,...] [--cpus 2] [--controlplane-memory 3072] [--memory 2048] [--disk-size 16G]")
	}
	code, vars := os.Getenv("CHALKLAB_OVMF_CODE"), os.Getenv("CHALKLAB_OVMF_VARS")
	if code == "" || vars == "" {
		return errors.New("CHALKLAB_OVMF_CODE and CHALKLAB_OVMF_VARS name no firmware; run chalklab from its package, which sets them")
	}
	images := map[string]string{}
	for _, img := range f.images {
		role, dir, ok := strings.Cut(img, "=")
		if !ok || role == "" || dir == "" {
			return fmt.Errorf("--image %q is not ROLE=DIR", img)
		}
		images[role] = dir
	}
	var guestForwards []lab.GuestForward
	for _, fw := range f.guestForwards {
		guest, host, ok := strings.Cut(fw, "=")
		if !ok || guest == "" || host == "" {
			return fmt.Errorf("--guest-forward %q is not GUEST=HOST", fw)
		}
		guestForwards = append(guestForwards, lab.GuestForward{Guest: guest, Host: host})
	}

	m, data, attr, err := a.loadManifest(ctx, f)
	if err != nil {
		return err
	}
	var named []string
	if f.nodes != "" {
		named = strings.Split(f.nodes, ",")
	}
	nodes, err := labNodes(m, named)
	if err != nil {
		return err
	}
	dir, err := lab.StateDir(m.Cluster.Name)
	if err != nil {
		return err
	}
	l := &lab.Lab{Cluster: m.Cluster.Name, FirmwareCode: code, FirmwareVars: filepath.Join(dir, "keys", "OVMF_VARS.fd"), GuestForwards: guestForwards}
	for _, n := range nodes {
		n.CPUs, n.MemoryMB = f.cpus, f.memory
		if n.Kind == manifest.KindControlPlane {
			n.MemoryMB = f.controlPlaneMemory
			if n.APIPort, err = lab.FreePort(); err != nil {
				return err
			}
		}
		if n.ChalkdPort, err = lab.FreePort(); err != nil {
			return err
		}
		l.Nodes = append(l.Nodes, n)
	}
	if err := l.CheckSockets(dir); err != nil {
		return err
	}
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("the lab of %s exists in %s; chalklab destroy removes it", m.Cluster.Name, dir)
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
	if err := os.WriteFile(filepath.Join(dir, manifestFile), data, 0o600); err != nil {
		return err
	}

	roles := map[string]bool{}
	for _, n := range l.Nodes {
		roles[n.Role] = true
	}
	for _, role := range slices.Sorted(maps.Keys(roles)) {
		if images[role] != "" {
			continue
		}
		if attr == "" {
			return fmt.Errorf("the manifest was read from a file, so the image of the role %s cannot be built; pass --image %s=DIR", role, role)
		}
		fmt.Fprintf(a.stdout, "building the %s image of %s\n", platform, role)
		if images[role], err = a.buildImage(ctx, f.flake, attr, m, role); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.stdout, "creating the lab's Secure Boot keys in %s\n", filepath.Join(dir, "keys"))
	if err := a.createKeys(ctx, filepath.Join(dir, "keys"), m.Cluster.Name, vars); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(dir, "images"), 0o700); err != nil {
		return err
	}
	for _, role := range slices.Sorted(maps.Keys(roles)) {
		raw := filepath.Join(dir, "images", role+".raw")
		if err := copyImage(ctx, images[role], raw); err != nil {
			return fmt.Errorf("the image of %s: %w", role, err)
		}
		if err := signImage(ctx, raw, filepath.Join(dir, "images", role+".json"), filepath.Join(dir, "keys")); err != nil {
			return fmt.Errorf("sign the image of %s: %w", role, err)
		}
	}
	for _, n := range l.Nodes {
		if err := os.Mkdir(filepath.Join(dir, n.Name), 0o700); err != nil {
			return err
		}
		if err := lab.CreateOverlay(ctx, filepath.Join(dir, "images", n.Role+".raw"), filepath.Join(dir, n.Name, "disk.qcow2"), f.diskSize); err != nil {
			return err
		}
	}
	if err := l.Write(dir); err != nil {
		return err
	}

	fmt.Fprintf(a.stdout, "starting %s\n", strings.Join(nodeNames(l), ", "))
	started = true
	if err := startSupervisor(ctx, dir); err != nil {
		return fmt.Errorf("%w; chalklab destroy removes what runs of the lab", err)
	}
	if err := a.setUp(ctx, f, dir, l); err != nil {
		return fmt.Errorf("%w; the lab runs on: chalklab status shows it, chalklab destroy removes it", err)
	}
	return nil
}

// loadManifest evaluates the cluster's manifest, or reads it from --manifest, and returns it, as
// JSON too, and the flake attribute of the cluster, empty for a manifest file.
func (a *app) loadManifest(ctx context.Context, f createFlags) (*manifest.Manifest, []byte, string, error) {
	var data []byte
	var attr string
	if f.manifest != "" {
		var err error
		if data, err = os.ReadFile(f.manifest); err != nil {
			return nil, nil, "", err
		}
	} else {
		name := f.cluster
		if name == "" {
			out, err := a.output(ctx, "nix", "eval", "--json", f.flake+"#chalkos", "--apply", "builtins.attrNames")
			if err != nil {
				return nil, nil, "", err
			}
			var names []string
			if err := json.Unmarshal(out, &names); err != nil {
				return nil, nil, "", fmt.Errorf("list the flake's clusters: %w", err)
			}
			if len(names) != 1 {
				return nil, nil, "", fmt.Errorf("the flake %s defines the clusters %v; choose one with --cluster", f.flake, names)
			}
			name = names[0]
		}
		quoted, err := attrName(name)
		if err != nil {
			return nil, nil, "", err
		}
		attr = "chalkos." + quoted
		fmt.Fprintf(a.stdout, "evaluating %s#%s\n", f.flake, attr)
		if data, err = a.output(ctx, "nix", "eval", "--json", f.flake+"#"+attr+".manifest"); err != nil {
			return nil, nil, "", err
		}
	}
	m, err := manifest.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, nil, "", err
	}
	return m, data, attr, nil
}

// buildImage builds the kvm image of a role and returns the directory holding it.
func (a *app) buildImage(ctx context.Context, flake, attr string, m *manifest.Manifest, role string) (string, error) {
	if _, ok := m.Roles[role].Images[platform]; !ok {
		return "", fmt.Errorf("the cluster builds no %s image of the role %s", platform, role)
	}
	quoted, err := attrName(role)
	if err != nil {
		return "", err
	}
	out, err := a.output(ctx, "nix", "build", "--no-link", "--print-out-paths", flake+"#"+attr+".roles."+quoted+".images."+platform+"^out")
	if err != nil {
		return "", err
	}
	paths := strings.Fields(string(out))
	if len(paths) != 1 {
		return "", fmt.Errorf("nix build printed %d paths for the image of %s, want one", len(paths), role)
	}
	return paths[0], nil
}

// attrName quotes a name as one component of a flake attribute path, as chalkctl does.
func attrName(name string) (string, error) {
	if strings.Contains(name, `"`) {
		return "", fmt.Errorf("the name %q contains a double quote, which a Nix attribute path cannot express", name)
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(name); i++ {
		c := name[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}

// labNodes are the cluster's nodes the lab runs, those named or all, sorted: nodes on kvm, each
// with the MAC address of its NIC on the lab network.
func labNodes(m *manifest.Manifest, named []string) ([]lab.LabNode, error) {
	if len(named) == 0 {
		named = slices.Sorted(maps.Keys(m.Nodes))
	}
	if len(named) == 0 {
		return nil, fmt.Errorf("the cluster %s has no nodes", m.Cluster.Name)
	}
	var nodes []lab.LabNode
	macs := map[string]string{}
	for _, name := range slices.Sorted(slices.Values(named)) {
		n, ok := m.Nodes[name]
		if !ok {
			return nil, fmt.Errorf("the cluster %s has no node %s", m.Cluster.Name, name)
		}
		if n.Platform != platform {
			return nil, fmt.Errorf("chalklab runs %s images, but %s is declared on %s (chalkos.nodes.%s.platform)", platform, name, n.Platform, name)
		}
		mac, err := labMAC(name, n.Identity)
		if err != nil {
			return nil, err
		}
		if other, ok := macs[mac]; ok {
			return nil, fmt.Errorf("%s and %s match the same MAC address %s", other, name, mac)
		}
		macs[mac] = name
		nodes = append(nodes, lab.LabNode{Name: name, Role: n.Role, Kind: m.Roles[n.Role].Kind, MAC: mac})
	}
	return nodes, nil
}

// labMAC is the MAC address the lab gives a node's NIC on the lab network: the one address its
// networks match by. The lab network carries the node's static addresses, as its definition
// configures them; the user-mode NIC of each VM is configured by DHCP.
func labMAC(name string, id manifest.Identity) (string, error) {
	networks, _ := id.Network["networks"].(map[string]any)
	var macs []string
	for _, key := range slices.Sorted(maps.Keys(networks)) {
		network, _ := networks[key].(map[string]any)
		match, _ := network["matchConfig"].(map[string]any)
		if mac, ok := match["MACAddress"].(string); ok {
			hw, err := net.ParseMAC(mac)
			if err != nil || len(hw) != 6 {
				return "", fmt.Errorf("chalkos.nodes.%s.network.networks.%s matches %q, not one MAC address", name, key, mac)
			}
			macs = append(macs, hw.String())
		}
	}
	slices.Sort(macs)
	switch macs = slices.Compact(macs); len(macs) {
	case 1:
		return macs[0], nil
	case 0:
		return "", fmt.Errorf("chalklab connects %s to the lab network by the MAC address one of its networks matches (matchConfig.MACAddress), and chalkos.nodes.%s.network matches none", name, name)
	}
	return "", fmt.Errorf("chalklab connects %s to the lab network by the MAC address one of its networks matches, and chalkos.nodes.%s.network matches several: %s", name, name, strings.Join(macs, ", "))
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
func startSupervisor(ctx context.Context, dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(self, "supervise", dir)
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

// setUp installs each node in place, bootstraps the first control plane, and writes a kubeconfig
// and a client file that reach the lab through its forwarded ports.
func (a *app) setUp(ctx context.Context, f createFlags, dir string, l *lab.Lab) error {
	cluster := []string{"--manifest", filepath.Join(dir, manifestFile), "--flake", f.flake}
	if f.secrets != "" {
		cluster = append(cluster, "--secrets", f.secrets)
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
		if err := a.chalkctl(ctx, a.stdout, append([]string{"install", n.Name, "--fingerprint", fp, "--endpoint", addr}, cluster...)...); err != nil {
			return fmt.Errorf("install %s: %w", n.Name, err)
		}
	}

	var cp *lab.LabNode
	for i, n := range l.Nodes {
		if n.Kind == manifest.KindControlPlane {
			cp = &l.Nodes[i]
			break
		}
	}
	if cp != nil {
		addr := "127.0.0.1:" + strconv.Itoa(cp.ChalkdPort)
		fmt.Fprintf(a.stdout, "waiting for %s to wait for its bootstrap\n", cp.Name)
		if err := a.waitBootstrap(ctx, cp.Name, addr, cluster); err != nil {
			return err
		}
		if err := a.chalkctl(ctx, a.stdout, append([]string{"bootstrap", cp.Name, "--endpoint", addr}, cluster...)...); err != nil {
			return err
		}
		if err := a.chalkctl(ctx, a.stdout, append([]string{"kubeconfig", "--out", filepath.Join(dir, kubeconfigFile), "--server", "https://127.0.0.1:" + strconv.Itoa(cp.APIPort)}, cluster...)...); err != nil {
			return err
		}
	}
	if err := a.chalkctl(ctx, a.stdout, append([]string{"config", "new", "--name", "chalklab", "--role", "admin", "--out", filepath.Join(dir, clientFile)}, cluster...)...); err != nil {
		return err
	}
	if err := forwardedClientFile(filepath.Join(dir, clientFile), l); err != nil {
		return err
	}

	fmt.Fprintf(a.stdout, "\nthe lab of %s runs %s; next:\n", l.Cluster, strings.Join(nodeNames(l), ", "))
	if cp != nil {
		fmt.Fprintf(a.stdout, "  export KUBECONFIG=%s\n  kubectl get nodes\n", filepath.Join(dir, kubeconfigFile))
	}
	fmt.Fprintf(a.stdout, "  chalkctl status %s --config %s\n  chalklab status\n  chalklab destroy\n", l.Nodes[0].Name, filepath.Join(dir, clientFile))
	return nil
}

// maintenanceFingerprint waits for a node's chalkd to come up in maintenance mode, and returns
// the fingerprint of the certificate it prints on the console.
func maintenanceFingerprint(ctx context.Context, l *lab.Lab, dir string, n lab.LabNode) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	vm, err := lab.AttachVM(ctx, l.VMConfig(dir, n))
	if err != nil {
		return "", err
	}
	defer vm.Detach()
	m, err := vm.Console.WaitFor(ctx, maintenanceRE)
	if err != nil {
		return "", fmt.Errorf("%s did not come up in maintenance mode: %w; its console is in %s", n.Name, err, vm.Config.ConsolePath())
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

// waitBootstrap waits until the control plane's status says it waits for its bootstrap.
func (a *app) waitBootstrap(ctx context.Context, name, addr string, cluster []string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var last bytes.Buffer
	for {
		last.Reset()
		if err := a.chalkctl(ctx, &last, append([]string{"status", name, "--endpoint", addr}, cluster...)...); err == nil && strings.Contains(last.String(), "waiting for bootstrap") {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s did not wait for its bootstrap: %w; its last status:\n%s", name, ctx.Err(), last.String())
		case <-time.After(5 * time.Second):
		}
	}
}

// forwardedClientFile makes the client file name the lab's nodes at their forwarded ports.
func forwardedClientFile(path string, l *lab.Lab) error {
	c, err := client.ReadConfig(path)
	if err != nil {
		return err
	}
	c.Nodes = map[string]string{}
	for _, n := range l.Nodes {
		c.Nodes[n.Name] = "127.0.0.1:" + strconv.Itoa(n.ChalkdPort)
	}
	data, err := c.Encode()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
