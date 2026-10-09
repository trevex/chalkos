package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

// rotateHelp explains chalkctl rotate.
const rotateHelp = `usage: chalkctl rotate os-ca|kubernetes-ca|service-account-key|encryption-key [--resume | --finish [--force]] [flags]

Rotates a CA or key of the cluster from the secrets file, in phases every node confirms in its
status before the next one starts: accept (every node trusts the new value besides the old one),
switch (the new value issues or signs), refresh (what the old value issued is issued again) and,
with --finish, finish (the old value is removed, and whatever it issued is refused from then on).
The secrets file records the phase reached and is updated in place, keeping its previous version
as <file>.prev, unless --out names a new file; an encrypted file is encrypted again to the
recipients it records inside, which --recipient replaces. A rotation that stopped, as at an
unreachable node, or paused for the operator, continues with --resume. One rotation runs at a
time.

flags:
`

// endpointList is --endpoint NODE=ADDR, which may be given for each node.
type endpointList map[string]string

func (l endpointList) String() string { return fmt.Sprint(map[string]string(l)) }

func (l endpointList) Set(v string) error {
	name, addr, ok := strings.Cut(v, "=")
	if !ok || name == "" || addr == "" {
		return errors.New("want NODE=ADDR")
	}
	l[name] = addr
	return nil
}

func (a *app) rotate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), rotateHelp)
		fs.PrintDefaults()
	}
	var cf clusterFlags
	var sf secretFlags
	var change changeFlags
	cf.register(fs)
	sf.register(fs)
	change.register(fs)
	endpoints := endpointList{}
	fs.Var(endpoints, "endpoint", "address of a node's chalkd, NODE=ADDR, host or host:port; may be repeated (default each node's first static address)")
	resume := fs.Bool("resume", false, "continue the rotation the secrets file records")
	finish := fs.Bool("finish", false, "remove the old value from every node and the secrets file")
	force := fs.Bool("force", false, "with --finish, finish the service-account key's rotation within the hour after its switch")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long to wait for each node to apply a phase")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *resume && *finish || *force && !*finish {
		return errors.New("usage: chalkctl rotate " + strings.Join(pki.RotationKinds, "|") + " [--resume | --finish [--force]]")
	}
	kind := pos[0]
	if !slices.Contains(pki.RotationKinds, kind) {
		return fmt.Errorf("rotate: unknown kind %q; rotate one of %s", kind, strings.Join(pki.RotationKinds, ", "))
	}
	c, err := a.loadCluster(ctx, cf)
	if err != nil {
		return err
	}
	for name := range endpoints {
		if _, err := c.node(name); err != nil {
			return err
		}
	}
	f, err := a.openSecrets(ctx, sf, cf.flake, change)
	if err != nil {
		return err
	}
	r := &rotation{a: a, cluster: c, file: f, endpoints: endpoints, timeout: *timeout, poll: a.poll()}
	switch {
	case *finish:
		return r.finish(ctx, kind, *force)
	case *resume:
		return r.resume(ctx, kind)
	}
	return r.start(ctx, kind)
}

// poll is the time between two looks at the nodes' status.
func (a *app) poll() time.Duration {
	if a.pollInterval > 0 {
		return a.pollInterval
	}
	return 3 * time.Second
}

// now is chalkctl's clock.
func (a *app) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}

// rotation runs the phases of a rotation on the nodes of the cluster, recording each in the
// secrets file.
type rotation struct {
	a       *app
	cluster *cluster
	file    *secretsFile
	// endpoints are the nodes' chalkd addresses that --endpoint gives.
	endpoints map[string]string
	// timeout bounds each wait for a node; poll is the time between two looks.
	timeout, poll time.Duration
}

// say prints a line of the rotation's progress.
func (r *rotation) say(format string, args ...any) {
	fmt.Fprintf(r.a.stdout, format+"\n", args...)
}

