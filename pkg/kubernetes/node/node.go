// Package node prepares a node's Kubernetes files at boot: the node's address, the kubelet's
// credentials, kubeconfig and flags from the node's share and identity, and on control-plane
// nodes the control plane's certificates and, once bootstrapped, its static pods.
package node

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/manifests"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// Paths are the files Prepare reads and writes; tests point them at temporary directories.
type Paths struct {
	// State is the Kubernetes directory on STATE, holding the share and the bootstrap and etcd
	// markers.
	State string
	// Cluster is the image's cluster file.
	Cluster string
	// NodeFile is the node's identity without secrets.
	NodeFile string
	// Run receives the kubelet's files, the certificates and the static pods.
	Run string
	// PKI is where the static pods find the certificates.
	PKI string
	// KubeletPKI is the kubelet's certificate directory on VAR.
	KubeletPKI string
	// EtcdData is etcd's data directory on VAR.
	EtcdData string
}

// DefaultPaths are the paths on a node.
func DefaultPaths() Paths {
	return Paths{
		State:      "/state/kubernetes",
		Cluster:    "/etc/chalkos/kubernetes/cluster.json",
		NodeFile:   "/run/chalkos/node.json",
		Run:        "/run/chalkos/kubernetes",
		PKI:        manifests.PKIDir,
		KubeletPKI: "/var/lib/kubelet/pki",
		EtcdData:   manifests.EtcdDataDir,
	}
}

func (p Paths) Share() string        { return filepath.Join(p.State, "share.json") }
func (p Paths) Bootstrapped() string { return filepath.Join(p.State, "bootstrapped") }
func (p Paths) Manifests() string    { return filepath.Join(p.Run, "manifests") }
func (p Paths) KubeletDir() string   { return filepath.Join(p.Run, "kubelet") }

// NodeIP holds the addresses the node picked, one per line, the primary family's first.
func (p Paths) NodeIP() string { return filepath.Join(p.Run, "node-ip") }

// VXLAN holds what the firewall's VXLAN rule accepts: a line "destination <address> <mode>" for
// each of the node's addresses, where mode is interface or any, and a line "source <range>" for
// each range VXLAN must come from.
func (p Paths) VXLAN() string { return filepath.Join(p.Run, "vxlan") }

// Pin holds the addresses a control-plane node was pinned to when it became an etcd member: its
// etcd peers and the certificates know it by them, so every later boot uses exactly these.
func (p Paths) Pin() string { return filepath.Join(p.State, "node-ip") }

// PrepareError holds why the last preparation failed, such as that no address of the node
// matched.
func (p Paths) PrepareError() string { return filepath.Join(p.Run, "prepare.error") }

// Prepared marks that Prepare finished: chalkd starts before it at boot and must not read the
// files it is still writing.
func (p Paths) Prepared() string { return filepath.Join(p.Run, "prepared") }

// Left marks that the node left etcd: it joins the cluster again only once reinstalled.
func (p Paths) Left() string { return filepath.Join(p.State, "left") }

// EtcdInitialCluster holds etcd's initial cluster when the node joined an existing cluster.
func (p Paths) EtcdInitialCluster() string { return filepath.Join(p.State, "etcd-initial-cluster") }

// EtcdInitialised marks that etcd answered ready after the bootstrap, so its data exists.
func (p Paths) EtcdInitialised() string { return filepath.Join(p.State, "etcd-initialised") }

// Joining marks that the node started joining the cluster at the endpoint it holds: it may be an
// etcd member from then on, so it never bootstraps a cluster of its own.
func (p Paths) Joining() string { return filepath.Join(p.State, "joining") }

// Kubeconfig is the kubelet's kubeconfig, which chalkd also reads the Node with.
func (p Paths) Kubeconfig() string { return filepath.Join(p.KubeletDir(), "kubeconfig") }

// kubeletClient is the file the kubelet's certificate store keeps its current client
// certificate in; it is a link to the newest one.
func (p Paths) kubeletClient() string {
	return filepath.Join(p.KubeletPKI, "kubelet-client-current.pem")
}

