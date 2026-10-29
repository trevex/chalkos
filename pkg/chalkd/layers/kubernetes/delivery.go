package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/pki"
)

var (
	_ layer.Delivery   = (*Plugin)(nil)
	_ nodeconfig.Layer = (*Plugin)(nil)
)

// Accepts takes the node's share.
func (p *Plugin) Accepts(payload *anypb.Any) bool { return kpki.IsPayload(payload) }

// CheckIdentity refuses an identity whose Kubernetes part a node could not pick its addresses
// by: it would run no kubelet.
func (p *Plugin) CheckIdentity(data []byte) error {
	var id identity.Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return failed(connect.CodeInvalidArgument, "parse the identity: %v", err)
	}
	if k := id.Kubernetes; k != nil {
		if err := k.CheckNodeIPs(); err != nil {
			return failed(connect.CodeInvalidArgument, "the identity's %v", err)
		}
		if _, err := nodeip.ParseFilter(k.ValidSubnets); err != nil {
			return failed(connect.CodeInvalidArgument, "%v", err)
		}
	}
	return nil
}

// Plan checks the cluster the config gives on this image and a delivered share, and, when the
// node trusts new OS CAs and keeps its share, the node CA the share holds. An install checks the
// share alone.
func (p *Plugin) Plan(_ context.Context, d layer.Planned) (layer.Commit, error) {
	var c layer.Commit
	var cluster *k8s.Cluster
	if d.Documents != nil && !d.Install {
		var err error
		if cluster, err = p.cluster(d.Documents); err != nil {
			return c, failed(connect.CodeInvalidArgument, "the config: %v", err)
		}
		// An image without image.json gives no cluster, and the delivery writes none.
		if cluster != nil {
			c.Config = func() error {
				if err := p.writeCluster(cluster); err != nil {
					return fmt.Errorf("write the Kubernetes cluster: %w", err)
				}
				return nil
			}
		}
	}
	share, err := p.share(d, cluster)
	if err != nil {
		return c, err
	}
	switch {
	case share != nil:
		c.Payloads = func(stateDir string) error {
			if err := writeShare(stateDir, share); err != nil {
				return fmt.Errorf("record the Kubernetes share: %w", err)
			}
			return nil
		}
	case d.Trust:
		// A control plane keeps renewing node certificates with the node CA it holds.
		if err := p.k.checkKeptNodeCA(d.OSCA); err != nil {
			return c, err
		}
	}
	if d.ForceRenewals && d.Documents != nil {
		c.Applied = p.k.forceLeaves
	}
	return c, nil
}

// WriteConfig writes the cluster the documents give on this image as the node's cluster file,
// or removes it without documents.
func (p *Plugin) WriteConfig(docs config.Documents) error {
	var c *k8s.Cluster
	if docs != nil {
		var err error
		if c, err = p.cluster(docs); err != nil {
			// No Kubernetes side runs on settings the config does not give.
			if rerr := p.writeCluster(nil); rerr != nil {
				return errors.Join(err, rerr)
			}
			return fmt.Errorf("the node runs no Kubernetes: its stored config gives no Kubernetes cluster for this image: %w; deliver config that does with chalkctl apply-config", err)
		}
	}
	return p.writeCluster(c)
}

// cluster returns the Kubernetes cluster docs give on this image, from the image's facts and the
// documents' settings; nil on an image without its image.json.
func (p *Plugin) cluster(docs config.Documents) (*k8s.Cluster, error) {
	img, err := k8s.ReadImage(p.k.imageFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := docs.Cluster(img)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// writeCluster writes c as the node's cluster file, or removes the file for nil. A file that
// holds c already is left as it is.
func (p *Plugin) writeCluster(c *k8s.Cluster) error {
	file := p.k.clusterFile
	if c == nil {
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	old, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if bytes.Equal(old, data) {
		return nil
	}
	return atomicfile.WriteFile(file, data, 0o644)
}

// share validates a delivered share for the node the identity names, a node CA in it against the
// OS CAs the node trusts once the delivery is applied, and returns it as STATE keeps it, in its
// canonical encoding; nil when none was delivered. A second share is refused. Outside an install,
// the share's kind must be the cluster's: cluster is the one the config of the same request gives,
// nil without config, when the node's cluster file is read instead.
func (p *Plugin) share(d layer.Planned, cluster *k8s.Cluster) ([]byte, error) {
	var data []byte
	for i, payload := range d.Payloads {
		if i > 0 {
			return nil, failed(connect.CodeInvalidArgument, "the request carries two shares; deliver one")
		}
		var err error
		if data, err = kpki.SharePayload(payload); err != nil {
			return nil, failed(connect.CodeInvalidArgument, "%v", err)
		}
	}
	if len(data) == 0 {
		return nil, nil
	}
	share, err := kpki.ParseShare(data)
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "%v", err)
	}
	if !d.Install {
		if cluster == nil {
			c, err := k8s.ReadCluster(p.k.paths.Cluster)
			if err != nil {
				return nil, failed(connect.CodeInvalidArgument, "the node has no Kubernetes cluster: %v", err)
			}
			cluster = &c
		}
		if share.Kind != cluster.Kind {
			return nil, failed(connect.CodeInvalidArgument, "the share is for a %s node, but the node's Node document gives it the type %s", share.Kind, cluster.Kind)
		}
	}
	var id identity.Identity
	if err := json.Unmarshal(d.Identity, &id); err != nil {
		return nil, failed(connect.CodeInvalidArgument, "parse the identity: %v", err)
	}
	var nodeName string
	if id.Kubernetes != nil {
		nodeName = id.Kubernetes.NodeName
	}
	// A node must never run its kubelet with a certificate issued for another node.
	if err := share.ValidateFor(nodeName); err != nil {
		return nil, failed(connect.CodeInvalidArgument, "%v", err)
	}
	// Certificates of another node CA would never verify against the node's OS CA.
	if share.NodeCA != nil {
		if err := pki.ValidateNodeCA(*share.NodeCA, d.OSCA); err != nil {
			return nil, failed(connect.CodeInvalidArgument, "the share's node CA: %v", err)
		}
	}
	canonical, err := share.Encode()
	if err != nil {
		return nil, failed(connect.CodeInternal, "encode the Kubernetes share: %v", err)
	}
	return canonical, nil
}

// writeShare records the node's Kubernetes share on STATE, in a directory only root can enter.
func writeShare(stateDir string, share []byte) error {
	dir := filepath.Join(stateDir, "kubernetes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "share.json"), share, 0o600)
}

// checkKeptNodeCA refuses OS CAs that the node CA a control plane holds does not chain to.
func (k *side) checkKeptNodeCA(osCA string) error {
	share, err := knode.ReadShare(k.paths)
	if errors.Is(err, knode.ErrNoShare) {
		return nil
	}
	if err != nil {
		return failed(connect.CodeInternal, "%v", err)
	}
	if share.NodeCA != nil {
		if err := pki.ValidateNodeCA(*share.NodeCA, osCA); err != nil {
			return failed(connect.CodeInvalidArgument, "the OS CAs do not verify the node's node CA, which it renews node certificates with: %v", err)
		}
	}
	return nil
}

// forceLeaves renews the control plane's certificates at once, on a control plane whose loop runs.
func (k *side) forceLeaves() {
	k.mu.Lock()
	leaves := k.leaves
	k.mu.Unlock()
	if leaves != nil {
		log.Print("renewing the control plane's certificates, as this test image does after each config")
		leaves.Force()
	}
}
