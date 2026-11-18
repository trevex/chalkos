package kubernetes

import (
	"archive/tar"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
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
	g.AddCommand(registryPushCommand(h))
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
		ref, err := parseReference(args[1])
		if err != nil {
			return err
		}
		s, err := f.endpoints(h, ctx, "registry push", pki.RoleAdmin)
		if err != nil {
			return err
		}
		if _, err := s.targetOf(s.Endpoints[0], ref); err != nil {
			return err
		}
		img, cleanup, err := openSource(ctx, args[0])
		if err != nil {
			return err
		}
		defer cleanup()
		return s.push(ctx, h.Stdout(), img, ref)
	})
	f.register(cmd.Flags(), pki.RoleAdmin)
	return cmd
}

func parseReference(s string) (name.Reference, error) {
	ref, err := name.ParseReference(s)
	if err != nil {
		return nil, cli.Usagef("%s is no image reference: %v", s, err)
	}
	return ref, nil
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
	fs.StringArrayVar(&f.connect, "connect-to", nil, "ENDPOINT=HOST:PORT: reach an endpoint at another address, such as a forwarded port, while still verifying its certificate for the endpoint; may be repeated")
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
		if a = a.Unmap(); !slices.Contains(r.Endpoints, a) {
			return endpoints{}, cli.Usagef("--connect-to names %s, which is no endpoint of the ClusterRegistry; its endpoints are %s", e, strs.And(endpointNames(r.Endpoints)))
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

// options are the remote options that reach the endpoint e.
func (s endpoints) options(ctx context.Context, e netip.Addr) []remote.Option {
	t := remote.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = s.tls.Clone()
	host := s.Host(e)
	if via, ok := s.connect[e]; ok {
		var d net.Dialer
		t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == host {
				addr = via
			}
			return d.DialContext(ctx, network, addr)
		}
	}
	opts := []remote.Option{remote.WithContext(ctx), remote.WithTransport(httpsTo{host, t})}
	if s.pageSize > 0 {
		opts = append(opts, remote.WithPageSize(s.pageSize))
	}
	return opts
}

// httpsTo sends every request to host over HTTPS, which the registry alone speaks.
// go-containerregistry speaks plain HTTP to loopback and RFC 1918 addresses once it chose that
// scheme. The answer keeps the request as go-containerregistry made it, whose scheme it checks
// the links to further pages of a list against.
type httpsTo struct {
	host string
	next http.RoundTripper
}

func (t httpsTo) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != t.host || r.URL.Scheme == "https" {
		return t.next.RoundTrip(r)
	}
	https := r.Clone(r.Context())
	https.URL.Scheme = "https"
	resp, err := t.next.RoundTrip(https)
	if resp != nil {
		resp.Request = r
	}
	return resp, err
}

// pushable is an image or an index of images, as a source gives it.
type pushable interface {
	Digest() (v1.Hash, error)
}

// push pushes img to each endpoint in turn, reports each, and fails unless every endpoint serves
// img's digest under the target of ref.
func (s endpoints) push(ctx context.Context, out io.Writer, img pushable, ref name.Reference) error {
	want, err := img.Digest()
	if err != nil {
		return err
	}
	var missing []string
	for _, e := range s.Endpoints {
		target, err := s.targetOf(e, ref)
		if err == nil {
			opts := s.options(ctx, e)
			if err = write(target, img, opts...); err == nil {
				err = served(target, want, opts...)
			}
		}
		if err != nil {
			fmt.Fprintf(out, "%s: failed: %v\n", e, err)
			missing = append(missing, e.String())
			continue
		}
		fmt.Fprintf(out, "%s: pushed %s as %s\n", e, target, want)
	}
	if len(missing) > 0 {
		return fmt.Errorf("the image is missing on %d of %d endpoints of the cluster registry: %s; nothing copies it between them, so push it again once they are reachable", len(missing), len(s.Endpoints), strings.Join(missing, ", "))
	}
	return nil
}