// ErrNoShare means the node has no Kubernetes share yet.
var ErrNoShare = errors.New("the node has no Kubernetes share; deliver one with chalkctl install or chalkctl apply-identity --kubernetes-share")

// ReadShare reads the node's share from STATE.
func ReadShare(p Paths) (kpki.Share, error) {
	data, err := os.ReadFile(p.Share())
	if errors.Is(err, fs.ErrNotExist) {
		return kpki.Share{}, ErrNoShare
	}
	if err != nil {
		return kpki.Share{}, err
	}
	return kpki.ParseShare(data)
}

// ErrEtcdDataMissing means etcd's data directory is empty on a node that initialised etcd, as
// after VAR was reset: etcd would start a new, empty cluster.
var ErrEtcdDataMissing = errors.New("etcd data is missing on a node whose cluster was initialised; restore etcd or reinstall the node")

// Bootstrapped reports whether this control-plane node was told to initialise etcd.
func Bootstrapped(p Paths) (bool, error) {
	return marked(p.Bootstrapped())
}

// EtcdInitialised reports whether etcd on this node answered ready after the bootstrap.
func EtcdInitialised(p Paths) (bool, error) {
	return marked(p.EtcdInitialised())
}

// Joining reports whether the node started joining the cluster and has not finished.
func Joining(p Paths) (bool, error) {
	return marked(p.Joining())
}

// MarkJoining records that the node starts joining the cluster at the endpoint.
func MarkJoining(p Paths, endpoint string) error {
	return install.WriteFile(p.Joining(), []byte(endpoint+"\n"), 0o644)
}

// MarkJoined records that the node joined the cluster: from then on it is a bootstrapped control
// plane that renders its static pods at boot. The bootstrapped marker comes first, so the node is
// marked as one or the other at any time.
func MarkJoined(p Paths, now time.Time) error {
	if err := MarkBootstrapped(p, now); err != nil {
		return fmt.Errorf("record the join: %w", err)
	}
	return removeIfExists(p.Joining())
}

