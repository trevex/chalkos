package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/pki"
)

type rotateFlags struct {
	cluster               clusterFlags
	secrets               secretFlags
	change                changeFlags
	addresses             addressList
	resume, finish, force bool
	ignoreRevoked         bool
	timeout               time.Duration
}

func (a *app) rotateCommand() *cobra.Command {
	f := rotateFlags{addresses: addressList{}}
	cmd := a.command(&cobra.Command{
		Use:     "rotate <kind>",
		Short:   "Rotate os-ca, kubernetes-ca, service-account-key or encryption-key",
		Long:    rotateLong,
		Example: rotateExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.rotate(ctx, f, pos) })
	fs := cmd.Flags()
	f.cluster.register(fs)
	f.secrets.register(fs)
	f.change.register(fs)
	registerAddresses(fs, f.addresses)
	fs.BoolVar(&f.resume, "resume", false, "continue the rotation the secrets file records")
	fs.BoolVar(&f.finish, "finish", false, "remove the old value from every node and the secrets file")
	fs.BoolVar(&f.force, "force", false, "with --finish, finish the service-account key's rotation within the hour after its switch")
	fs.BoolVar(&f.ignoreRevoked, "ignore-revoked", false, "rotate every node of the inventory, revoked ones included, without reading the revoked nodes, as while no control plane can say which are")
	fs.DurationVar(&f.timeout, "timeout", 10*time.Minute, "how long to wait for each node to apply a phase")
	return cmd
}

func (a *app) rotate(ctx context.Context, f rotateFlags, pos []string) error {
	cf, sf, change, addresses := f.cluster, f.secrets, f.change, f.addresses
	switch {
	case f.resume && f.finish:
		return cli.Usagef("pass --resume or --finish, not both")
	case f.force && !f.finish:
		return cli.Usagef("--force goes with --finish")
	}
	kind := clustersecrets.RotationKind(pos[0])
	if !slices.Contains(clustersecrets.RotationKinds, kind) {
		return cli.Usagef("unknown kind %q; rotate one of %s", kind, clustersecrets.RotationKindList())
	}
	c, err := loadCluster(ctx, cf)
	if err != nil {
		return err
	}
	if err := c.requireNodes("rotate"); err != nil {
		return err
	}
	if err := c.checkAddresses(addresses); err != nil {
		return err
	}
	// Every node's kind is read before any node is sent anything.
	if _, err := c.nodesOf("", ""); err != nil {
		return err
	}
	file, err := a.openSecrets(ctx, sf, change)
	if err != nil {
		return err
	}
	defer file.Close()
	r := &rotation{a: a, cluster: c, file: file, addresses: addresses, timeout: f.timeout, poll: a.poll(), ignoreRevoked: f.ignoreRevoked}
	if f.ignoreRevoked {
		fmt.Fprintln(a.stderr, "chalkctl: warning: --ignore-revoked: rotate reads no revoked nodes, so a revoked node still in the inventory "+
			"receives the new value and certificates of it like any other; remove revoked nodes from the inventory first")
	}
	if err := r.skipRevoked(ctx); err != nil {
		return err
	}
	switch {
	case f.finish:
		return r.finish(ctx, kind, f.force)
	case f.resume:
		return r.resume(ctx, kind)
	}
	return r.start(ctx, kind)
}

// rotation runs the phases of a rotation on the nodes of the cluster, recording each in the
// secrets file.
type rotation struct {
	a       *app
	cluster *cluster
	file    *secretsFile
	// addresses are the nodes' chalkd addresses that --address gives.
	addresses map[string]string
	// timeout bounds each wait for a node; poll is the time between two looks.
	timeout, poll time.Duration
	// revoked are the nodes the rotation skips, because a control plane lists them as revoked.
	// With ignoreRevoked, the list is not read and no node is skipped.
	revoked       map[string]bool
	ignoreRevoked bool
}

// say prints a line of the rotation's progress.
func (r *rotation) say(format string, args ...any) {
	fmt.Fprintf(r.a.stdout, format+"\n", args...)
}

