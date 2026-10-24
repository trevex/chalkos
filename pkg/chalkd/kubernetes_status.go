package chalkd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// preparation is how far the preparation of the node's Kubernetes files got.
type preparation struct {
	// failure is why the last preparation failed; "" unless it did.
	failure string
	// done is set once a preparation succeeded.
	done bool
}

// readPreparation reads how far the preparation of the node's Kubernetes files got.
func readPreparation(p knode.Paths) (preparation, error) {
	reason, err := knode.PreparationError(p)
	if err != nil || reason != "" {
		return preparation{failure: reason}, err
	}
	done, err := knode.Prepared(p)
	return preparation{done: done}, err
}

// problem says why the node's Kubernetes files cannot be used, "" once they can.
func (p preparation) problem() string {
	switch {
	case p.failure != "":
		return "preparation failed: " + p.failure
	case !p.done:
		return "preparing"
	}
	return ""
}

// status describes the node's Kubernetes state.
func (k *Kubernetes) status(ctx context.Context) (*nodev1.KubernetesStatus, error) {
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return nil, err
	}
	st := &nodev1.KubernetesStatus{Kind: c.Kind, Phase: nodev1.KubernetesPhase_KUBERNETES_PHASE_PREPARING}
	if _, err := os.Stat(k.Paths.Share()); errors.Is(err, os.ErrNotExist) {
		st.State = "no share"
		return st, nil
	}
	if prep, err := readPreparation(k.Paths); err != nil {
		return nil, err
	} else if problem := prep.problem(); problem != "" {
		st.State = problem
		return st, nil
	}
	m, leftovers, err := knode.ReadMembership(k.Paths)
	if err != nil {
		return nil, err
	}
	switch {
	case c.Kind == k8s.KindWorker:
		st.State, st.Phase = "joined", nodev1.KubernetesPhase_KUBERNETES_PHASE_JOINED
	case m == knode.MembershipLeft:
		st.State, st.Phase = "left etcd; reinstall the node to join the cluster again", nodev1.KubernetesPhase_KUBERNETES_PHASE_LEFT
		return st, nil
	case m == knode.MembershipBootstrapped && leftovers.EtcdDataMissing:
		st.State, st.Phase = "etcd data missing: restore etcd or reinstall the node", nodev1.KubernetesPhase_KUBERNETES_PHASE_ETCD_DATA_MISSING
		return st, nil
	case m == knode.MembershipBootstrapped:
		st.State, st.Phase = "bootstrapped", nodev1.KubernetesPhase_KUBERNETES_PHASE_BOOTSTRAPPED
		k.mu.Lock()
		for _, problem := range []string{k.pinProblem, k.split} {
			if problem != "" {
				st.State += ": " + problem
			}
		}
		k.mu.Unlock()
		if len(c.VIP.Addresses) > 0 {
			st.Vip = "standby"
			if k.vipHolder.Load() {
				st.Vip = "holder"
			}
		}
		if share, err := knode.ReadShare(k.Paths); err == nil {
			st.ControlPlane, st.ControlPlaneCurrent = controlPlaneCurrent, true
			if err := k.hooks.CheckControlPlane(ctx, c, share); err != nil {
				st.ControlPlane, st.ControlPlaneCurrent = err.Error(), false
			}
		}
	default:
		st.State = k.joinStatus(c, m == knode.MembershipJoining)
		st.Phase = nodev1.KubernetesPhase_KUBERNETES_PHASE_WAITING
		if m == knode.MembershipJoining {
			st.Phase = nodev1.KubernetesPhase_KUBERNETES_PHASE_JOINING
		}
		return st, nil
	}
	if c.Kind == k8s.KindControlPlane {
		rctx, cancel := context.WithTimeout(ctx, k.timing.StatusTimeout)
		k.revokedStatus(rctx, st)
		cancel()
	}
	ctx, cancel := context.WithTimeout(ctx, k.timing.StatusTimeout)
	defer cancel()
	node, err := k.hooks.Node(ctx)
	if err != nil {
		st.NodeReady = fmt.Sprintf("unknown: %v", err)
		return st, nil
	}
	st.NodeReady = nodeReady(node)
	if c.Kind == k8s.KindWorker {
		problem, err := missingPodRanges(c, node)
		if err != nil {
			return nil, err
		}
		if problem != "" {
			st.State += ": " + problem
		}
	}
	return st, nil
}

// controlPlaneCurrent is the control plane's state in the status once checkControlPlane passes.
const controlPlaneCurrent = "current"

// checkControlPlane checks that the API server and etcd present the certificates in the control
// plane's files and that the API server answers ready. Once the files are replaced, the kubelet
// starts the static pods again; until then they run on the certificates before.
func (k *Kubernetes) checkControlPlane(ctx context.Context, c k8s.Cluster, share kpki.Share) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, server := range []struct{ name, url, file string }{
		{"the API server", c.LocalAPIServer(), kpki.FileAPIServer},
		{"etcd", k.localEtcd(c), kpki.FileEtcdServer},
	} {
		u, err := url.Parse(server.url)
		if err != nil {
			return fmt.Errorf("%s: %v", server.name, err)
		}
		if err := servesFile(ctx, u.Host, filepath.Join(k.Paths.PKI, server.file)); err != nil {
			return fmt.Errorf("%s %v", server.name, err)
		}
	}
	if !k.hooks.APIServerReady(ctx, c, share) {
		return errors.New("the API server is not ready")
	}
	return nil
}

// servesFile checks that the TLS server at addr presents the certificate the file holds. The
// server is not verified: its certificate is only compared with the file, and nothing is sent.
func servesFile(ctx context.Context, addr, file string) error {
	want, err := readPEMCertificate(file)
	if err != nil {
		return fmt.Errorf("has no certificate: %v", err)
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("does not answer: %v", err)
	}
	defer conn.Close()
	if got := conn.(*tls.Conn).ConnectionState().PeerCertificates; len(got) == 0 || !got[0].Equal(want) {
		return errors.New("serves certificates the node replaced")
	}
	return nil
}

// node reads the node's Node object with the kubelet's credentials, which may read their own
// node.
func (k *Kubernetes) node(ctx context.Context) (*corev1.Node, error) {
	n, err := k8s.ReadNode(k.Paths.NodeFile)
	if err != nil {
		return nil, err
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", k.Paths.Kubeconfig())
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 5 * time.Second
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return client.CoreV1().Nodes().Get(ctx, n.Name, metav1.GetOptions{})
}

// nodeReady returns the status of the Node's Ready condition.
func nodeReady(node *corev1.Node) string {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return string(cond.Status)
		}
	}
	return string(corev1.ConditionUnknown)
}

// missingPodRanges says which of the cluster's families the Node has no pod range of, once the
// controller-manager allocated it any: it allocates ranges of the families the control plane
// runs with, which a worker's image cannot change.
func missingPodRanges(c k8s.Cluster, node *corev1.Node) (string, error) {
	if len(node.Spec.PodCIDRs) == 0 {
		return "", nil
	}
	families := c.Families()
	have := map[nodeip.Family]bool{}
	for _, cidr := range node.Spec.PodCIDRs {
		if p, err := netip.ParsePrefix(cidr); err == nil {
			have[nodeip.FamilyOf(p.Addr())] = true
		}
	}
	var missing []string
	for _, f := range families {
		if !have[f] {
			missing = append(missing, string(f))
		}
	}
	if len(missing) == 0 {
		return "", nil
	}
	return fmt.Sprintf("the Node has no %s pod range; the control plane allocates ranges of the families the cluster was created with", strings.Join(missing, " or ")), nil
}
