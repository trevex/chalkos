package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/pki"
)

// tokenRefresh is how long after the switch of the service-account key kubelets have renewed
// every projected token they hold: they renew at 80 % of its nominal hour.
const tokenRefresh = time.Hour

// Rotates reports whether kind is the Kubernetes CAs', the service-account key's or the
// encryption key's rotation.
func (p *Plugin) Rotates(kind clustersecrets.RotationKind) bool {
	switch kind {
	case clustersecrets.RotateKubernetesCA, clustersecrets.RotateServiceAccountKey, clustersecrets.RotateEncryptionKey:
		return true
	}
	return false
}

// RevokedNodes are the nodes the first control plane that answers with them lists as revoked, as
// revokedNodesOf reads them.
func (p *Plugin) RevokedNodes(ctx context.Context, r layer.Rotating) (map[string]string, error) {
	list, err := revokedNodesOf(ctx, rotatingNodesOf(r, k8s.KindControlPlane), func(ctx context.Context, name string) (*kubernetesv1.StatusResponse, error) {
		return kubernetesStatus(ctx, r, name)
	})
	if err != nil {
		return nil, err
	}
	since := make(map[string]string, len(list))
	for name, n := range list {
		since[name] = revokedSince(n)
	}
	return since, nil
}

// NodeCAHolders are the control planes.
func (p *Plugin) NodeCAHolders(c layer.Cluster) ([]string, error) {
	return nodesOf(c, k8s.KindControlPlane, "")
}

// kubernetesStatus reads the state of a node's Kubernetes, as nodeStatus does.
func kubernetesStatus(ctx context.Context, r layer.Rotating, name string) (*kubernetesv1.StatusResponse, error) {
	var st *kubernetesv1.StatusResponse
	err := r.Call(name, func(conn *client.Conn) error {
		var err error
		st, err = nodeStatus(ctx, conn)
		return err
	})
	return st, err
}

// expectation is what a node's status reports once it applied a phase: an entry of its trust,
// with the value that issues on control planes.
type expectation struct {
	name    string
	values  []string
	issuing string
}

// expectations are the trust entries a node of the Kubernetes kind confirms a phase of the
// rotation of kind by, as the secrets file has them now.
func expectations(s clustersecrets.Secrets, kind clustersecrets.RotationKind, nodeKind string) ([]expectation, error) {
	k := s.Kubernetes
	controlPlane := nodeKind == k8s.KindControlPlane
	var list []expectation
	switch kind {
	case clustersecrets.RotateKubernetesCA:
		cas := []struct{ name, bundle, issuing string }{{client.NameKubernetesCA, k.CABundle(), k.CA.Certificate}}
		if controlPlane {
			cas = append(cas,
				struct{ name, bundle, issuing string }{client.NameFrontProxyCA, k.FrontProxyCABundle(), k.FrontProxyCA.Certificate},
				struct{ name, bundle, issuing string }{client.NameEtcdCA, k.EtcdCABundle(), k.EtcdCA.Certificate})
		}
		for _, ca := range cas {
			values, err := pki.Fingerprints(ca.bundle)
			if err != nil {
				return nil, err
			}
			e := expectation{name: ca.name, values: values}
			if controlPlane {
				if e.issuing, err = layer.FingerprintOf(ca.issuing); err != nil {
					return nil, err
				}
			}
			list = append(list, e)
		}
	case clustersecrets.RotateServiceAccountKey:
		pubs, err := k.ServiceAccountPublicKeys()
		if err != nil {
			return nil, err
		}
		e := expectation{name: client.NameServiceAccountKeys}
		for _, pub := range pubs {
			fp, err := pki.PublicKeyFingerprint(pub)
			if err != nil {
				return nil, err
			}
			e.values = append(e.values, fp)
		}
		e.issuing = e.values[0]
		list = append(list, e)
	case clustersecrets.RotateEncryptionKey:
		e := expectation{name: client.NameEncryptionKeys}
		for _, key := range k.EncryptionKeys() {
			e.values = append(e.values, key.Fingerprint())
		}
		e.issuing = e.values[0]
		list = append(list, e)
	}
	return list, nil
}