// MarkBootstrapped records that the node is a bootstrapped control plane, which renders its
// static pods at boot.
func MarkBootstrapped(p Paths, now time.Time) error {
	return install.WriteFile(p.Bootstrapped(), []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// ClearMembership removes what makes the node an etcd member at boot: the bootstrapped,
// etcd-initialised and joining markers, the initial cluster and, last, the pin. etcd's data stays.
// The caller records first that the node left etcd: a node interrupted here must not look like one
// that has yet to join.
func ClearMembership(p Paths) error {
	for _, f := range []string{p.Bootstrapped(), p.EtcdInitialised(), p.EtcdInitialCluster(), p.Joining(), p.Pin()} {
		if err := removeIfExists(f); err != nil {
			return err
		}
	}
	return nil
}

// Left reports whether the node left etcd.
func Left(p Paths) (bool, error) {
	return marked(p.Left())
}

// MarkLeft records that the node left etcd. It comes before the node deletes its etcd data and
// ClearMembership, so a node interrupted there never looks like one that has yet to join.
func MarkLeft(p Paths, now time.Time) error {
	return install.WriteFile(p.Left(), []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// RemoveEtcdData deletes etcd's data and keeps its directory.
func RemoveEtcdData(p Paths) error {
	entries, err := os.ReadDir(p.EtcdData)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(p.EtcdData, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Prepared reports whether Prepare finished and succeeded since it last started.
func Prepared(p Paths) (bool, error) {
	return marked(p.Prepared())
}

// MarkEtcdInitialised records that etcd answered ready; it keeps an existing marker.
func MarkEtcdInitialised(p Paths, now time.Time) error {
	initialised, err := EtcdInitialised(p)
	if err != nil || initialised {
		return err
	}
	return install.WriteFile(p.EtcdInitialised(), []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// CheckEtcdData refuses an empty etcd data directory once etcd was initialised. Without the
// marker an empty directory is a bootstrap that was interrupted before etcd became ready, which
// may be retried.
func CheckEtcdData(p Paths) error {
	initialised, err := EtcdInitialised(p)
	if err != nil || !initialised {
		return err
	}
	hasData, err := EtcdHasData(p)
	if err != nil {
		return err
	}
	if !hasData {
		return ErrEtcdDataMissing
	}
	return nil
}

// marked reports whether a marker file exists. Only a marker that does not exist is unset.
func marked(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

// Load reads the node's share, its image's cluster file and its identity, and checks they
// belong together.
func Load(p Paths) (kpki.Share, kubernetes.Cluster, kubernetes.Node, error) {
	share, err := ReadShare(p)
	if err != nil {
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, err
	}
	c, err := kubernetes.ReadCluster(p.Cluster)
	if err != nil {
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, err
	}
	n, err := kubernetes.ReadNode(p.NodeFile)
	if err != nil {
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, err
	}
	if share.Kind != c.Kind {
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, fmt.Errorf("the node's share is for a %s node, but its image is for %s nodes", share.Kind, c.Kind)
	}
	if share.Kind == kubernetes.KindWorker && share.Node() != n.Name {
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, fmt.Errorf("the node's kubelet certificate is for node %s, not %s", share.Node(), n.Name)
	}
	return share, c, n, nil
}

// AddressesTarget is the systemd target that says the node's addresses have settled.
const AddressesTarget = "chalkos-node-addresses.target"

// Resolver waits up to timeout for the addresses the selector picks.
type Resolver func(sel nodeip.Selector, timeout time.Duration) ([]nodeip.Address, error)

// WaitForAddresses waits for the addresses the selector picks among the node's own.
func WaitForAddresses(sel nodeip.Selector, timeout time.Duration) ([]nodeip.Address, error) {
	log.Printf("picking the node's addresses, %s, waiting up to %v", sel, timeout)
	picked, err := nodeip.NewWaiter().Wait(sel, timeout)
	if err != nil {
		return nil, err
	}
	log.Printf("the node's addresses are %v", picked)
	return picked, nil
}

// checkFlannelLinks refuses node addresses on interfaces flannel's VXLAN cannot use as they
// are. flannel takes its MTU from the interface holding the node's address unless one is set,
// which on a loopback interface is far beyond any network's. It gives its VXLAN devices that MTU
// minus 50, and the kernel limits them to the interface's MTU minus the encapsulation, 50 for
// IPv4 and 70 for IPv6, without telling flannel, so a dummy interface needs room for the MTU set.
func checkFlannelLinks(c kubernetes.Cluster, picked []nodeip.Address) error {
	if c.Flannel == nil {
		return nil
	}
	for _, a := range picked {
		switch {
		case a.Link.Loopback && c.Flannel.MTU == 0:
			return fmt.Errorf("the node's address %s is on the loopback interface %s, whose MTU flannel would take; set chalkos.cni.flannel.mtu", a.IP, a.Interface)
		case a.Link.Kind == "dummy" && c.Flannel.MTU > 0:
			need := c.Flannel.MTU
			if a.IP.Is6() {
				need += 20
			}
			if a.Link.MTU < need {
				return fmt.Errorf("the node's address %s is on the dummy interface %s with MTU %d; flannel's %s VXLAN with chalkos.cni.flannel.mtu %d needs an MTU of at least %d there", a.IP, a.Interface, a.Link.MTU, nodeip.FamilyOf(a.IP), c.Flannel.MTU, need)
			}
		}
	}
	return nil
}

// ReadNodeIPs reads the addresses Prepare picked, the primary family's first.
func ReadNodeIPs(p Paths) ([]net.IP, error) {
	data, err := os.ReadFile(p.NodeIP())
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, line := range strings.Fields(string(data)) {
		ip := net.ParseIP(line)
		if ip == nil {
			return nil, fmt.Errorf("%s holds %q, which is not an address", p.NodeIP(), line)
		}
		ips = append(ips, ip)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s holds no address", p.NodeIP())
	}
	return ips, nil
}

// WritePin pins the node to the addresses, the ones the preparation picked and etcd advertises.
func WritePin(p Paths, ips []net.IP) error {
	if len(ips) == 0 {
		return errors.New("pin the node's addresses: there are none")
	}
	return install.WriteFile(p.Pin(), nodeIPFile(ips), 0o644)
}

// ReadPin returns the addresses the node is pinned to, nil when it is not pinned.
func ReadPin(p Paths) ([]netip.Addr, error) {
	data, err := os.ReadFile(p.Pin())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pin []netip.Addr
	for _, line := range strings.Fields(string(data)) {
		ip, err := netip.ParseAddr(line)
		if err != nil {
			return nil, fmt.Errorf("%s holds %q, which is not an address", p.Pin(), line)
		}
		pin = append(pin, ip)
	}
	if len(pin) == 0 {
		return nil, fmt.Errorf("%s holds no address", p.Pin())
	}
	return pin, nil
}

// nodeSelector is how the node picks its addresses: a control-plane node that is pinned waits
// for exactly its pinned addresses, any other node selects them as its cluster and identity say.
func nodeSelector(p Paths, c kubernetes.Cluster, n kubernetes.Node) (nodeip.Selector, error) {
	if c.Kind == kubernetes.KindControlPlane {
		pin, err := ReadPin(p)
		if err != nil {
			return nodeip.Selector{}, err
		}
		if pin != nil {
			sel := nodeip.Selector{Fixed: pin, Pinned: true, SameInterface: c.Flannel != nil && len(pin) > 1}
			for _, ip := range pin {
				sel.Families = append(sel.Families, nodeip.FamilyOf(ip))
			}
			// The pin holds an address of each family the cluster was created with, the primary
			// one first. Other families, or another order, would change the cluster's addressing
			// and the addresses etcd's peers know the node by.
			families, err := c.Families()
			if err != nil {
				return nodeip.Selector{}, err
			}
			if !slices.Equal(sel.Families, families) {
				return nodeip.Selector{}, fmt.Errorf("the cluster was created with families %v; ipFamilies is %v; changing the families of a running cluster is not supported",
					sel.Families, families)
			}
			return sel, nil
		}
	}
	return c.NodeIPSelector(n)
}

// vxlanFile is the content of p.VXLAN() for the node's addresses. An address on a network
// interface takes VXLAN on that interface alone. One on a loopback or dummy interface is an
// address routers reach the node at through its other interfaces, as with a routing daemon that
// announces it, so VXLAN to it arrives on any interface.
func vxlanFile(picked []nodeip.Address, sources []netip.Prefix) []byte {
	var b strings.Builder
	for _, a := range picked {
		mode := "interface"
		if a.Link.Loopback || a.Link.Kind == "dummy" {
			mode = "any"
		}
		fmt.Fprintf(&b, "destination %s %s\n", a.IP, mode)
	}
	for _, s := range sources {
		fmt.Fprintf(&b, "source %s\n", s)
	}
	return []byte(b.String())
}

// checkVXLANSources refuses node addresses outside the ranges the other nodes take VXLAN from:
// their pods could not reach this node's.
func checkVXLANSources(picked []nodeip.Address, sources []netip.Prefix) error {
	if len(sources) == 0 {
		return nil
	}
	for _, a := range picked {
		if !slices.ContainsFunc(sources, func(p netip.Prefix) bool { return p.Contains(a.IP) }) {
			return fmt.Errorf("the node's address %s is outside vxlanSourceSubnets %s, from which alone the other nodes take VXLAN", a.IP, joinPrefixes(sources))
		}
	}
	return nil
}

// joinPrefixes lists the ranges, separated by commas.
func joinPrefixes(prefixes []netip.Prefix) string {
	list := make([]string, len(prefixes))
	for i, p := range prefixes {
		list[i] = p.String()
	}
	return strings.Join(list, ", ")
}

// nodeIPFile is the content of the file holding the addresses: one per line.
func nodeIPFile(ips []net.IP) []byte {
	var b strings.Builder
	for _, ip := range ips {
		b.WriteString(ip.String() + "\n")
	}
	return []byte(b.String())
}

// PreparationError reads why the last preparation failed; "" when none failed since the last one
// started.
func PreparationError(p Paths) (string, error) {
	data, err := os.ReadFile(p.PrepareError())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Firewall accepts VXLAN as p.VXLAN() says, or none when the file does not exist.
type Firewall func(p Paths) error

// VXLANRule is the Firewall that runs script with p.VXLAN(). The script prints the rules to the
// journal, which the error points to.
func VXLANRule(script string) Firewall {
	return func(p Paths) error {
		cmd := exec.Command(script, p.VXLAN())
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("accept VXLAN to the node's address: %w; see chalkctl logs <node> --unit chalkos-kubernetes", err)
		}
		return nil
	}
}

// Prepare picks the node's address and writes the kubelet's files and, on a control-plane node,
// the control plane's certificates, and its static pods once the node is bootstrapped; then it
// lets the firewall, if any, accept VXLAN to the address. A node without a share or an address,
// and one whose preparation fails, keeps none of them, so its kubelet does not start, and
// accepts no VXLAN. The node is marked prepared only once all of this is done; otherwise
// PrepareError says why.
func Prepare(p Paths, now time.Time, resolve Resolver, firewall Firewall) error {
	// chalkd renews the control plane's certificates only between preparations.
	unlock, err := lockPKI(p, true)
	if err != nil {
		return err
	}
	defer unlock()
	for _, f := range []string{p.Prepared(), p.PrepareError()} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	err = prepare(p, now, resolve)
	if err == nil && firewall != nil {
		err = firewall(p)
	}
	if err == nil {
		if err = install.WriteFile(p.Prepared(), []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o644); err == nil {
			return nil
		}
	}
	// The kubelet must not start with what an earlier attempt wrote, nor the control plane with
	// certificates naming the address picked then, nor VXLAN reach that address.
	for _, path := range []string{p.KubeletDir(), p.Manifests(), p.PKI, p.OldPKI(), p.NodeIP(), p.VXLAN()} {
		if rerr := os.RemoveAll(path); rerr != nil {
			log.Print(rerr)
		}
	}
	if firewall != nil {
		if ferr := firewall(p); ferr != nil {
			log.Print(ferr)
		}
	}
	// chalkd reports the reason in the node's status. Errors never hold key material.
	if werr := install.WriteFile(p.PrepareError(), []byte(err.Error()+"\n"), 0o644); werr != nil {
		log.Print(werr)
	}
	return err
}

func prepare(p Paths, now time.Time, resolve Resolver) error {
	// An address picked before must not outlive an attempt that fails.
	for _, f := range []string{p.NodeIP(), p.VXLAN()} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	share, c, n, err := Load(p)
	if errors.Is(err, ErrNoShare) {
		log.Print(err)
		return os.RemoveAll(p.KubeletDir())
	}
	if err != nil {
		return err
	}
	sel, err := nodeSelector(p, c, n)
	if err != nil {
		return err
	}
	timeout := time.Duration(c.NodeIP.Timeout) * time.Second
	picked, err := resolve(sel, timeout)
	if err != nil {
		// Units that add the node's addresses, such as a routing daemon's, order themselves before
		// the target, which the preparation runs after. The resolver returns an error only once
		// its deadline passed (nodeip.Waiter.Wait), so the whole timeout went by.
		err = fmt.Errorf("%w, %v after %s", err, timeout, AddressesTarget)
	}
	if pinned := (*nodeip.PinnedError)(nil); errors.As(err, &pinned) {
		return fmt.Errorf("%w; restore it, or remove the node's etcd member with chalkctl etcd remove-member %s and reinstall the node", err, n.Name)
	}
	if err != nil {
		return err
	}
	if err := checkFlannelLinks(c, picked); err != nil {
		return err
	}
	sources, err := c.VXLANSources()
	if err != nil {
		return err
	}
	if c.Flannel != nil {
		if err := checkVXLANSources(picked, sources); err != nil {
			return err
		}
	}
	for _, a := range picked {
		n.IPs = append(n.IPs, net.IP(a.IP.AsSlice()))
	}
	kubeletCert := share.Kubelet
	if c.Kind == kubernetes.KindControlPlane {
		files, err := kpki.ControlPlane(share, c, n, now)
		if err != nil {
			return err
		}
		if err := replaceDir(p.PKI, files); err != nil {
			return err
		}
		issued, err := kpki.IssueKubeletClient(share.CA, n.Name, now)
		if err != nil {
			return err
		}
		kubeletCert = &issued
	}
	if err := installKubeletClient(p, share.CA, *kubeletCert, n.Name, now); err != nil {
		return err
	}
	kubeconfig, err := kpki.Kubeconfig{
		Name:       "chalkos",
		Server:     c.Endpoint,
		CAFile:     filepath.Join(p.KubeletDir(), "ca.crt"),
		ClientFile: p.kubeletClient(),
	}.Encode()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.KubeletDir(), 0o755); err != nil {
		return err
	}
	for name, data := range map[string][]byte{
		"ca.crt": []byte(share.CA.Certificate),
		"flags":  []byte("KUBELET_ARGS=" + strings.Join(KubeletFlags(c, n), " ") + "\n"),
	} {
		if err := install.WriteFile(filepath.Join(p.KubeletDir(), name), data, 0o644); err != nil {
			return err
		}
	}
	// Written once the certificates naming it are, which Bootstrap relies on.
	if err := install.WriteFile(p.NodeIP(), nodeIPFile(n.IPs), 0o644); err != nil {
		return err
	}
	if err := install.WriteFile(p.VXLAN(), vxlanFile(picked, sources), 0o644); err != nil {
		return err
	}
	// The kubelet starts once its kubeconfig exists, so it is written last.
	if err := install.WriteFile(p.Kubeconfig(), kubeconfig, 0o644); err != nil {
		return err
	}
	if c.Kind != kubernetes.KindControlPlane {
		return nil
	}
	if err := os.MkdirAll(p.Manifests(), 0o755); err != nil {
		return err
	}
	bootstrapped, err := Bootstrapped(p)
	if err != nil || !bootstrapped {
		return err
	}
	// A leave that was interrupted keeps the marker, but the node's member is gone.
	if left, err := Left(p); err != nil || left {
		return err
	}
	return RenderStaticPods(p)
}

// KubeletFlags are the kubelet's node-specific flags: its name, addresses, labels and taints.
// Control-plane nodes are tainted unless the cluster allows workloads on them.
func KubeletFlags(c kubernetes.Cluster, n kubernetes.Node) []string {
	flags := []string{"--hostname-override=" + n.Name}
	if len(n.IPs) > 0 {
		ips := make([]string, len(n.IPs))
		for i, ip := range n.IPs {
			ips[i] = ip.String()
		}
		flags = append(flags, "--node-ip="+strings.Join(ips, ","))
	}
	var labels []string
	for k, v := range n.Labels {
		labels = append(labels, k+"="+v)
	}
	sort.Strings(labels)
	if len(labels) > 0 {
		flags = append(flags, "--node-labels="+strings.Join(labels, ","))
	}
	var taints []string
	if c.Kind == kubernetes.KindControlPlane && !c.AllowSchedulingOnControlPlanes {
		taints = append(taints, "node-role.kubernetes.io/control-plane:NoSchedule")
	}
	for _, t := range n.Taints {
		taint := t.Key
		if t.Value != "" {
			taint += "=" + t.Value
		}
		taints = append(taints, taint+":"+t.Effect)
	}
	if len(taints) > 0 {
		flags = append(flags, "--register-with-taints="+strings.Join(taints, ","))
	}
	return flags
}

// installKubeletClient puts the certificate into the kubelet's certificate store unless the
// store holds one the kubelet renewed itself that is trusted and expires later.
func installKubeletClient(p Paths, ca pki.CertKey, ck pki.CertKey, node string, now time.Time) error {
	cert, _, err := ck.Parse()
	if err != nil {
		return fmt.Errorf("kubelet client certificate: %w", err)
	}
	switch err := checkRenewedClient(p.kubeletClient(), ca, cert, node, now); {
	case err == nil:
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		log.Printf("kubelet: installing the share's client certificate instead of %s: %v", p.kubeletClient(), err)
	}
	if err := os.MkdirAll(p.KubeletPKI, 0o700); err != nil {
		return err
	}
	// One file, replaced atomically; the kubelet writes renewed certificates next to it.
	name := "kubelet-client-chalkos.pem"
	if err := install.WriteFile(filepath.Join(p.KubeletPKI, name), []byte(ck.Certificate+ck.Key), 0o600); err != nil {
		return err
	}
	// The kubelet's store follows the link and refuses to replace a file that is not one.
	tmp := p.kubeletClient() + ".chalkos"
	os.Remove(tmp)
	if err := os.Symlink(name, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, p.kubeletClient())
}

// checkRenewedClient returns nil if the kubelet's current client certificate in path should be
// kept: it was issued by ca for node with its own key, is valid now and expires after want.
// Errors never include the file's contents.
func checkRenewedClient(path string, ca pki.CertKey, want *x509.Certificate, node string, now time.Time) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// The kubelet stores the certificate and its key in one file; X509KeyPair also checks that
	// the key belongs to the certificate, whatever encoding the key has.
	pair, err := tls.X509KeyPair(data, data)
	if err != nil {
		return fmt.Errorf("not a certificate with its key: %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	caCert, err := pki.ParseCertificate([]byte(ca.Certificate))
	if err != nil {
		return fmt.Errorf("the cluster CA: %w", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: now}); err != nil {
		return err
	}
	if cert.Subject.CommonName != kpki.NodeUserPrefix+node || !slices.Equal(cert.Subject.Organization, []string{kpki.NodesGroup}) {
		return fmt.Errorf("the certificate is for %q in groups %q, want node %s in %q only", cert.Subject.CommonName, cert.Subject.Organization, node, kpki.NodesGroup)
	}
	if !cert.NotAfter.After(want.NotAfter) {
		return errors.New("it expires no later than the share's certificate")
	}
	return nil
}

// WriteInitialCluster records the initial cluster the node's etcd joins with.
func WriteInitialCluster(p Paths, initialCluster string) error {
	return install.WriteFile(p.EtcdInitialCluster(), []byte(initialCluster+"\n"), 0o644)
}

// HasInitialCluster reports whether the node recorded the initial cluster its etcd joins with.
func HasInitialCluster(p Paths) (bool, error) {
	return marked(p.EtcdInitialCluster())
}

// readInitialCluster returns the initial cluster the node joined with, "" when it started its own.
func readInitialCluster(p Paths) (string, error) {
	data, err := os.ReadFile(p.EtcdInitialCluster())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	initial := strings.TrimSpace(string(data))
	if initial == "" {
		return "", fmt.Errorf("%s is empty", p.EtcdInitialCluster())
	}
	return initial, nil
}

// RenderStaticPods writes the static pods from the certificates Prepare wrote, unless etcd's
// data is missing.
func RenderStaticPods(p Paths) error {
	return render(p, "")
}

// RenderEtcd writes etcd's static pod alone, for a node whose etcd joins as a learner: its API
// server would not start before etcd is a voter.
func RenderEtcd(p Paths) error {
	return render(p, "etcd.json")
}

// render writes the static pods, or only the one in the file only names.
func render(p Paths, only string) error {
	if err := CheckEtcdData(p); err != nil {
		return err
	}
	c, err := kubernetes.ReadCluster(p.Cluster)
	if err != nil {
		return err
	}
	n, err := kubernetes.ReadNode(p.NodeFile)
	if err != nil {
		return err
	}
	if n.IPs, err = ReadNodeIPs(p); err != nil {
		return fmt.Errorf("the node's addresses: %w", err)
	}
	files, err := readDir(p.PKI)
	if err != nil {
		return fmt.Errorf("read the control plane's certificates: %w", err)
	}
	initialCluster, err := readInitialCluster(p)
	if err != nil {
		return err
	}
	pods, err := manifests.StaticPods(c, n, files, initialCluster)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Manifests(), 0o755); err != nil {
		return err
	}
	for name, data := range pods {
		if only != "" && name != only {
			continue
		}
		if err := install.WriteFile(filepath.Join(p.Manifests(), name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// EtcdHasData reports whether etcd's data directory holds anything.
func EtcdHasData(p Paths) (bool, error) {
	entries, err := os.ReadDir(p.EtcdData)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

func readDir(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[rel] = data
		return nil
	})
	return files, err
}
