package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

// tokenRefresh is how long after the switch of the service-account key kubelets have renewed
// every projected token they hold: they renew at 80 % of its nominal hour.
const tokenRefresh = time.Hour

// expectation is what a node's status reports once it applied a phase: an entry of its trust,
// with the value that issues on control planes.
type expectation struct {
	name    string
	values  []string
	issuing string
}

// expectations are the trust entries a node of the Kubernetes kind confirms a phase of the
// rotation of kind by, as the secrets file has them now.
func expectations(s pki.Secrets, kind, nodeKind string) ([]expectation, error) {
	k := s.Kubernetes
	controlPlane := nodeKind == manifest.KindControlPlane
	var list []expectation
	switch kind {
	case pki.RotateKubernetesCA:
		cas := []struct{ name, bundle, issuing string }{{"Kubernetes CA", k.CABundle(), k.CA.Certificate}}
		if controlPlane {
			cas = append(cas,
				struct{ name, bundle, issuing string }{"front-proxy CA", k.FrontProxyCABundle(), k.FrontProxyCA.Certificate},
				struct{ name, bundle, issuing string }{"etcd CA", k.EtcdCABundle(), k.EtcdCA.Certificate})
		}
		for _, ca := range cas {
			values, err := pki.Fingerprints(ca.bundle)
			if err != nil {
				return nil, err
			}
			e := expectation{name: ca.name, values: values}
			if controlPlane {
				if e.issuing, err = fingerprintOf(ca.issuing); err != nil {
					return nil, err
				}
			}
			list = append(list, e)
		}
	case pki.RotateServiceAccountKey:
		pubs, err := k.ServiceAccountPublicKeys()
		if err != nil {
			return nil, err
		}
		e := expectation{name: "service-account keys"}
		for _, pub := range pubs {
			fp, err := pki.PublicKeyFingerprint(pub)
			if err != nil {
				return nil, err
			}
			e.values = append(e.values, fp)
		}
		e.issuing = e.values[0]
		list = append(list, e)
	case pki.RotateEncryptionKey:
		e := expectation{name: "encryption keys"}
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
		if err := trusts(st, e.name, e.values, e.issuing); err != nil {
			return err
		}
	}
	return nil
}

// applyKubernetes runs a phase of the rotation of the Kubernetes CAs, the service-account key or
// the encryption key. Control planes take the phase one at a time, each confirming that it runs
// on its new files with a healthy etcd before the next one restarts; workers follow for the
// Kubernetes CAs.
func (r *rotation) applyKubernetes(ctx context.Context, kind, phase string) error {
	workers := r.nodesOf(manifest.KindWorker)
	if phase == pki.PhaseRefresh {
		switch kind {
		case pki.RotateKubernetesCA:
			return r.refreshKubernetesCA(ctx, workers)
		case pki.RotateEncryptionKey:
			return r.rewriteEncrypted(ctx)
		}
		// Kubelets renew their pods' tokens with the new service-account key within the hour;
		// chalkos's addons, which the network and DNS depend on, get theirs at once when they
		// restart.
		return r.restartAddons(ctx)
	}
	if err := r.controlPlanesInTurn(ctx, kind); err != nil {
		return err
	}
	if kind != pki.RotateKubernetesCA {
		return nil
	}
	// A worker's share holds the CAs it trusts; which one issues matters to control planes alone.
	if phase != pki.PhaseSwitch {
		if err := r.workersTrust(ctx, workers); err != nil {
			return err
		}
	}
	if phase == pki.PhaseAccept {
		return r.restartAddons(ctx)
	}
	return nil
}

// restartAddons has a control plane restart chalkos's addons and the cluster's other workloads
// its manifests define.
func (r *rotation) restartAddons(ctx context.Context) error {
	return r.throughControlPlane(ctx, func(ctx context.Context, conn *client.Conn) error {
		resp, err := conn.RotationStep(ctx, connect.NewRequest(&nodev1.RotationStepRequest{Step: nodev1.RotationStep_ROTATION_STEP_RESTART_ADDONS}))
		if err == nil {
			r.say("  restarted %s", strings.Join(resp.Msg.Restarted, ", "))
		}
		return err
	})
}