// save changes a copy of the secrets and writes it, so the file and what chalkctl holds never
// differ.
func (r *rotation) save(change func(s *clustersecrets.Secrets) error) error {
	s := r.file.secrets
	if s.Rotation != nil {
		copied := *s.Rotation
		s.Rotation = &copied
	}
	if err := change(&s); err != nil {
		return err
	}
	return r.a.writeSecretsFile(r.file, s)
}

func (r *rotation) start(ctx context.Context, kind clustersecrets.RotationKind) error {
	if err := r.save(func(s *clustersecrets.Secrets) error { return s.BeginRotation(kind, r.a.now()) }); err != nil {
		return err
	}
	r.say("%s records the rotation of the %s; %s", r.file.out, kind.Name(), r.file.written())
	return r.run(ctx)
}

func (r *rotation) resume(ctx context.Context, kind clustersecrets.RotationKind) error {
	if err := r.running(kind); err != nil {
		return err
	}
	return r.run(ctx)
}

// running checks that the secrets file records a rotation of kind.
func (r *rotation) running(kind clustersecrets.RotationKind) error {
	switch rot := r.file.secrets.Rotation; {
	case rot == nil:
		return fmt.Errorf("%s records no rotation; start one with chalkctl rotate %s", r.file.path, kind)
	case rot.Kind != kind:
		return fmt.Errorf("%s records a rotation of the %s, not of the %s", r.file.path, rot.Kind.Name(), kind.Name())
	}
	return nil
}

func (r *rotation) finish(ctx context.Context, kind clustersecrets.RotationKind, force bool) error {
	if err := r.running(kind); err != nil {
		return err
	}
	rot := r.file.secrets.Rotation
	if rot.Phase != clustersecrets.PhaseFinish {
		if rot.Phase != clustersecrets.PhaseRefresh || !rot.Applied {
			return fmt.Errorf("the rotation of the %s is in its %s phase; finish it once its refresh phase is applied, after chalkctl rotate %s --resume", kind.Name(), rot.Phase, kind)
		}
		if err := r.finishGuard(ctx, kind, force); err != nil {
			return err
		}
		if err := r.save(func(s *clustersecrets.Secrets) error { return s.FinishRotation() }); err != nil {
			return err
		}
	}
	return r.run(ctx)
}

// finishGuard refuses to remove an old value something still needs.
func (r *rotation) finishGuard(ctx context.Context, kind clustersecrets.RotationKind, force bool) error {
	if kind == clustersecrets.RotateOSCA {
		return nil
	}
	return r.finishGuardKubernetes(ctx, kind, force)
}

