package chalkctl

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/pflag"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
)

// nodeFlags holds what commands addressing one node share.
type nodeFlags struct {
	cluster clusterFlags
	secrets secretFlags
	address string
	// config is the client file of commands that a client file may run.
	config string
	// needs names a command that needs the nodes' config. Only an inventory gives that config, so
	// without one the command is refused before any credentials are read.
	needs string
}

func (n *nodeFlags) register(fs *pflag.FlagSet) {
	n.cluster.register(fs)
	n.secrets.register(fs)
	fs.StringVar(&n.address, "address", "", "address of the node's chalkd, host or host:port (default the node's address in the inventory)")
}

// registerClient registers the flags of a command that a client file may run.
func (n *nodeFlags) registerClient(fs *pflag.FlagSet) {
	n.register(fs)
	registerConfig(fs, &n.config)
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
	c, err := n.loadCluster(ctx)
	if err != nil {
		return nil, err
	}
	creds, err := a.loadSecretCredentials(ctx, n.secrets)
	if err != nil {
		return nil, err
	}
	return targetIn(c, n.address, name, creds)
}

// clientTarget addresses a node with the secrets file or a client file.
func (a *app) clientTarget(ctx context.Context, n nodeFlags, name string) (*target, error) {
	c, err := n.loadCluster(ctx)
	if err != nil {
		return nil, err
	}
	creds, err := a.loadCredentials(ctx, n.secrets, n.config)
	if err != nil {
		return nil, err
	}
	return targetIn(c, n.address, name, creds)
}

// loadCluster loads the cluster the command's flags name, and refuses a partial one when the
// command needs the nodes' config.
func (n nodeFlags) loadCluster(ctx context.Context) (*cluster, error) {
	c, err := loadCluster(ctx, n.cluster)
	if err != nil {
		return nil, err
	}
	if n.needs != "" {
		if err := c.requireNodes(n.needs); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// targetIn addresses a node of the cluster at address, or without one at the inventory's.
func targetIn(c *cluster, address, name string, creds *credentials) (*target, error) {
	if err := c.node(name); err != nil {
		return nil, err
	}
	addr, source, err := c.address(address, name)
	if err != nil {
		return nil, err
	}
	t := &target{cluster: c, name: name, creds: creds, addr: addr, source: source}
	if creds.secrets != nil {
		t.secrets = *creds.secrets
	}
	return t, nil
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

// nodeError puts the node's name where the node's own messages say <node>. The error it wraps
// stays reachable, so its connect code still decides whether another node is tried.
func nodeError(err error, name string) error {
	if err == nil {
		return nil
	}
	return namedError{err, name}
}

// namedError is a node's error with the node's name in place of <node>.
type namedError struct {
	err  error
	name string
}

func (e namedError) Error() string { return strings.ReplaceAll(e.err.Error(), "<node>", e.name) }

func (e namedError) Unwrap() error { return e.err }

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

// addressList is --address NODE=ADDR, which may be given for each node.
type addressList map[string]string

func (l addressList) String() string {
	var s []string
	for _, name := range slices.Sorted(maps.Keys(l)) {
		s = append(s, name+"="+l[name])
	}
	return strings.Join(s, ",")
}

// Type names the value for pflag; help names it NODE=ADDR, from the flag's usage.
func (l addressList) Type() string { return "nodeAddresses" }

func (l addressList) Set(v string) error {
	name, addr, ok := strings.Cut(v, "=")
	if !ok || name == "" || addr == "" {
		return errors.New("want NODE=ADDR")
	}
	l[name] = addr
	return nil
}

// registerAddresses registers --address NODE=ADDR of a command that reaches several nodes.
func registerAddresses(fs *pflag.FlagSet, l addressList) {
	fs.Var(l, "address", "address of a node's chalkd, `NODE=ADDR`, host or host:port; may be repeated (default each node's address in the inventory)")
}

// checkAddresses refuses an --address NODE=ADDR whose node the cluster does not have.
func (c *cluster) checkAddresses(l addressList) error {
	for name := range l {
		if err := c.node(name); err != nil {
			return err
		}
	}
	return nil
}

// registerConfig registers --config, the client file of a command that a client file may run.
func registerConfig(fs *pflag.FlagSet, p *string) {
	fs.StringVar(p, "config", "", "client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)")
}

// nodeSet reaches the nodes of a cluster with one set of credentials: each at the address
// --address gives for it, or else at the inventory's.
type nodeSet struct {
	cluster   *cluster
	creds     *credentials
	addresses map[string]string
}

// target addresses a node of the set.
func (s nodeSet) target(name string) (*target, error) {
	return targetIn(s.cluster, s.addresses[name], name, s.creds)
}

// call runs fn on a connection to an installed node of the set.
func (s nodeSet) call(name string, fn func(conn *client.Conn) error) error {
	t, err := s.target(name)
	if err != nil {
		return err
	}
	return callNode(t, fn)
}

// callNode runs fn on a connection to an installed node and closes the connection. fn's error
// carries the node's name in place of <node>.
func callNode(t *target, fn func(conn *client.Conn) error) error {
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	defer conn.Close()
	return nodeError(fn(conn), t.name)
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

// viaControlPlanes calls fn on the first of the control planes named that answers: it moves on
// past those whose error next accepts. The error names the control plane that gave it.
func (s nodeSet) viaControlPlanes(names []string, next func(error) bool, fn func(conn *client.Conn) error) error {
	if len(names) == 0 {
		return errors.New("the cluster has no control-plane node to ask")
	}
	var last error
	for _, name := range names {
		err := s.call(name, fn)
		if err == nil {
			return nil
		}
		last = fmt.Errorf("%s: %w", name, err)
		if !next(err) {
			return last
		}
	}
	return fmt.Errorf("no control-plane node answered: %w", last)
}
