package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
)

// kubernetesShare issues the share of a node whose role has Kubernetes; nil for one without.
func kubernetesShare(t *target, now time.Time) ([]byte, error) {
	role, ok := t.cluster.manifest.Roles[t.node.Role]
	if !ok {
		return nil, fmt.Errorf("the cluster has no role %s", t.node.Role)
	}
	if role.Kind == "" {
		return nil, nil
	}
	share, err := kpki.ShareFor(&t.secrets, role.Kind, t.name, now)
	if err != nil {
		return nil, err
	}
	return share.Encode()
}

func (a *app) bootstrap(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	var n nodeCommand
	n.registerClient(fs)
	timeout := fs.Duration("timeout", 20*time.Minute, "how long to wait for the control plane to apply its manifests")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: chalkctl bootstrap <node>")
	}
	t, err := a.clientTarget(ctx, n, pos[0])
	if err != nil {
		return err
	}
	// Without the cluster definition the node itself refuses when it is a worker.
	if kind := t.cluster.manifest.Roles[t.node.Role].Kind; !t.cluster.partial && kind != manifest.KindControlPlane {
		return fmt.Errorf("%s is not a control-plane node; bootstrap one of the cluster's control-plane nodes", t.name)
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	fmt.Fprintf(a.stdout, "bootstrapping %s; the control plane pulls its images and starts\n", t.name)
	resp, err := conn.Bootstrap(ctx, connect.NewRequest(&nodev1.BootstrapRequest{}))
	if err != nil {
		return fmt.Errorf("bootstrap %s: %w", t.name, err)
	}
	fmt.Fprintf(a.stdout, "%s is bootstrapped; applied %d objects\n", t.name, resp.Msg.Applied)
	return nil
}

func (a *app) kubeconfig(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("kubeconfig", flag.ContinueOnError)
	var cf clusterFlags
	var sf secretFlags
	cf.register(fs)
	sf.register(fs)
	ttl := fs.Duration("ttl", 8760*time.Hour, "validity of the admin certificate")
	out := fs.String("out", "kubeconfig", "file to write, - for standard output")
	force := fs.Bool("force", false, "replace an existing file")
	server := fs.String("server", "", "URL clients reach the API server at, when it differs from the cluster endpoint; the certificate is still verified for the endpoint")
	name := fs.String("name", "admin", "user name in the admin certificate")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return errors.New("usage: chalkctl kubeconfig [--ttl 8760h] [--out FILE] [--server URL]")
	}
	c, err := a.loadCluster(ctx, cf)
	if err != nil {
		return err
	}
	secrets, err := a.loadSecrets(ctx, sf, cf.flake)
	if err != nil {
		return err
	}
	k := secrets.Kubernetes
	admin, err := kpki.IssueAdmin(k.CA, *name, *ttl, time.Now())
	if err != nil {
		return err
	}
	kc := kpki.Kubeconfig{Name: c.manifest.Cluster.Name, Server: c.manifest.Cluster.Endpoint, CA: []byte(k.CABundle()), Client: admin}
	if *server != "" {
		endpoint, err := url.Parse(c.manifest.Cluster.Endpoint)
		if err != nil {
			return fmt.Errorf("the cluster endpoint: %w", err)
		}
		kc.Server, kc.ServerName = *server, endpoint.Hostname()
	}
	data, err := kc.Encode()
	if err != nil {
		return err
	}
	if *out == "-" {
		_, err := a.stdout.Write(data)
		return err
	}
	if *force {
		if err := os.Remove(*out); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// The file holds the admin's private key.
	if err := writeNew(*out, data, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "wrote %s; its certificate is valid for %s\n", *out, *ttl)
	return nil
}