// run applies the phase the secrets file records and the ones after it, up to the kind's pause
// or the end of the finish.
func (r *rotation) run(ctx context.Context) error {
	// rotate read the revoked nodes before the first phase; a node revoked while a phase ran is
	// left out of the phases after it.
	first := true
	for {
		rot := *r.file.secrets.Rotation
		if !rot.Applied {
			if !first {
				if err := r.skipRevoked(ctx); err != nil {
					return fmt.Errorf("the %s phase of the rotation of the %s did not start: %w; continue with chalkctl rotate %s --resume", rot.Phase, rot.Kind.Name(), err, rot.Kind)
				}
			}
			first = false
			r.say("%s: the %s phase", rot.Kind, rot.Phase)
			if err := r.apply(ctx, rot.Kind, rot.Phase); err != nil {
				return fmt.Errorf("the %s phase of the rotation of the %s stopped: %w; once that is fixed, continue with chalkctl rotate %s --resume", rot.Phase, rot.Kind.Name(), err, rot.Kind)
			}
			if err := r.save(func(s *clustersecrets.Secrets) error { return s.RecordApplied(r.a.now()) }); err != nil {
				return err
			}
			r.say("%s: every node applied the %s phase", rot.Kind, rot.Phase)
			if pausesAfter(rot.Kind, rot.Phase) {
				return r.paused(ctx, rot)
			}
		}
		var err error
		switch rot.Phase {
		case clustersecrets.PhaseAccept:
			err = r.save(func(s *clustersecrets.Secrets) error { return s.SwitchRotation() })
		case clustersecrets.PhaseSwitch:
			err = r.save(func(s *clustersecrets.Secrets) error { return s.RefreshRotation() })
		case clustersecrets.PhaseRefresh:
			return r.paused(ctx, rot)
		case clustersecrets.PhaseFinish:
			if err := r.save(func(s *clustersecrets.Secrets) error { return s.EndRotation() }); err != nil {
				return err
			}
			r.say("the rotation of the %s is finished: every node trusts the new value alone; %s", rot.Kind.Name(), r.file.written())
			// What a client trusts is in its file, which the finish cannot reach.
			switch rot.Kind {
			case clustersecrets.RotateOSCA:
				r.say("Client files issued during the rotation still trust the old OS CA besides the new one when they verify the nodes, so whoever holds the old OS CA's key could pose as a node to them; issue them again with chalkctl config new.")
			case clustersecrets.RotateKubernetesCA:
				r.say("Kubeconfigs issued during the rotation still trust the old Kubernetes CA besides the new one when they verify the API server, so whoever holds the old CA's key could pose as the API server to them; issue them again with chalkctl kubeconfig.")
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// apply delivers a phase to the nodes and waits until each confirms it.
func (r *rotation) apply(ctx context.Context, kind clustersecrets.RotationKind, phase clustersecrets.Phase) error {
	if kind == clustersecrets.RotateOSCA {
		return r.applyOSCA(ctx, phase)
	}
	return r.applyKubernetes(ctx, kind, phase)
}

// nodesOf lists the cluster's nodes of a Kubernetes kind in name order, but the revoked ones:
// control planes, workers, or with kind "" those without Kubernetes.
func (r *rotation) nodesOf(kind string) []string {
	// rotate read every node's kind before it started, so no error is left to report.
	names, _ := r.cluster.nodesOf(kind, "")
	return slices.DeleteFunc(names, func(name string) bool { return r.revoked[name] })
}

// skipRevoked reads the revoked nodes from the first control plane that answers with them and
// leaves those of the cluster out of the rotation, naming each as it first skips it. Without the
// list no phase runs, so none hands a certificate to a revoked node. A node stays skipped for the
// rest of the run once it is: it missed the phases before.
func (r *rotation) skipRevoked(ctx context.Context) error {
	if r.ignoreRevoked {
		return nil
	}
	nodes := r.allNodes()
	list, err := revokedNodesOf(ctx, r.nodesOf(kubernetes.KindControlPlane), r.status)
	if err != nil {
		return fmt.Errorf("%w; rotate runs only once one does, so it never hands a certificate to a revoked node, or with --ignore-revoked", err)
	}
	if r.revoked == nil {
		r.revoked = map[string]bool{}
	}
	for _, name := range slices.Sorted(maps.Keys(list)) {
		if slices.Contains(nodes, name) && !r.revoked[name] {
			r.revoked[name] = true
			r.say("skipping %s: revoked %s", name, revokedSince(list[name]))
		}
	}
	return nil
}

// allNodes lists every node, control planes first.
func (r *rotation) allNodes() []string {
	return slices.Concat(r.nodesOf(kubernetes.KindControlPlane), r.nodesOf(kubernetes.KindWorker), r.nodesOf(""))
}

// nodes reaches the nodes with the secrets as they are now: chalkctl's admin certificate comes
// from the OS CA that issues, and it trusts every OS CA accepted.
func (r *rotation) nodes() (nodeSet, error) {
	creds, err := secretCredentials(r.file.secrets)
	if err != nil {
		return nodeSet{}, err
	}
	return nodeSet{cluster: r.cluster, creds: creds, addresses: r.addresses}, nil
}

// target addresses a node with the secrets as they are now.
func (r *rotation) target(name string) (*target, error) {
	nodes, err := r.nodes()
	if err != nil {
		return nil, err
	}
	return nodes.target(name)
}

// call runs fn against a node.
func (r *rotation) call(name string, fn func(conn *client.Conn) error) error {
	nodes, err := r.nodes()
	if err != nil {
		return err
	}
	return nodes.call(name, fn)
}

// deliver sends a node what req carries.
func (r *rotation) deliver(ctx context.Context, name string, req *nodev1.ApplyConfigRequest) error {
	return r.call(name, func(conn *client.Conn) error {
		resp, err := conn.ApplyConfig(ctx, connect.NewRequest(req))
		if err != nil {
			return err
		}
		r.a.warnDelivery(name, resp.Msg)
		return nil
	})
}

// deliverShare sends a node whose image has Kubernetes its share as the secrets file has it now.
func (r *rotation) deliverShare(ctx context.Context, name string) error {
	t, err := r.target(name)
	if err != nil {
		return err
	}
	share, err := kubernetesShare(t, r.a.now())
	if err != nil {
		return err
	}
	return r.deliver(ctx, name, &nodev1.ApplyConfigRequest{KubernetesShare: share})
}

// status reads a node's status.
func (r *rotation) status(ctx context.Context, name string) (*nodev1.StatusResponse, error) {
	nodes, err := r.nodes()
	if err != nil {
		return nil, err
	}
	return nodes.status(ctx, name)
}

// stopWaiting is a check's error that waiting does not fix: waitFor returns it at once.
type stopWaiting struct{ error }

func (s stopWaiting) Unwrap() error { return s.error }

// waitFor checks until check passes, up to the timeout, or until it fails with stopWaiting.
func (r *rotation) waitFor(ctx context.Context, what string, check func() error) error {
	deadline := time.Now().Add(r.timeout)
	for {
		err := check()
		if err == nil {
			return nil
		}
		var stop stopWaiting
		if errors.As(err, &stop) {
			return stop.error
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("waiting for %s: %w", what, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", what, context.Cause(ctx))
		case <-time.After(r.poll):
		}
	}
}

// waitStatus waits until a node's status passes check.
func (r *rotation) waitStatus(ctx context.Context, name, what string, check func(st *nodev1.StatusResponse) error) error {
	return r.waitFor(ctx, name+" "+what, func() error {
		st, err := r.status(ctx, name)
		if err != nil {
			return err
		}
		return check(st)
	})
}

// eachNode runs fn for every node, also after one failed, and names those that failed.
func (r *rotation) eachNode(names []string, fn func(name string) error) error {
	var failed []string
	for _, name := range names {
		if err := fn(name); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

// trusts checks that a node's status reports the entry of name with exactly the fingerprints
// want and, when issuing is given, that value as the one that issues.
func trusts(st *nodev1.StatusResponse, name string, want []string, issuing string) error {
	for _, t := range st.Trust {
		if t.Name != name {
			continue
		}
		if !sameFingerprints(t.Fingerprints, want) {
			return fmt.Errorf("trusts %d %s values, not the %d of the secrets file", len(t.Fingerprints), name, len(want))
		}
		if issuing != "" && t.Issuing != issuing {
			return fmt.Errorf("issues with another %s than the secrets file's", name)
		}
		return nil
	}
	return fmt.Errorf("reports no %s", name)
}

func sameFingerprints(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// certificate returns the status of the node's certificate of the name.
func certificate(st *nodev1.StatusResponse, name string) *nodev1.CertificateStatus {
	for _, c := range st.Certificates {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// issuedBy checks that the node's certificate of the name was issued by the CA of the
// fingerprint.
func issuedBy(st *nodev1.StatusResponse, name, issuer string) error {
	c := certificate(st, name)
	switch {
	case c == nil:
		return fmt.Errorf("reports no %s certificate", name)
	case c.Issuer != issuer:
		return fmt.Errorf("its %s certificate is not issued by the CA that issues now", name)
	}
	return nil
}

// fingerprintOf is the fingerprint of a PEM certificate.
func fingerprintOf(certificate string) (string, error) {
	cert, err := pki.ParseCertificate([]byte(certificate))
	if err != nil {
		return "", err
	}
	return pki.Fingerprint(cert.Raw), nil
}

// applyOSCA runs a phase of the OS CA's rotation: nodes trust both OS CAs, control planes renew
// node certificates with the new node CA, every node gets a certificate of it, and finally nodes
// trust the new OS CA alone.
func (r *rotation) applyOSCA(ctx context.Context, phase clustersecrets.Phase) error {
	s := r.file.secrets
	switch phase {
	case clustersecrets.PhaseAccept, clustersecrets.PhaseFinish:
		bundle := s.OSCABundle()
		want, err := pki.Fingerprints(bundle)
		if err != nil {
			return err
		}
		return r.eachNode(r.allNodes(), func(name string) error {
			if err := r.deliver(ctx, name, &nodev1.ApplyConfigRequest{Trust: []byte(bundle)}); err != nil {
				return err
			}
			if err := r.waitStatus(ctx, name, "to trust the OS CAs", func(st *nodev1.StatusResponse) error {
				return trusts(st, client.NameOSCA, want, "")
			}); err != nil {
				return err
			}
			r.say("  %s trusts the %s of the secrets file", name, count(len(want), "OS CA"))
			return nil
		})
	case clustersecrets.PhaseSwitch:
		nodeCA, err := fingerprintOf(s.NodeCA.Certificate)
		if err != nil {
			return err
		}
		return r.eachNode(r.nodesOf(kubernetes.KindControlPlane), func(name string) error {
			if err := r.deliverShare(ctx, name); err != nil {
				return err
			}
			if err := r.waitStatus(ctx, name, "to hold the new node CA", func(st *nodev1.StatusResponse) error {
				if c := certificate(st, client.NameNodeCA); c == nil || c.Fingerprint != nodeCA {
					return errors.New("holds another node CA")
				}
				return nil
			}); err != nil {
				return err
			}
			r.say("  %s renews node certificates with the new node CA", name)
			return nil
		})
	case clustersecrets.PhaseRefresh:
		nodeCA, err := fingerprintOf(s.NodeCA.Certificate)
		if err != nil {
			return err
		}
		return r.eachNode(r.allNodes(), func(name string) error {
			// A node renewed since, or in an earlier run, keeps its certificate.
			if st, err := r.status(ctx, name); err == nil && issuedBy(st, client.NameNode, nodeCA) == nil {
				r.say("  %s serves a certificate of the new node CA already", name)
				return nil
			}
			names, err := r.cluster.certNames(name)
			if err != nil {
				return err
			}
			cert, err := pki.IssueNode(s.NodeCA, names, r.a.now())
			if err != nil {
				return err
			}
			if err := r.deliver(ctx, name, &nodev1.ApplyConfigRequest{NodeCertificate: []byte(cert.Certificate), NodeKey: []byte(cert.Key)}); err != nil {
				return err
			}
			if err := r.waitStatus(ctx, name, "to serve a certificate of the new node CA", func(st *nodev1.StatusResponse) error {
				return issuedBy(st, client.NameNode, nodeCA)
			}); err != nil {
				return err
			}
			r.say("  %s serves a certificate of the new node CA", name)
			return nil
		})
	}
	return nil
}

// pausesAfter reports whether the rotation stops after the phase for the operator, besides after
// the refresh: the OS CA and the Kubernetes CAs after their accept phase, so client files,
// kubeconfigs and workloads trust the new CA before it issues the servers' certificates.
func pausesAfter(kind clustersecrets.RotationKind, phase clustersecrets.Phase) bool {
	return phase == clustersecrets.PhaseAccept && (kind == clustersecrets.RotateOSCA || kind == clustersecrets.RotateKubernetesCA)
}

// paused says what the operator does before the rotation continues.
func (r *rotation) paused(ctx context.Context, rot clustersecrets.Rotation) error {
	switch {
	case rot.Kind != clustersecrets.RotateOSCA:
		return r.pausedKubernetes(ctx, rot)
	case rot.Phase == clustersecrets.PhaseAccept:
		r.say("Every node trusts the old and the new OS CA. Issue new client files now with chalkctl config new: they carry both OS CAs and work throughout the rotation and after it. Client files from before the rotation stop verifying the nodes from the switch on and are refused at the finish. Then continue with chalkctl rotate %s --resume, which makes the new OS CA issue.", rot.Kind)
	default:
		r.say("Every node serves a certificate of the new node CA and trusts the old and the new OS CA. Client files from before the rotation cannot verify the nodes any more and are refused after the finish: issue new ones with chalkctl config new. Build images and installer media again from %s; older ones trust the old OS CA alone, and installing from them fails. Then remove the old OS CA with chalkctl rotate %s --finish.", r.publicFile(), rot.Kind)
	}
	return nil
}

// publicFile names the public half of the secrets file.
func (r *rotation) publicFile() string {
	if r.file.publicOut != "" {
		return r.file.publicOut
	}
	return "secrets.pub.json"
}