// targetOf is ref below the endpoint e: <endpoint>/<prefix of ref's host>/<repository>, with
// ref's tag or digest. A host the ClusterRegistry does not mirror is refused.
func (s endpoints) targetOf(e netip.Addr, ref name.Reference) (name.Reference, error) {
	prefix, err := clusterregistry.Prefix(ref.Context().RegistryStr())
	if err != nil {
		return nil, err
	}
	if !slices.Contains(s.Mirrors, prefix) {
		return nil, fmt.Errorf("the ClusterRegistry mirrors %s, not %s, so containerd never pulls %s from it; add %s to its mirrors", strs.And(s.Mirrors), prefix, ref, prefix)
	}
	repo := s.Host(e) + "/" + prefix + "/" + ref.Context().RepositoryStr()
	if d, ok := ref.(name.Digest); ok {
		return name.NewDigest(repo + "@" + d.DigestStr())
	}
	return name.NewTag(repo + ":" + ref.Identifier())
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

// openSource reads the image of source: an OCI image layout directory, an OCI archive (a tar of
// one) or a docker archive as docker save writes it, each of one image or index, or else a
// reference to an image in a registry, read with the docker config's credentials. cleanup removes
// what reading it unpacked.
func openSource(ctx context.Context, source string) (img pushable, cleanup func(), err error) {
	cleanup = func() {}
	fi, err := os.Stat(source)
	switch {
	case err == nil && fi.IsDir():
		img, err = fromLayout(source)
		return img, cleanup, err
	case err == nil:
		return fromArchive(source)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, cleanup, err
	}
	ref, err := name.ParseReference(source)
	if err != nil {
		return nil, cleanup, fmt.Errorf("%s is no file, no directory and no image reference: %w", source, err)
	}
	desc, err := remote.Get(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return nil, cleanup, fmt.Errorf("%s is no file and no directory, and as an image reference: %w", source, err)
	}
	if desc.MediaType.IsIndex() {
		img, err = desc.ImageIndex()
	} else {
		img, err = desc.Image()
	}
	return img, cleanup, err
}

// fromLayout reads the one image or index the OCI image layout dir names.
func fromLayout(dir string) (pushable, error) {
	idx, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		return nil, err
	}
	m, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	if len(m.Manifests) != 1 {
		return nil, fmt.Errorf("%s names %d images; push a layout of one image", dir, len(m.Manifests))
	}
	d := m.Manifests[0]
	if d.MediaType.IsIndex() {
		return idx.ImageIndex(d.Digest)
	}
	return idx.Image(d.Digest)
}

// fromArchive reads an OCI archive, unpacked into a directory that cleanup removes, or a docker
// archive of one image.
func fromArchive(path string) (pushable, func(), error) {
	none := func() {}
	oci, err := holds(path, "oci-layout")
	if err != nil {
		return nil, none, err
	}
	if !oci {
		img, err := tarball.ImageFromPath(path, nil)
		if err != nil {
			return nil, none, fmt.Errorf("%s is neither an OCI archive nor a docker archive of one image: %w", path, err)
		}
		return img, none, nil
	}
	dir, err := os.MkdirTemp("", "chalkctl-oci-")
	if err != nil {
		return nil, none, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	var img pushable
	err = unpack(path, dir)
	if err == nil {
		img, err = fromLayout(dir)
	}
	if err != nil {
		cleanup()
		return nil, none, fmt.Errorf("%s: %w", path, err)
	}
	return img, cleanup, nil
}

// holds reports whether the tar archive at path holds the file name at its top.
func holds(path, file string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
		if filepath.Clean(h.Name) == file {
			return true, nil
		}
	}
}

// unpack writes the directories and regular files of the tar archive at path into dir, and
// refuses an entry that leaves it.
func unpack(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !filepath.IsLocal(h.Name) {
			return fmt.Errorf("the entry %s leaves the archive's directory", h.Name)
		}
		target := filepath.Join(dir, h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			err = writeEntry(target, tr)
		}
		if err != nil {
			return err
		}
	}
}

func writeEntry(target string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