// save changes a copy of the secrets and writes it, so the file and what chalkctl holds never
// differ.
func (r *rotation) save(change func(s *pki.Secrets) error) error {
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

func (r *rotation) start(ctx context.Context, kind string) error {
	if err := r.save(func(s *pki.Secrets) error { return s.BeginRotation(kind, r.a.now()) }); err != nil {
		return err
	}
	r.say("%s records the rotation of the %s; %s", r.file.out, pki.RotationName(kind), r.file.written())
	return r.run(ctx)
}

func (r *rotation) resume(ctx context.Context, kind string) error {
	if err := r.running(kind); err != nil {
		return err
	}
	return r.run(ctx)
}

// running checks that the secrets file records a rotation of kind.
func (r *rotation) running(kind string) error {
	switch rot := r.file.secrets.Rotation; {
	case rot == nil:
		return fmt.Errorf("%s records no rotation; start one with chalkctl rotate %s", r.file.path, kind)
	case rot.Kind != kind:
		return fmt.Errorf("%s records a rotation of the %s, not of the %s", r.file.path, pki.RotationName(rot.Kind), pki.RotationName(kind))
	}
	return nil
}

func (r *rotation) finish(ctx context.Context, kind string, force bool) error {
	if err := r.running(kind); err != nil {
		return err
	}
	rot := r.file.secrets.Rotation
	if rot.Phase != pki.PhaseFinish {
		if rot.Phase != pki.PhaseRefresh || !rot.Applied {
			return fmt.Errorf("the rotation of the %s is in its %s phase; finish it once its refresh phase is applied, after chalkctl rotate %s --resume", pki.RotationName(kind), rot.Phase, kind)
		}
		if err := r.finishGuard(ctx, kind, force); err != nil {
			return err
		}
		if err := r.save(func(s *pki.Secrets) error { return s.FinishRotation() }); err != nil {
			return err
		}
	}
	return r.run(ctx)
}

// finishGuard refuses to remove an old value something still needs.
func (r *rotation) finishGuard(ctx context.Context, kind string, force bool) error {
	if kind == pki.RotateOSCA {
		return nil
	}
	return r.finishGuardKubernetes(ctx, kind, force)
}

// run applies the phase the secrets file records and the ones after it, up to the kind's pause
// or the end of the finish.
func (r *rotation) run(ctx context.Context) error {
	for {
		rot := *r.file.secrets.Rotation
		if !rot.Applied {
			r.say("%s: the %s phase", rot.Kind, rot.Phase)
			if err := r.apply(ctx, rot.Kind, rot.Phase); err != nil {
				return fmt.Errorf("the %s phase of the rotation of the %s stopped: %w; once that is fixed, continue with chalkctl rotate %s --resume", rot.Phase, pki.RotationName(rot.Kind), err, rot.Kind)
			}
			if err := r.save(func(s *pki.Secrets) error { return s.RecordApplied(r.a.now()) }); err != nil {
				return err
			}
			r.say("%s: every node applied the %s phase", rot.Kind, rot.Phase)
			if pausesAfter(rot.Kind, rot.Phase) {
				return r.paused(ctx, rot)
			}
		}
		var err error
		switch rot.Phase {
		case pki.PhaseAccept:
			err = r.save(func(s *pki.Secrets) error { return s.SwitchRotation() })
		case pki.PhaseSwitch:
			err = r.save(func(s *pki.Secrets) error { return s.RefreshRotation() })
		case pki.PhaseRefresh:
			return r.paused(ctx, rot)
		case pki.PhaseFinish:
			if err := r.save(func(s *pki.Secrets) error { return s.EndRotation() }); err != nil {
				return err
			}
			r.say("the rotation of the %s is finished: every node trusts the new value alone; %s", pki.RotationName(rot.Kind), r.file.written())
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// apply delivers a phase to the nodes and waits until each confirms it.
func (r *rotation) apply(ctx context.Context, kind, phase string) error {
	if kind == pki.RotateOSCA {
		return r.applyOSCA(ctx, phase)
	}
	return r.applyKubernetes(ctx, kind, phase)
}

// nodesOf lists the cluster's nodes of a Kubernetes kind in name order: control planes,
// workers, or with kind "" those of roles without Kubernetes.
func (r *rotation) nodesOf(kind string) []string {
	m := r.cluster.manifest
	var names []string
	for _, name := range slices.Sorted(maps.Keys(m.Nodes)) {
		if m.Roles[m.Nodes[name].Role].Kind == kind {
			names = append(names, name)
		}
	}
	return names
}

// allNodes lists every node, control planes first.
func (r *rotation) allNodes() []string {
	return slices.Concat(r.nodesOf(manifest.KindControlPlane), r.nodesOf(manifest.KindWorker), r.nodesOf(""))
}

// target addresses a node with the secrets as they are now: chalkctl's admin certificate comes
// from the OS CA that issues, and it trusts every OS CA accepted.
func (r *rotation) target(name string) (*target, error) {
	creds, err := secretCredentials(r.file.secrets)
	if err != nil {
		return nil, err
	}
	return targetIn(r.cluster, nodeCommand{endpoint: r.endpoints[name]}, name, creds)
}

// call runs fn against a node.
func (r *rotation) call(name string, fn func(conn *client.Conn, t *target) error) error {
	t, err := r.target(name)
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	defer conn.Close()
	return nodeError(fn(conn, t), name)
}

// deliver sends a node what req carries.
func (r *rotation) deliver(ctx context.Context, name string, req *nodev1.ApplyIdentityRequest) error {
	return r.call(name, func(conn *client.Conn, _ *target) error {
		_, err := conn.ApplyIdentity(ctx, connect.NewRequest(req))
		return err
	})
}

// deliverShare sends a node of a role with Kubernetes its share as the secrets file has it now.
func (r *rotation) deliverShare(ctx context.Context, name string) error {
	t, err := r.target(name)
	if err != nil {
		return err
	}
	share, err := kubernetesShare(t, r.a.now())
	if err != nil {
		return err
	}
	return r.deliver(ctx, name, &nodev1.ApplyIdentityRequest{KubernetesShare: share})
}

// status reads a node's status.
func (r *rotation) status(ctx context.Context, name string) (*nodev1.StatusResponse, error) {
	var st *nodev1.StatusResponse
	err := r.call(name, func(conn *client.Conn, _ *target) error {
		resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
		if err == nil {
			st = resp.Msg
		}
		return err
	})
	return st, err
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
func (r *rotation) applyOSCA(ctx context.Context, phase string) error {
	s := r.file.secrets
	switch phase {
	case pki.PhaseAccept, pki.PhaseFinish:
		bundle := s.OSCABundle()
		want, err := pki.Fingerprints(bundle)
		if err != nil {
			return err
		}
		return r.eachNode(r.allNodes(), func(name string) error {
			if err := r.deliver(ctx, name, &nodev1.ApplyIdentityRequest{Trust: []byte(bundle)}); err != nil {
				return err
			}
			if err := r.waitStatus(ctx, name, "to trust the OS CAs", func(st *nodev1.StatusResponse) error {
				return trusts(st, "OS CA", want, "")
			}); err != nil {
				return err
			}
			r.say("  %s trusts the %d OS CAs of the secrets file", name, len(want))
			return nil
		})
	case pki.PhaseSwitch:
		nodeCA, err := fingerprintOf(s.NodeCA.Certificate)
		if err != nil {
			return err
		}
		return r.eachNode(r.nodesOf(manifest.KindControlPlane), func(name string) error {
			if err := r.deliverShare(ctx, name); err != nil {
				return err
			}
			if err := r.waitStatus(ctx, name, "to hold the new node CA", func(st *nodev1.StatusResponse) error {
				if c := certificate(st, "node CA"); c == nil || c.Fingerprint != nodeCA {
					return errors.New("holds another node CA")
				}
				return nil
			}); err != nil {
				return err
			}
			r.say("  %s renews node certificates with the new node CA", name)
			return nil
		})
	case pki.PhaseRefresh:
		nodeCA, err := fingerprintOf(s.NodeCA.Certificate)
		if err != nil {
			return err
		}
		return r.eachNode(r.allNodes(), func(name string) error {
			// A node renewed since, or in an earlier run, keeps its certificate.
			if st, err := r.status(ctx, name); err == nil && issuedBy(st, "node", nodeCA) == nil {
				r.say("  %s serves a certificate of the new node CA already", name)
				return nil
			}
			t, err := r.target(name)
			if err != nil {
				return err
			}
			cert, err := pki.IssueNode(s.NodeCA, nodeNames(t), r.a.now())
			if err != nil {
				return err
			}
			if err := r.deliver(ctx, name, &nodev1.ApplyIdentityRequest{NodeCertificate: []byte(cert.Certificate), NodeKey: []byte(cert.Key)}); err != nil {
				return err
			}
			if err := r.waitStatus(ctx, name, "to serve a certificate of the new node CA", func(st *nodev1.StatusResponse) error {
				return issuedBy(st, "node", nodeCA)
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
func pausesAfter(kind, phase string) bool {
	return phase == pki.PhaseAccept && (kind == pki.RotateOSCA || kind == pki.RotateKubernetesCA)
}

// paused says what the operator does before the rotation continues.
func (r *rotation) paused(ctx context.Context, rot pki.Rotation) error {
	switch {
	case rot.Kind != pki.RotateOSCA:
		return r.pausedKubernetes(ctx, rot)
	case rot.Phase == pki.PhaseAccept:
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
