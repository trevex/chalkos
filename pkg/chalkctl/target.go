package chalkctl

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/pflag"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
)

// nodeFlags are the flags of a command that reaches a cluster's nodes, with what the command
// needs of the cluster.
type nodeFlags struct {
	layer.NodeFlags
	// needs names a command that needs the nodes' config. Only an inventory gives that config, so
	// without one the command is refused before any credentials are read.
	needs string
}

// target is a node of the cluster with the credentials chalkctl reaches it with. secrets holds
// the secrets file for the commands that need it, and nothing for a client file.
type target struct {
	cluster *cluster
	name    string
	creds   *credentials
	secrets clustersecrets.Secrets
	addr    string
	// source says where addr comes from, for errors reaching it.
	source string
}

// target addresses a node with the secrets file.
func (a *app) target(ctx context.Context, n nodeFlags, name string) (*target, error) {
	s, err := a.reach(ctx, n, name, false)
	if err != nil {
		return nil, err
	}
	return s.target(name)
}

// clientTarget addresses a node with the secrets file or a client file.
func (a *app) clientTarget(ctx context.Context, n nodeFlags, name string) (*target, error) {
	s, err := a.reach(ctx, n, name, true)
	if err != nil {
		return nil, err
	}
	return s.target(name)
}

// reach reaches the nodes of the cluster the flags name with the secrets file or, with
// clientFile, with the secrets file or a client file. A bare --address ADDR reaches one, the
// node the command names; one is empty for a command that reaches several nodes.
func (a *app) reach(ctx context.Context, n nodeFlags, one string, clientFile bool) (nodeSet, error) {
	c, err := n.loadCluster(ctx)
	if err != nil {
		return nodeSet{}, err
	}
	addresses, err := n.addresses(c, one)
	if err != nil {
		return nodeSet{}, err
	}
	var creds *credentials
	if clientFile {
		creds, err = a.loadCredentials(ctx, n.Secrets, n.Config)
	} else {
		creds, err = a.loadSecretCredentials(ctx, n.Secrets)
	}
	if err != nil {
		return nodeSet{}, err
	}
	return nodeSet{cluster: c, creds: creds, addresses: addresses}, nil
}

// addresses are the addresses --address gives, by node, as Addresses.Resolve gives them. It
// refuses a NODE=ADDR whose node the cluster does not have.
func (n nodeFlags) addresses(c *cluster, one string) (map[string]string, error) {
	addresses, err := n.Address.Resolve(one)
	if err != nil {
		return nil, err
	}
	for name := range addresses {
		if err := c.node(name); err != nil {
			return nil, err
		}
	}
	return addresses, nil
}

