package kubernetes

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/internal/strs"
	"github.com/trevex/chalkos/pkg/kubernetes/clusterregistry"
	"github.com/trevex/chalkos/pkg/kubernetes/translate"
	"github.com/trevex/chalkos/pkg/pki"
)

func registryCommand(h layer.Host) *cobra.Command {
	g := cli.Group(&cobra.Command{Use: "registry", Short: "Manage the images of the cluster registry", Long: registryLong, Example: registryExample})
	g.AddCommand(registryPushCommand(h), registryDeleteCommand(h), registryGCCommand(h), registryListCommand(h))
	return g
}

func registryPushCommand(h layer.Host) *cobra.Command {
	var f endpointFlags
	cmd := h.Command(&cobra.Command{
		Use:     "push SOURCE REFERENCE",
		Short:   "Push an image to every endpoint of the cluster registry",
		Long:    registryPushLong,
		Example: registryPushExample,
		Args:    cli.ExactArgs(2),
	}, func(h layer.Host, ctx context.Context, args []string) error {
		ref, err := parseImageRef(args[1])
		if err != nil {
			return err
		}
		s, err := f.endpoints(h, ctx, "registry push", pki.RoleAdmin)
		if err != nil {
			return err
		}
		if _, err := s.repoAt(s.Endpoints[0], ref); err != nil {
			return err
		}
		img, cleanup, err := openSource(ctx, args[0])
		if err != nil {
			return err
		}
		defer cleanup()
		return s.push(ctx, h.Stdout(), h.Stderr(), img, ref)
	})
	f.register(cmd.Flags(), pki.RoleAdmin)
	return cmd
}

// imageRef is a REFERENCE of the registry commands: a repository with a tag, a digest or both,
// as the kinds write images. A reference with neither names the tag latest.
type imageRef struct {
	repo name.Repository
	// tag and digest are empty when the reference names none.
	tag, digest string
}

func parseImageRef(s string) (imageRef, error) {
	base, digest, pinned := strings.Cut(s, "@")
	var ref imageRef
	if pinned {
		d, err := name.NewDigest(s)
		if err != nil {
			return imageRef{}, cli.Usagef("%s is no image reference: %v", s, err)
		}
		ref.repo, ref.digest = d.Context(), digest
		// go-containerregistry drops the tag of repository:tag@digest.
		if last := base[strings.LastIndex(base, "/")+1:]; !strings.Contains(last, ":") {
			return ref, nil
		}
	}
	t, err := name.NewTag(base)
	if err != nil {
		return imageRef{}, cli.Usagef("%s is no image reference: %v", s, err)
	}
	ref.repo, ref.tag = t.Context(), t.TagStr()
	return ref, nil
}

// name is the repository as containerd names it, docker.io/library/nginx for nginx.
func (r imageRef) name() string {
	host, err := clusterregistry.Prefix(r.repo.RegistryStr())
	if err != nil {
		host = r.repo.RegistryStr()
	}
	return host + "/" + r.repo.RepositoryStr()
}

// String is the reference as containerd names it, docker.io/library/nginx:1.27 for nginx:1.27.
func (r imageRef) String() string {
	s := r.name()
	if r.tag != "" {
		s += ":" + r.tag
	}
	if r.digest != "" {
		s += "@" + r.digest
	}
	return s
}

// endpointFlags are the flags of the registry commands, which reach every endpoint.
type endpointFlags struct {
	node layer.NodeFlags
	// connect are the values of --connect-to, ENDPOINT=HOST:PORT.
	connect []string
}

func (f *endpointFlags) register(fs *pflag.FlagSet, need pki.Role) {
	f.node.ClusterFlags.Register(fs)
	f.node.Secrets.Register(fs)
	file := "client file"
	if need == pki.RoleAdmin {
		file = "admin client file"
	}
	fs.StringVar(&f.node.Config, "config", "", file+" to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)")
	fs.StringArrayVar(&f.connect, "connect-to", nil, "ENDPOINT=HOST:PORT: reach an endpoint at another address, such as a forwarded port, while still verifying its certificate for the endpoint; may be repeated, once per endpoint")
}