// controlPlanesInTurn delivers the current share to each control-plane node in turn. Each
// restarts its static pods on the new files, so the next one waits until the one before runs on
// them, answers ready, and etcd has every member healthy again: two control planes restarting at
// once could take etcd's quorum.
func (r *rotation) controlPlanesInTurn(ctx context.Context, kind string) error {
	want, err := expectations(r.file.secrets, kind, manifest.KindControlPlane)
	if err != nil {
		return err
	}
	for _, name := range r.nodesOf(manifest.KindControlPlane) {
		if err := r.waitQuorum(ctx, name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := r.deliverShare(ctx, name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := r.waitStatus(ctx, name, "to run on its new files", func(st *nodev1.StatusResponse) error {
			if err := applied(st, want); err != nil {
				return err
			}
			if k := st.Kubernetes; k != nil && strings.HasPrefix(k.State, "bootstrapped") && k.ControlPlane != "current" {
				return fmt.Errorf("control plane: %s", k.ControlPlane)
			}
			return nil
		}); err != nil {
			return err
		}
		if err := r.waitQuorum(ctx, name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		r.say("  %s runs on its new files", name)
	}
	return nil
}

// waitQuorum waits until every etcd member the node sees is a healthy voter. A node that is no
// etcd member yet has no members to wait for.
func (r *rotation) waitQuorum(ctx context.Context, name string) error {
	return r.waitFor(ctx, "etcd's members to be healthy", func() error {
		var members []*nodev1.EtcdMember
		err := r.call(name, func(conn *client.Conn, _ *target) error {
			resp, err := conn.EtcdMembers(ctx, connect.NewRequest(&nodev1.EtcdMembersRequest{}))
			if err == nil {
				members = resp.Msg.Members
			}
			return err
		})
		var ce *connect.Error
		if errors.As(err, &ce) && ce.Code() == connect.CodeFailedPrecondition && strings.Contains(ce.Message(), "is not an etcd member") {
			return nil
		}
		if err != nil {
			return err
		}
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
func (r *rotation) workersTrust(ctx context.Context, workers []string) error {
	want, err := expectations(r.file.secrets, pki.RotateKubernetesCA, manifest.KindWorker)
	if err != nil {
		return err
	}
	return r.eachNode(workers, func(name string) error {
		if err := r.deliverShare(ctx, name); err != nil {
			return err
		}
		if err := r.waitStatus(ctx, name, "to trust the Kubernetes CAs", func(st *nodev1.StatusResponse) error { return applied(st, want) }); err != nil {
			return err
		}
		r.say("  %s trusts the Kubernetes CAs", name)
		return nil
	})
}

// refreshKubernetesCA gives workers kubelet client certificates of the new CA, which control
// planes issued themselves at the switch, and has every kubelet request a serving certificate,
// which the controller-manager signs with the new CA now that every control plane switched.
func (r *rotation) refreshKubernetesCA(ctx context.Context, workers []string) error {
	ca, err := fingerprintOf(r.file.secrets.Kubernetes.CA.Certificate)
	if err != nil {
		return err
	}
	if err := r.eachNode(workers, func(name string) error {
		if st, err := r.status(ctx, name); err == nil && issuedBy(st, "kubelet client", ca) == nil {
			return nil
		}
		if err := r.deliverShare(ctx, name); err != nil {
			return err
		}
		return r.waitStatus(ctx, name, "to use a kubelet client certificate of the new CA", func(st *nodev1.StatusResponse) error {
			return issuedBy(st, "kubelet client", ca)
		})
	}); err != nil {
		return err
	}
	return r.eachNode(append(r.nodesOf(manifest.KindControlPlane), workers...), func(name string) error {
		if st, err := r.status(ctx, name); err == nil && issuedBy(st, "kubelet serving", ca) == nil {
			r.say("  %s's kubelet serves a certificate of the new CA already", name)
			return nil
		}
		if err := r.call(name, func(conn *client.Conn, _ *target) error {
			ctx, cancel := context.WithTimeout(ctx, r.timeout)
			defer cancel()
			_, err := conn.RotationStep(ctx, connect.NewRequest(&nodev1.RotationStepRequest{Step: nodev1.RotationStep_ROTATION_STEP_RENEW_KUBELET_SERVING}))
			return err
		}); err != nil {
			return err
		}
		if err := r.waitStatus(ctx, name, "for a kubelet serving certificate of the new CA", func(st *nodev1.StatusResponse) error {
			return issuedBy(st, "kubelet serving", ca)
		}); err != nil {
			return err
		}
		r.say("  %s's kubelet serves a certificate of the new CA", name)
		return nil
	})
}

// rewriteEncrypted has a control plane rewrite every encrypted object and checks that etcd
// holds none under the old key afterwards.
func (r *rotation) rewriteEncrypted(ctx context.Context) error {
	old := r.file.secrets.Kubernetes.Accepted.EncryptionKeys
	if len(old) != 1 {
		return errors.New("the secrets file accepts no single old encryption key")
	}
	counts, err := r.encryptedCounts(ctx, nodev1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED)
	if err != nil {
		return err
	}
	if n := counts[old[0].Name]; n > 0 {
		return fmt.Errorf("etcd still holds %d objects under the old key %s", n, old[0].Name)
	}
	return nil
}

// pausesAfter reports whether the rotation stops after the phase for the operator: the Kubernetes
// CAs after their accept phase, so workloads restart to trust the new CA before it issues the API
// server's certificate.
func pausesAfter(kind, phase string) bool {
	return kind == pki.RotateKubernetesCA && phase == pki.PhaseAccept
}

// finishGuardKubernetes refuses to remove an old service-account key within the hour after the
// switch, unless forced, and an old encryption key while etcd holds objects encrypted with it.
func (r *rotation) finishGuardKubernetes(ctx context.Context, kind string, force bool) error {
	switch kind {
	case pki.RotateServiceAccountKey:
		at := r.file.secrets.Rotation.Switched.Add(tokenRefresh)
		if r.a.now().Before(at) && !force {
			return fmt.Errorf("tokens signed with the old service-account key are renewed until %s, an hour after the switch; finish then, or pass --force to refuse the tokens not renewed yet", at.Local().Format(time.RFC3339))
		}
	case pki.RotateEncryptionKey:
		old := r.file.secrets.Kubernetes.Accepted.EncryptionKeys
		if len(old) != 1 {
			return errors.New("the secrets file accepts no single old encryption key")
		}
		counts, err := r.encryptedCounts(ctx, nodev1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED)
		if err != nil {
			return err
		}
		if n := counts[old[0].Name]; n > 0 {
			return fmt.Errorf("etcd holds %d objects encrypted with the old key %s, which removing it would lose; rewrite them with chalkctl rotate encryption-key --resume", n, old[0].Name)
		}
	}
	return nil
}

// pausedKubernetes says what the operator does before the rotation of a Kubernetes CA or key
// continues.
func (r *rotation) pausedKubernetes(ctx context.Context, rot pki.Rotation) error {
	kind := rot.Kind
	switch {
	case kind == pki.RotateKubernetesCA && rot.Phase == pki.PhaseAccept:
		r.say("Every node trusts the old and the new Kubernetes CAs, and chalkos's addons restarted. Restart your workloads that talk to the API server, so they trust both CAs too; pods started from now on do. Then continue with chalkctl rotate %s --resume, which makes the new CAs issue.", kind)
		return nil
	case kind == pki.RotateKubernetesCA:
		r.say("The new Kubernetes CAs issue every certificate of the cluster, and the kubelets requested new serving certificates. Kubeconfigs from chalkctl kubeconfig before the rotation are refused after the finish: issue them again with chalkctl kubeconfig. Then remove the old CAs with chalkctl rotate %s --finish.", kind)
	case kind == pki.RotateServiceAccountKey:
		at := rot.Switched.Add(tokenRefresh)
		r.say("The API server signs tokens with the new service-account key, and chalkos's addons restarted with new tokens. Kubelets renew the tokens of other pods within the hour after the switch; finish from %s with chalkctl rotate %s --finish.", at.Local().Format(time.RFC3339), kind)
		var secrets []string
		err := r.throughControlPlane(ctx, func(ctx context.Context, conn *client.Conn) error {
			resp, err := conn.RotationStep(ctx, connect.NewRequest(&nodev1.RotationStepRequest{Step: nodev1.RotationStep_ROTATION_STEP_LIST_TOKEN_SECRETS}))
			if err == nil {
				secrets = resp.Msg.TokenSecrets
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("list the Secrets of service-account tokens: %w", err)
		}
		if len(secrets) > 0 {
			r.say("These Secrets of type kubernetes.io/service-account-token hold tokens of the old key, which nothing signs again and which are refused after the finish; recreate them: %s", strings.Join(secrets, ", "))
		}
	case kind == pki.RotateEncryptionKey:
		r.say("etcd holds every encrypted object under the new key; remove the old key with chalkctl rotate %s --finish.", kind)
	}
	return nil
}

// throughControlPlane calls fn on the first control-plane node that answers as an etcd member.
// A step waits on the node for what it needs, up to the timeout.
func (r *rotation) throughControlPlane(ctx context.Context, fn func(ctx context.Context, conn *client.Conn) error) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	var last error = errors.New("the cluster has no control-plane node")
	for _, name := range r.nodesOf(manifest.KindControlPlane) {
		err := r.call(name, func(conn *client.Conn, _ *target) error { return fn(ctx, conn) })
		if err == nil {
			return nil
		}
		last = fmt.Errorf("%s: %w", name, err)
		if !tryNextControlPlane(err) {
			return last
		}
	}
	return last
}

// encryptedCounts runs a rotation step that counts the objects etcd holds and returns them by key
// name, "" for those stored unencrypted.
func (r *rotation) encryptedCounts(ctx context.Context, step nodev1.RotationStep) (map[string]uint64, error) {
	counts := map[string]uint64{}
	err := r.throughControlPlane(ctx, func(ctx context.Context, conn *client.Conn) error {
		resp, err := conn.RotationStep(ctx, connect.NewRequest(&nodev1.RotationStepRequest{Step: step}))
		if err != nil {
			return err
		}
		if resp.Msg.Rewritten > 0 {
			r.say("  rewrote %d encrypted objects unchanged", resp.Msg.Rewritten)
		}
		for _, e := range resp.Msg.Encrypted {
			counts[e.Key] += e.Objects
			key := e.Key
			if key == "" {
				key = "no key"
			}
			r.say("  etcd holds %d of the %s under %s", e.Objects, e.Resource, key)
		}
		return nil
	})
	return counts, err
}
