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

// inputs are what the node's Kubernetes files are made from.
type inputs struct {
	share   kpki.Share
	cluster kubernetes.Cluster
	// node is the node's identity, with the addresses the preparation picked once they are known.
	node kubernetes.Node
}

// load reads the node's share, its cluster file and its identity, and checks they belong
// together.
func (s State) load() (inputs, error) {
	share, err := s.ReadShare()
	if err != nil {
		return inputs{}, err
	}
	in, err := s.readClusterNode()
	if err != nil {
		return inputs{}, err
	}
	in.share = share
	if err := in.check(); err != nil {
		return inputs{}, err
	}
	return in, nil
}

// readClusterNode reads the node's cluster file and its identity, without the share.
func (s State) readClusterNode() (inputs, error) {
	c, err := kubernetes.ReadCluster(s.Cluster)
	if err != nil {
		return inputs{}, err
	}
	n, err := kubernetes.ReadNode(s.NodeFile)
	if err != nil {
		return inputs{}, err
	}
	return inputs{cluster: c, node: n}, nil
}

// check checks that the share belongs to the cluster file and the node's identity.
func (in inputs) check() error {
	if in.share.Kind != in.cluster.Kind {
		return fmt.Errorf("the node's share is for a %s node, but its config gives it the type %s", in.share.Kind, in.cluster.Kind)
	}
	if in.share.Kind == kubernetes.KindWorker && in.share.Node() != in.node.Name {
		return fmt.Errorf("the node's kubelet certificate is for node %s, not %s", in.share.Node(), in.node.Name)
	}
	return nil
}

// Prepare picks the node's address and writes the kubelet's files and, on a control-plane node,
// the control plane's certificates, and its static pods once the node is bootstrapped; then it
// lets the firewall, if any, accept VXLAN to the address. A node without a share or an address,
// and one whose preparation fails, keeps none of them, so its kubelet does not start, and
// accepts no VXLAN. The node is marked prepared only once all of this is done; otherwise
// PrepareError says why.
func (s State) Prepare(now time.Time, resolve Resolver, firewall Firewall) error {
	// chalkd renews the control plane's certificates only between preparations.
	unlock, err := s.lockPKI(true)
	if err != nil {
		return err
	}
	defer unlock()
	for _, f := range []string{s.PreparedMarker(), s.PrepareError()} {
		if err := removeIfExists(f); err != nil {
			return err
		}
	}
	err = s.prepare(now, resolve)
	if err == nil && firewall != nil {
		err = firewall(s.Paths)
	}
	if err == nil {
		if err = markAt(s.PreparedMarker(), now); err == nil {
			return nil
		}
	}
	// The kubelet must not start with what an earlier attempt wrote, nor the control plane with
	// certificates naming the address picked then, nor VXLAN reach that address.
	for _, path := range []string{s.KubeletDir(), s.Manifests(), s.PKI, s.OldPKI(), s.PKI + ".new", s.NodeIP(), s.VXLAN()} {
		if rerr := os.RemoveAll(path); rerr != nil {
			log.Print(rerr)
		}
	}
	if firewall != nil {
		if ferr := firewall(s.Paths); ferr != nil {
			log.Print(ferr)
		}
	}
	// chalkd reports the reason in the node's status. Errors never hold key material.
	if werr := atomicfile.WriteFile(s.PrepareError(), []byte(err.Error()+"\n"), 0o644); werr != nil {
		log.Print(werr)
	}
	return err
}

func (s State) prepare(now time.Time, resolve Resolver) error {
	// An address picked before must not outlive an attempt that fails.
	for _, f := range []string{s.NodeIP(), s.VXLAN()} {
		if err := removeIfExists(f); err != nil {
			return err
		}
	}
	in, err := s.load()
	if errors.Is(err, ErrNoShare) {
		log.Print(err)
		return os.RemoveAll(s.KubeletDir())
	}
	if err != nil {
		return err
	}
	picked, err := s.pickAddresses(in.cluster, in.node, resolve)
	if err != nil {
		return err
	}
	for _, a := range picked {
		in.node.IPs = append(in.node.IPs, a.IP)
	}
	kubeletCert := in.share.Kubelet
	if in.cluster.Kind == kubernetes.KindControlPlane {
		files, err := kpki.ControlPlane(in.share, in.cluster, in.node, now)
		if err != nil {
			return err
		}
		if err := replaceDir(s.PKI, files); err != nil {
			return err
		}
		issued, err := kpki.IssueKubeletClient(in.share.CA, in.node.Name, now)
		if err != nil {
			return err
		}
		kubeletCert = &issued
	}
	if err := s.writeKubeletFiles(in, *kubeletCert, now); err != nil {
		return err
	}
	// Written once the certificates naming it are, which Bootstrap relies on.
	if err := atomicfile.WriteFile(s.NodeIP(), nodeIPFile(in.node.IPs), 0o644); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(s.VXLAN(), vxlanAccept(picked, in.cluster.VXLANSources()).Marshal(), 0o644); err != nil {
		return err
	}
	// The kubelet starts once its kubeconfig exists, so it is written last.
	kubeconfig, err := kpki.Kubeconfig{
		Name:       "chalkos",
		Server:     in.cluster.Endpoint,
		CAFile:     filepath.Join(s.KubeletDir(), "ca.crt"),
		ClientFile: s.KubeletClient(),
	}.Encode()
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(s.Kubeconfig(), kubeconfig, 0o644); err != nil {
		return err
	}
	if in.cluster.Kind != kubernetes.KindControlPlane {
		return nil
	}
	if err := os.MkdirAll(s.Manifests(), 0o755); err != nil {
		return err
	}
	// A leave that was interrupted keeps the bootstrapped marker, but the node's member is gone.
	if renders, err := s.rendersStaticPods(); err != nil || !renders {
		return err
	}
	if err := s.CheckEtcdData(); err != nil {
		return err
	}
	// The addresses and certificates are read back as the static pods see them.
	if in.node.IPs, err = s.ReadNodeIPs(); err != nil {
		return fmt.Errorf("the node's addresses: %w", err)
	}
	return s.renderFrom(in, "")
}