// loadCluster loads the cluster the command's flags name, and refuses a partial one when the
// command needs the nodes' config.
func (n nodeFlags) loadCluster(ctx context.Context) (*cluster, error) {
	c, err := loadCluster(ctx, n.ClusterFlags)
	if err != nil {
		return nil, err
	}
	if n.needs != "" {
		if err := c.RequireNodes(n.needs); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// dialInstalled connects to an installed node, verifying it by the OS CA and its name.
func dialInstalled(t *target) (*client.Conn, error) {
	return dialNode(t, false)
}

// dialNode connects to an installed node and verifies it by the OS CA and its name.
// ignoreValidity also accepts an expired node certificate; only node renew passes it.
// With a client file, a failed handshake names the file: a client file names no cluster, and one
// of another cluster carries another OS CA. The secrets file keeps the handshake's own error.
func dialNode(t *target, ignoreValidity bool) (*client.Conn, error) {
	roots, err := t.creds.roots()
	if err != nil {
		return nil, err
	}
	verify := client.ByCA(roots, t.name)
	if ignoreValidity {
		verify = client.ByCAIgnoringValidity(roots, t.name)
	}
	o := client.Options{Verify: verify, Certificate: t.creds.cert, Source: t.source}
	if path := t.creds.configPath; t.creds.config != nil {
		o.Untrusted = func(err error, refused bool) error {
			if refused {
				return fmt.Errorf("the node does not accept this client file, %s; is it another cluster's? (%w)", path, err)
			}
			return fmt.Errorf("this client file's OS CA does not verify the node's certificate, %s: is the file another cluster's or from before an OS CA rotation, or does the address reach another cluster's node? (%w)", path, err)
		}
	}
	return client.Dial(t.addr, o)
}

// pinFlags verifies a node in maintenance mode, which serves a self-signed certificate.
type pinFlags struct {
	fingerprint string
	insecure    bool
}

func (p *pinFlags) register(fs *pflag.FlagSet) {
	fs.StringVar(&p.fingerprint, "fingerprint", "", "SHA-256 fingerprint the node prints on its console in maintenance mode")
	fs.BoolVar(&p.insecure, "insecure", false, "accept any certificate and print the node's fingerprint")
}

func (p pinFlags) set() bool { return p.fingerprint != "" || p.insecure }

func (p pinFlags) dial(addr, source string, cert *tls.Certificate) (*client.Conn, error) {
	if p.fingerprint != "" && p.insecure {
		return nil, errors.New("pass either --fingerprint or --insecure")
	}
	if !p.set() {
		return nil, errors.New("a node in maintenance mode is verified by its certificate's fingerprint; pass --fingerprint FP, or --insecure to accept any certificate")
	}
	verify := client.ByFingerprint(p.fingerprint)
	if p.insecure {
		verify = client.AnyCertificate()
	}
	return client.Dial(addr, client.Options{Verify: verify, Certificate: cert, Source: source})
}

// nodePinFlags are the flags of a command that reaches an installed node or, with a fingerprint
// or --insecure, a node in maintenance mode.
type nodePinFlags struct {
	node nodeFlags
	pin  pinFlags
}

// dialEither connects to a node in maintenance mode when a fingerprint or --insecure is given,
// and to an installed node otherwise.
func (a *app) dialEither(t *target, p pinFlags) (*client.Conn, error) {
	if p.set() {
		return p.dial(t.addr, t.source, t.creds.cert)
	}
	return dialInstalled(t)
}

// setAddressUsage gives --address, which nodeFlags registered, the usage of a command that
// reaches other nodes than the one it names.
func setAddressUsage(fs *pflag.FlagSet, usage string) {
	fs.Lookup("address").Usage = usage
}

// nodeSet reaches the nodes of a cluster with one set of credentials: each at the address
// --address gives for it, or else at the inventory's.
type nodeSet struct {
	cluster   *cluster
	creds     *credentials
	addresses map[string]string
}

// target addresses a node of the set, at the address --address gives for it or else at the
// inventory's.
func (s nodeSet) target(name string) (*target, error) {
	if err := s.cluster.node(name); err != nil {
		return nil, err
	}
	addr, source, err := s.cluster.address(s.addresses[name], name)
	if err != nil {
		return nil, err
	}
	t := &target{cluster: s.cluster, name: name, creds: s.creds, addr: addr, source: source}
	if s.creds.secrets != nil {
		t.secrets = *s.creds.secrets
	}
	return t, nil
}

// Call runs fn on a connection to an installed node of the set.
func (s nodeSet) Call(name string, fn func(conn *client.Conn) error) error {
	t, err := s.target(name)
	if err != nil {
		return err
	}
	return callNode(t, fn)
}

// callNode runs fn on a connection to an installed node and closes the connection. Its error,
// the connection's or fn's, names the node, as layer.NodeError does.
func callNode(t *target, fn func(conn *client.Conn) error) error {
	conn, err := dialInstalled(t)
	if err != nil {
		return layer.Named(t.name, err)
	}
	defer conn.Close()
	return layer.Named(t.name, fn(conn))
}

// status reads an installed node's status.
func (s nodeSet) status(ctx context.Context, name string) (*nodev1.StatusResponse, error) {
	t, err := s.target(name)
	if err != nil {
		return nil, err
	}
	return nodeStatus(ctx, t)
}

// nodeStatus reads an installed node's status.
func nodeStatus(ctx context.Context, t *target) (*nodev1.StatusResponse, error) {
	var st *nodev1.StatusResponse
	err := callNode(t, func(conn *client.Conn) error {
		resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
		if err == nil {
			st = resp.Msg
		}
		return err
	})
	return st, err
}

// ViaFirst calls fn on the first of the nodes named that answers: it moves on past those whose
// error next accepts. The error names the node that gave it.
func (s nodeSet) ViaFirst(names []string, next func(error) bool, fn func(conn *client.Conn) error) error {
	if len(names) == 0 {
		return errors.New("no node to ask")
	}
	var last error
	for _, name := range names {
		err := s.Call(name, fn)
		if err == nil {
			return nil
		}
		last = layer.Named(name, err)
		if !next(err) {
			return last
		}
	}
	return fmt.Errorf("none of %s answered: %w", strings.Join(names, ", "), last)
}

// soleNode is the node of names that a bare --address ADDR reaches: the one node named, or none
// of several.
func soleNode(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return ""
}