// applied checks a node's status against the expectations.
func applied(st *nodev1.StatusResponse, want []expectation) error {
	for _, e := range want {
		if err := layer.Trusts(st, e.name, e.values, e.issuing); err != nil {
			return err
		}
	}
	return nil
}

// ApplyRotation runs a phase of the rotation of the Kubernetes CAs, the service-account key or
// the encryption key. Control planes take the phase one at a time, each confirming that it runs
// on its new files with a healthy etcd before the next one restarts; workers follow for the
// Kubernetes CAs.
func (p *Plugin) ApplyRotation(ctx context.Context, r layer.Rotating, kind clustersecrets.RotationKind, phase clustersecrets.Phase) error {
	workers := rotatingNodesOf(r, k8s.KindWorker)
	if phase == clustersecrets.PhaseRefresh {
		switch kind {
		case clustersecrets.RotateKubernetesCA:
			return refreshKubernetesCA(ctx, r, workers)
		case clustersecrets.RotateEncryptionKey:
			return rewriteEncrypted(ctx, r)
		}
		// Kubelets renew their pods' tokens with the new service-account key within the hour;
		// chalkos's addons, which the network and DNS depend on, get theirs at once when they
		// restart.
		return restartAddons(ctx, r)
	}
	if err := controlPlanesInTurn(ctx, r, kind); err != nil {
		return err
	}
	if kind != clustersecrets.RotateKubernetesCA {
		return nil
	}
	// A worker's share holds the CAs it trusts; which one issues matters to control planes alone.
	if phase != clustersecrets.PhaseSwitch {
		if err := workersTrust(ctx, r, workers); err != nil {
			return err
		}
	}
	if phase == clustersecrets.PhaseAccept {
		return restartAddons(ctx, r)
	}
	return nil
}

// restartAddons has a control plane restart chalkos's addons and the cluster's other workloads
// its manifests define.
func restartAddons(ctx context.Context, r layer.Rotating) error {
	return rotationThroughControlPlane(ctx, r, func(ctx context.Context, conn *client.Conn) error {
		resp, err := Client(conn).RotationStep(ctx, connect.NewRequest(&kubernetesv1.RotationStepRequest{Step: kubernetesv1.RotationStep_ROTATION_STEP_RESTART_ADDONS}))
		if err == nil {
			r.Say("  restarted %s", strings.Join(resp.Msg.Restarted, ", "))
			if len(resp.Msg.NotWaited) > 0 {
				r.Say("  their rollout does not complete by itself (OnDelete, a partition, paused), so it was not waited for: %s", strings.Join(resp.Msg.NotWaited, ", "))
			}
		}
		return err
	})
}

