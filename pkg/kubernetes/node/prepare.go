// Package node prepares a node's Kubernetes files at boot: the node's address, the kubelet's
// credentials, kubeconfig and flags from the node's share and identity, and on control-plane
// nodes the control plane's certificates and, once bootstrapped, its static pods.
package node

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// load reads the node's share, its cluster file and its identity, and checks they belong
// together.
func load(p Paths) (kpki.Share, kubernetes.Cluster, kubernetes.Node, error) {
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
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, fmt.Errorf("the node's share is for a %s node, but its config gives it the type %s", share.Kind, c.Kind)
	}
	if share.Kind == kubernetes.KindWorker && share.Node() != n.Name {
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, fmt.Errorf("the node's kubelet certificate is for node %s, not %s", share.Node(), n.Name)
	}
	return share, c, n, nil
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
		if err := removeIfExists(f); err != nil {
			return err
		}
	}
	err = prepare(p, now, resolve)
	if err == nil && firewall != nil {
		err = firewall(p)
	}
	if err == nil {
		if err = markAt(p.Prepared(), now); err == nil {
			return nil
		}
	}
	// The kubelet must not start with what an earlier attempt wrote, nor the control plane with
	// certificates naming the address picked then, nor VXLAN reach that address.
	for _, path := range []string{p.KubeletDir(), p.Manifests(), p.PKI, p.OldPKI(), p.PKI + ".new", p.NodeIP(), p.VXLAN()} {
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
	if werr := atomicfile.WriteFile(p.PrepareError(), []byte(err.Error()+"\n"), 0o644); werr != nil {
		log.Print(werr)
	}
	return err
}

func prepare(p Paths, now time.Time, resolve Resolver) error {
	// An address picked before must not outlive an attempt that fails.
	for _, f := range []string{p.NodeIP(), p.VXLAN()} {
		if err := removeIfExists(f); err != nil {
			return err
		}
	}
	share, c, n, err := load(p)
	if errors.Is(err, ErrNoShare) {
		log.Print(err)
		return os.RemoveAll(p.KubeletDir())
	}
	if err != nil {
		return err
	}
	picked, err := pickAddresses(p, c, n, resolve)
	if err != nil {
		return err
	}
	for _, a := range picked {
		n.IPs = append(n.IPs, a.IP)
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
	if err := writeKubeletFiles(p, c, n, share, *kubeletCert, now); err != nil {
		return err
	}
	// Written once the certificates naming it are, which Bootstrap relies on.
	if err := atomicfile.WriteFile(p.NodeIP(), nodeIPFile(n.IPs), 0o644); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(p.VXLAN(), vxlanFile(picked, c.VXLANSources()), 0o644); err != nil {
		return err
	}
	// The kubelet starts once its kubeconfig exists, so it is written last.
	kubeconfig, err := kpki.Kubeconfig{
		Name:       "chalkos",
		Server:     c.Endpoint,
		CAFile:     filepath.Join(p.KubeletDir(), "ca.crt"),
		ClientFile: p.KubeletClient(),
	}.Encode()
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(p.Kubeconfig(), kubeconfig, 0o644); err != nil {
		return err
	}
	if c.Kind != kubernetes.KindControlPlane {
		return nil
	}
	if err := os.MkdirAll(p.Manifests(), 0o755); err != nil {
		return err
	}
	// A leave that was interrupted keeps the bootstrapped marker, but the node's member is gone.
	if renders, err := rendersStaticPods(p); err != nil || !renders {
		return err
	}
	return RenderStaticPods(p)
}