// endpoints is the cluster's ClusterRegistry with the flags' credentials, refused for command
// unless they grant need.
func (f endpointFlags) endpoints(h layer.Host, ctx context.Context, command string, need pki.Role) (endpoints, error) {
	c, err := h.Cluster(ctx, f.node.ClusterFlags)
	if err != nil {
		return endpoints{}, err
	}
	if err := c.RequireNodes(command); err != nil {
		return endpoints{}, err
	}
	r, err := clusterRegistry(c)
	if err != nil {
		return endpoints{}, err
	}
	s := endpoints{Registry: r, connect: map[netip.Addr]string{}}
	for _, v := range f.connect {
		e, via, _ := strings.Cut(v, "=")
		a, err := netip.ParseAddr(e)
		if err == nil {
			_, _, err = net.SplitHostPort(via)
		}
		if err != nil {
			return endpoints{}, cli.Usagef("--connect-to %q: want ENDPOINT=HOST:PORT, such as fd00::11=127.0.0.1:15000", v)
		}
		a = a.Unmap()
		if !slices.Contains(r.Endpoints, a) {
			return endpoints{}, cli.Usagef("--connect-to names %s, which is no endpoint of the ClusterRegistry; its endpoints are %s", e, strs.And(endpointNames(r.Endpoints)))
		}
		if other, ok := s.connect[a]; ok {
			return endpoints{}, cli.Usagef("--connect-to names %s twice, at %s and %s; give each endpoint one address", a, other, via)
		}
		s.connect[a] = via
	}
	config, role, err := h.ClientTLS(ctx, f.node)
	if err != nil {
		return endpoints{}, err
	}
	if !role.Allows(need) {
		label := string(role)
		if label == "" {
			label = "client file's"
		}
		return endpoints{}, fmt.Errorf("%s needs the %s role; the %s role may not", command, need, label)
	}
	s.tls = config
	return s, nil
}

func endpointNames(es []netip.Addr) []string {
	names := make([]string, len(es))
	for i, e := range es {
		names[i] = e.String()
	}
	return names
}

// clusterRegistry is the ClusterRegistry the inventory's Kubernetes nodes hold, which the
// inventory check holds to be one alike on all of them.
func clusterRegistry(c layer.Cluster) (clusterregistry.Registry, error) {
	for _, n := range c.NodeNames() {
		docs, err := c.Documents(n)
		if err != nil {
			return clusterregistry.Registry{}, err
		}
		if r, ok, err := translate.ClusterRegistry(docs); ok || err != nil {
			return r, err
		}
	}
	return clusterregistry.Registry{}, fmt.Errorf("no node of the cluster %s holds a ClusterRegistry", c.Name())
}

// endpoints reaches every endpoint of a ClusterRegistry with one set of credentials.
type endpoints struct {
	clusterregistry.Registry
	tls *tls.Config
	// connect is the host:port chalkctl connects to in place of an endpoint.
	connect map[netip.Addr]string
	// pageSize is the size of the pages of catalogs and tag lists; 0 for go-containerregistry's.
	pageSize int
}

// transport reaches the endpoint e with the credentials, at the address --connect-to gives for
// it, and verifies its certificate for e either way.
func (s endpoints) transport(e netip.Addr) http.RoundTripper {
	t := remote.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = s.tls.Clone()
	host := s.Host(e)
	if via, ok := s.connect[e]; ok {
		// Straight to via, past any proxy.
		t.Proxy = nil
		var d net.Dialer
		t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == host {
				addr = via
			}
			return d.DialContext(ctx, network, addr)
		}
	}
	// Its host is an address and a port, which go-containerregistry always takes.
	reg, _ := name.NewRegistry(host)
	return httpsTo{host: host, scheme: reg.Scheme(), next: t}
}

// options are the remote options that reach the endpoint e.
func (s endpoints) options(ctx context.Context, e netip.Addr) []remote.Option {
	opts := []remote.Option{remote.WithContext(ctx), remote.WithTransport(s.transport(e))}
	if s.pageSize > 0 {
		opts = append(opts, remote.WithPageSize(s.pageSize))
	}
	return opts
}