// controlPlanesInTurn delivers the current share to each control-plane node in turn. Each
// restarts its static pods on the new files, so the next one waits until the one before runs on
// them, answers ready, and etcd has every member healthy again: two control planes restarting at
// once could take etcd's quorum.
func controlPlanesInTurn(ctx context.Context, r layer.Rotating, kind clustersecrets.RotationKind) error {
	want, err := expectations(r.Secrets(), kind, k8s.KindControlPlane)
	if err != nil {
		return err
	}
	for _, name := range rotatingNodesOf(r, k8s.KindControlPlane) {
		if err := waitQuorum(ctx, r, name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := r.DeliverPayloads(ctx, name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := layer.WaitStatus(ctx, r, name, "to run on its new files", func(st *nodev1.StatusResponse) error {
			if err := applied(st, want); err != nil {
				return err
			}
			k, err := kubernetesStatus(ctx, r, name)
			if err != nil {
				return err
			}
			if k.GetPhase() == kubernetesv1.Phase_PHASE_BOOTSTRAPPED && !k.ControlPlaneCurrent {
				return fmt.Errorf("control plane: %s", k.ControlPlane)
			}
			return nil
		}); err != nil {
			return err
		}
		if err := waitQuorum(ctx, r, name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		r.Say("  %s runs on its new files", name)
	}
	return nil
}

// waitQuorum waits until every etcd member the node sees is a healthy voter. A node that is no
// etcd member yet has no members to wait for. etcd may take the whole timeout to become healthy,
// but a node whose chalkd does not answer at all is named after a few polls: it is down or
// addressed wrongly, which waiting does not fix.
func waitQuorum(ctx context.Context, r layer.Rotating, name string) error {
	window := min(10*r.Poll(), r.Timeout())
	var silent time.Time
	return layer.WaitFor(ctx, "etcd's members to be healthy", r.Timeout(), r.Poll(), func() error {
		var members []*kubernetesv1.EtcdMember
		var answer error
		err := r.Call(name, func(conn *client.Conn) error {
			resp, err := Client(conn).EtcdMembers(ctx, connect.NewRequest(&kubernetesv1.EtcdMembersRequest{}))
			if err == nil {
				members = resp.Msg.Members
			}
			answer = err
			return err
		})
		switch {
		case connect.IsWireError(answer) && notEtcdMember(answer):
			return nil
		case err != nil && (answer == nil || !connect.IsWireError(answer)):
			if silent.IsZero() {
				silent = time.Now()
			} else if time.Since(silent) >= window {
				return layer.Permanent{Err: fmt.Errorf("%s does not answer: %w", name, err)}
			}
			return err
		case err != nil:
			silent = time.Time{}
			return err
		}
		silent = time.Time{}
		for _, m := range members {
			switch {
			case m.Unhealthy != "":
				return fmt.Errorf("member %s is unhealthy: %s", m.Name, m.Unhealthy)
			case m.Learner:
				return fmt.Errorf("member %s is a learner", m.Name)
			}
		}
		return nil
	})
}

// workersTrust delivers the workers their shares and waits until their kubelets trust the CAs.
func workersTrust(ctx context.Context, r layer.Rotating, workers []string) error {
	want, err := expectations(r.Secrets(), clustersecrets.RotateKubernetesCA, k8s.KindWorker)
	if err != nil {
		return err
	}
	return layer.EachNode(workers, func(name string) error {
		if err := r.DeliverPayloads(ctx, name); err != nil {
			return err
		}
		if err := layer.WaitStatus(ctx, r, name, "to trust the Kubernetes CAs", func(st *nodev1.StatusResponse) error { return applied(st, want) }); err != nil {
			return err
		}
		r.Say("  %s trusts the Kubernetes CAs", name)
		return nil
	})
}

// refreshKubernetesCA gives workers kubelet client certificates of the new CA, which control
// planes issued themselves at the switch, and has every kubelet request a serving certificate,
// which the controller-manager signs with the new CA now that every control plane switched.
func refreshKubernetesCA(ctx context.Context, r layer.Rotating, workers []string) error {
	ca, err := layer.FingerprintOf(r.Secrets().Kubernetes.CA.Certificate)
	if err != nil {
		return err
	}
	if err := layer.EachNode(workers, func(name string) error {
		if st, err := r.Status(ctx, name); err == nil && layer.IssuedBy(st, client.NameKubeletClient, ca) == nil {
			return nil
		}
		if err := r.DeliverPayloads(ctx, name); err != nil {
			return err
		}
		return layer.WaitStatus(ctx, r, name, "to use a kubelet client certificate of the new CA", func(st *nodev1.StatusResponse) error {
			return layer.IssuedBy(st, client.NameKubeletClient, ca)
		})
	}); err != nil {
		return err
	}
	return layer.EachNode(append(rotatingNodesOf(r, k8s.KindControlPlane), workers...), func(name string) error {
		if st, err := r.Status(ctx, name); err == nil && layer.IssuedBy(st, client.NameKubeletServing, ca) == nil {
			r.Say("  %s's kubelet serves a certificate of the new CA already", name)
			return nil
		}
		if err := r.Call(name, func(conn *client.Conn) error {
			ctx, cancel := context.WithTimeout(ctx, r.Timeout())
			defer cancel()
			_, err := Client(conn).RotationStep(ctx, connect.NewRequest(&kubernetesv1.RotationStepRequest{Step: kubernetesv1.RotationStep_ROTATION_STEP_RENEW_KUBELET_SERVING}))
			return err
		}); err != nil {
			return err
		}
		if err := layer.WaitStatus(ctx, r, name, "for a kubelet serving certificate of the new CA", func(st *nodev1.StatusResponse) error {
			return layer.IssuedBy(st, client.NameKubeletServing, ca)
		}); err != nil {
			return err
		}
		r.Say("  %s's kubelet serves a certificate of the new CA", name)
		return nil
	})
}

// rewriteEncrypted has a control plane rewrite every encrypted object and checks that etcd
// holds none under the old key afterwards.
func rewriteEncrypted(ctx context.Context, r layer.Rotating) error {
	old := r.Secrets().Kubernetes.Accepted.EncryptionKeys
	if len(old) != 1 {
		return errors.New("the secrets file accepts no single old encryption key")
	}
	deadline := time.Now().Add(r.Timeout())
	counts, err := encryptedCounts(ctx, r, kubernetesv1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED)
	// A cluster with many Secrets takes longer to rewrite than the default allows. Whatever
	// stopped once the time was up, the node's error or the call's own, the time is the cause.
	if err != nil && (connect.CodeOf(err) == connect.CodeDeadlineExceeded || !time.Now().Before(deadline)) {
		return fmt.Errorf("%w; the rewrite of the encrypted objects took longer than --timeout %v, so give it more time, as with chalkctl rotate %s --resume --timeout %v. --resume repeats the rewrite safely: it writes every object back unchanged, those rewritten already included", err, r.Timeout(), clustersecrets.RotateEncryptionKey, 2*r.Timeout())
	}
	if err != nil {
		return err
	}
	if n := counts[old[0].Name]; n > 0 {
		return fmt.Errorf("etcd still holds %s under the old key %s", layer.Count(n, "object"), old[0].Name)
	}
	return nil
}

// FinishGuard refuses to remove an old service-account key within the hour after every
// control plane applied the switch, unless forced, and an old encryption key while etcd holds
// objects encrypted with it.
func (p *Plugin) FinishGuard(ctx context.Context, r layer.Rotating, kind clustersecrets.RotationKind, force bool) error {
	switch kind {
	case clustersecrets.RotateServiceAccountKey:
		at := r.Secrets().Rotation.Switched.Add(tokenRefresh)
		if r.Now().Before(at) && !force {
			return fmt.Errorf("tokens signed with the old service-account key are renewed until %s, an hour after every control plane applied the switch; finish then, or pass --force to refuse the tokens not renewed yet", at.Local().Format(time.RFC3339))
		}
	case clustersecrets.RotateEncryptionKey:
		old := r.Secrets().Kubernetes.Accepted.EncryptionKeys
		if len(old) != 1 {
			return errors.New("the secrets file accepts no single old encryption key")
		}
		counts, err := encryptedCounts(ctx, r, kubernetesv1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED)
		if err != nil {
			return err
		}
		if n := counts[old[0].Name]; n > 0 {
			return fmt.Errorf("etcd holds %s encrypted with the old key %s, which removing it would lose; rewrite them with chalkctl rotate encryption-key --resume", layer.Count(n, "object"), old[0].Name)
		}
	}
	return nil
}

// Paused says what the operator does before the rotation of a Kubernetes CA or key
// continues.
func (p *Plugin) Paused(ctx context.Context, r layer.Rotating, rot clustersecrets.Rotation) error {
	kind := rot.Kind
	switch {
	case kind == clustersecrets.RotateKubernetesCA && rot.Phase == clustersecrets.PhaseAccept:
		r.Say("Every node trusts the old and the new Kubernetes CAs, and chalkos's addons and the workloads of the Addon documents, StatefulSets included, restarted. Restart your other workloads that talk to the API server, so they trust both CAs too; pods started from now on do. Issue new kubeconfigs now with chalkctl kubeconfig: they carry both CAs and work throughout the rotation and after it; kubeconfigs from before the rotation stop verifying the API server at the switch. Then continue with chalkctl rotate %s --resume, which makes the new CAs issue.", kind)
		return nil
	case kind == clustersecrets.RotateKubernetesCA:
		r.Say("The new Kubernetes CAs issue every certificate of the cluster, and the kubelets requested new serving certificates. Kubeconfigs from before the rotation cannot verify the API server any more and are refused after the finish: issue them again with chalkctl kubeconfig. Then remove the old CAs with chalkctl rotate %s --finish.", kind)
	case kind == clustersecrets.RotateServiceAccountKey:
		at := rot.Switched.Add(tokenRefresh)
		r.Say("The API server signs tokens with the new service-account key, and chalkos's addons and the workloads of the Addon documents, StatefulSets included, restarted with new tokens. Kubelets renew the tokens of other pods within the hour after every control plane applied the switch; finish from %s with chalkctl rotate %s --finish.", at.Local().Format(time.RFC3339), kind)
		var secrets []string
		err := rotationThroughControlPlane(ctx, r, func(ctx context.Context, conn *client.Conn) error {
			resp, err := Client(conn).RotationStep(ctx, connect.NewRequest(&kubernetesv1.RotationStepRequest{Step: kubernetesv1.RotationStep_ROTATION_STEP_LIST_TOKEN_SECRETS}))
			if err == nil {
				secrets = resp.Msg.TokenSecrets
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("list the Secrets of service-account tokens: %w", err)
		}
		if len(secrets) > 0 {
			r.Say("These Secrets of type kubernetes.io/service-account-token hold tokens of the old key, which nothing signs again and which are refused after the finish; recreate them: %s", strings.Join(secrets, ", "))
		}
	case kind == clustersecrets.RotateEncryptionKey:
		r.Say("etcd holds every encrypted object under the new key; remove the old key with chalkctl rotate %s --finish.", kind)
	}
	return nil
}

// rotationThroughControlPlane calls fn on the first control-plane node that answers as an etcd
// member. A step waits on the node for what it needs, up to the timeout.
func rotationThroughControlPlane(ctx context.Context, r layer.Rotating, fn func(ctx context.Context, conn *client.Conn) error) error {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout())
	defer cancel()
	nodes, err := r.Nodes()
	if err != nil {
		return err
	}
	names := rotatingNodesOf(r, k8s.KindControlPlane)
	if len(names) == 0 {
		return errors.New("the cluster has no control-plane node to ask")
	}
	return nodes.ViaFirst(names, tryNextControlPlane, func(conn *client.Conn) error { return fn(ctx, conn) })
}

// encryptedCounts runs a rotation step that counts the objects etcd holds and returns them by key
// name, "" for those stored unencrypted.
func encryptedCounts(ctx context.Context, r layer.Rotating, step kubernetesv1.RotationStep) (map[string]uint64, error) {
	counts := map[string]uint64{}
	err := rotationThroughControlPlane(ctx, r, func(ctx context.Context, conn *client.Conn) error {
		resp, err := Client(conn).RotationStep(ctx, connect.NewRequest(&kubernetesv1.RotationStepRequest{Step: step}))
		if err != nil {
			return err
		}
		if resp.Msg.Rewritten > 0 {
			r.Say("  rewrote %s unchanged", layer.Count(resp.Msg.Rewritten, "encrypted object"))
		}
		for _, e := range resp.Msg.Encrypted {
			counts[e.Key] += e.Objects
			key := e.Key
			if key == "" {
				key = "no key"
			}
			r.Say("  etcd holds %d of the %s under %s", e.Objects, e.Resource, key)
		}
		return nil
	})
	return counts, err
}