// httpsTo sends every request to host over HTTPS, which the registry alone speaks, though
// go-containerregistry tries plain HTTP besides for loopback and RFC 1918 hosts. scheme is the one
// name.Registry gives host, which go-containerregistry checks the links to a list's further pages
// against, so the request of an answer from host carries it.
type httpsTo struct {
	host, scheme string
	next         http.RoundTripper
}

func (t httpsTo) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != t.host {
		return t.next.RoundTrip(r)
	}
	https := r.Clone(r.Context())
	https.URL.Scheme = "https"
	resp, err := t.next.RoundTrip(https)
	if resp != nil {
		asked := *resp.Request
		u := *asked.URL
		u.Scheme = t.scheme
		asked.URL = &u
		resp.Request = &asked
	}
	return resp, err
}

// repoAt is the repository of ref below the endpoint e: <endpoint>/<prefix of ref's
// host>/<repository>. A host the ClusterRegistry does not mirror is refused.
func (s endpoints) repoAt(e netip.Addr, ref imageRef) (name.Repository, error) {
	prefix, err := clusterregistry.Prefix(ref.repo.RegistryStr())
	if err != nil {
		return name.Repository{}, err
	}
	if !slices.Contains(s.Mirrors, prefix) {
		return name.Repository{}, fmt.Errorf("the ClusterRegistry mirrors %s, not %s, so containerd never pulls %s from it; add %s to its mirrors", strs.And(s.Mirrors), prefix, ref, prefix)
	}
	return name.NewRepository(s.Host(e) + "/" + prefix + "/" + ref.repo.RepositoryStr())
}

// targetOf is ref below the endpoint e, by its tag when it names one, else by its digest.
func (s endpoints) targetOf(e netip.Addr, ref imageRef) (name.Reference, error) {
	repo, err := s.repoAt(e, ref)
	if err != nil {
		return nil, err
	}
	if ref.tag != "" {
		return repo.Tag(ref.tag), nil
	}
	return repo.Digest(ref.digest), nil
}

// pushable is an image or an index of images, as a source gives it.
type pushable interface {
	Digest() (v1.Hash, error)
}

// push pushes img to each endpoint in turn, under ref's tag, else by its digest, and fails
// unless every endpoint serves img's digest there. A digest ref names must be img's. It reports
// each endpoint, a failed one to errOut.
func (s endpoints) push(ctx context.Context, out, errOut io.Writer, img pushable, ref imageRef) error {
	want, err := img.Digest()
	if err != nil {
		return err
	}
	if ref.digest != "" && ref.digest != want.String() {
		return fmt.Errorf("%s names the digest %s, but the source's image is %s; push a source of that digest, such as the image in its registry by that reference, since docker save and rebuilt images get other digests", ref, ref.digest, want)
	}
	fmt.Fprintf(out, "pushing %s as %s@%s\n", ref, ref.name(), want)
	var failed []string
	for _, e := range s.Endpoints {
		target, err := s.targetOf(e, ref)
		if err == nil {
			opts := s.options(ctx, e)
			if err = write(target, img, opts...); err == nil {
				err = served(target, want, opts...)
			}
		}
		if err != nil {
			fmt.Fprintf(errOut, "%s: failed: %v\n", e, err)
			failed = append(failed, e.String())
			continue
		}
		fmt.Fprintf(out, "%s: pushed %s as %s\n", e, target, want)
	}
	if len(failed) > 0 {
		return fmt.Errorf("the push failed on %d of %d endpoints of the cluster registry: %s; nothing copies images between them, so push again once they are reachable", len(failed), len(s.Endpoints), strings.Join(failed, ", "))
	}
	return nil
}

// write pushes img to target; an index after the images it names, which the registry wants
// first.
func write(target name.Reference, img pushable, opts ...remote.Option) error {
	if idx, ok := img.(v1.ImageIndex); ok {
		return remote.WriteIndex(target, idx, opts...)
	}
	return remote.Write(target, img.(v1.Image), opts...)
}

// served fails unless target's manifest has the digest want.
func served(target name.Reference, want v1.Hash, opts ...remote.Option) error {
	desc, err := remote.Head(target, opts...)
	if err != nil {
		return err
	}
	if desc.Digest != want {
		return fmt.Errorf("the endpoint serves %s, not %s", desc.Digest, want)
	}
	return nil
}
