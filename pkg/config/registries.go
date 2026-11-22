package config

import (
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/imageref"
)

// RegistryCredentials are what a Registry document gives a node for its host: Username and
// Password are empty for a registry pulled from anonymously, and Insecure allows plain HTTP to
// the host.
type RegistryCredentials struct {
	Host, Username, Password string
	Insecure                 bool
}

// RegistryMirror is what a Mirror document says of its registry: the endpoints chalkd and
// containerd try in order before the registry itself.
type RegistryMirror struct {
	Registry  string
	Endpoints []string
}

// Registries are a node's Registry and Mirror documents, each in the order of the documents.
type Registries struct {
	Logins  []RegistryCredentials
	Mirrors []RegistryMirror
}

// RegistriesOf returns the Registry and Mirror documents among docs, each password taken from
// secret, or empty when secret is nil. It needs no image defaults, so chalkd can read an
// upgrade's config before checking it against the image. It refuses two documents of one registry,
// also under different names such as docker.io and index.docker.io, an endpoint with a user in
// its URL, and plain HTTP unless the host's Registry allows it.
func RegistriesOf(docs Documents, secret func(SecretRef) (string, bool)) (Registries, error) {
	var r Registries
	type named struct {
		doc  Document
		host string
	}
	logins := map[string]named{}
	for _, d := range docs.Of(OCIAPI, "Registry") {
		var spec registrySpec
		if err := d.DecodeSpec(&spec); err != nil {
			return Registries{}, err
		}
		reg, err := imageref.RegistryHost(spec.Host)
		if err != nil {
			return Registries{}, fmt.Errorf("%s: host %q: %w", d, spec.Host, err)
		}
		if other, ok := logins[reg]; ok {
			if other.host == spec.Host {
				return Registries{}, fmt.Errorf("%s and %s both name the host %s; give one Registry document per host", other.doc, d, spec.Host)
			}
			return Registries{}, fmt.Errorf("%s and %s name the same registry %s, as %s and %s; give one Registry document per host", other.doc, d, reg, other.host, spec.Host)
		}
		logins[reg] = named{d, spec.Host}
		c := RegistryCredentials{Host: spec.Host, Insecure: spec.Insecure}
		if spec.Username != nil {
			c.Username = *spec.Username
		}
		if spec.Password != nil && secret != nil {
			ref := spec.Password.SecretRef
			v, ok := secret(ref)
			if !ok {
				return Registries{}, fmt.Errorf("%s names key %q of Secret %q, which the config does not hold", d, ref.Key, ref.Name)
			}
			c.Password = v
		}
		r.Logins = append(r.Logins, c)
	}
	mirrors := map[string]named{}
	for _, d := range docs.Of(OCIAPI, "Mirror") {
		var spec mirrorSpec
		if err := d.DecodeSpec(&spec); err != nil {
			return Registries{}, err
		}
		reg, err := imageref.RegistryHost(spec.Registry)
		if err != nil {
			return Registries{}, fmt.Errorf("%s: registry %q: %w", d, spec.Registry, err)
		}
		if other, ok := mirrors[reg]; ok {
			if other.host == spec.Registry {
				return Registries{}, fmt.Errorf("%s and %s both mirror %s; give one Mirror document per registry", other.doc, d, spec.Registry)
			}
			return Registries{}, fmt.Errorf("%s and %s mirror the same registry %s, as %s and %s; give one Mirror document per registry", other.doc, d, reg, other.host, spec.Registry)
		}
		mirrors[reg] = named{d, spec.Registry}
		for _, e := range spec.Endpoints {
			u, err := url.Parse(e)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
				return Registries{}, fmt.Errorf("%s: the endpoint %q is no http:// or https:// URL", d, e)
			}
			if u.User != nil {
				return Registries{}, fmt.Errorf("%s: the endpoint %q holds a user; give the credentials of %s in its Registry instead", d, u.Redacted(), u.Host)
			}
			if _, err := imageref.RegistryHost(u.Host); err != nil {
				return Registries{}, fmt.Errorf("%s: the endpoint %s: %w", d, e, err)
			}
			if l, ok := r.Login(u.Host); u.Scheme == "http" && (!ok || !l.Insecure) {
				return Registries{}, fmt.Errorf("%s: the endpoint %s is plain HTTP, which nothing authenticates; give %s a Registry with insecure: true to allow it", d, e, u.Host)
			}
		}
		r.Mirrors = append(r.Mirrors, RegistryMirror(spec))
	}
	return r, nil
}

// Login returns the Registry of host, by any of its names, and whether there is one.
func (r Registries) Login(host string) (RegistryCredentials, bool) {
	reg, err := imageref.RegistryHost(host)
	if err != nil {
		return RegistryCredentials{}, false
	}
	for _, l := range r.Logins {
		if h, _ := imageref.RegistryHost(l.Host); h == reg {
			return l, true
		}
	}
	return RegistryCredentials{}, false
}

// Endpoints returns the endpoints of the Mirror of host, by any of its names; none without one.
func (r Registries) Endpoints(host string) []string {
	reg, err := imageref.RegistryHost(host)
	if err != nil {
		return nil
	}
	for _, m := range r.Mirrors {
		if h, _ := imageref.RegistryHost(m.Registry); h == reg {
			return m.Endpoints
		}
	}
	return nil
}

// HostEndpoint is an endpoint containerd tries for a registry before the endpoints of its
// Mirror: its URL, whose path is the registry API's root when OverridePath is set rather than
// the prefix of /v2, the file of the CAs containerd verifies it by, empty for the system's, and
// the file of the client certificate's chain and key containerd presents to it, empty for none.
type HostEndpoint struct {
	URL          string
	OverridePath bool
	CA, Client   string
}

// HostsTOML returns containerd's hosts.toml of each registry that first, a Mirror or a Registry
// that allows plain HTTP names, keyed by the directory below config_path that containerd looks
// it up in. first names registries by any of their names. containerd tries the endpoints of
// first, then those of the Mirror, in order, then the registry itself.
func (r Registries) HostsTOML(first map[string][]HostEndpoint) map[string]string {
	before := map[string][]HostEndpoint{}
	for host, endpoints := range first {
		if reg, err := imageref.RegistryHost(host); err == nil {
			before[reg] = append(before[reg], endpoints...)
		}
	}
	out := map[string]string{}
	add := func(host string) {
		// RegistriesOf checked the hosts of Mirror and Registry documents, and before holds those
		// RegistryHost read.
		reg, _ := imageref.RegistryHost(host)
		dir, server := reg, reg
		if reg == imageref.DockerHub {
			dir, server = "docker.io", "registry-1.docker.io"
		}
		if _, ok := out[dir]; ok {
			return
		}
		scheme := "https"
		if l, ok := r.Login(reg); ok && l.Insecure {
			scheme = "http"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "server = %q\n", scheme+"://"+server)
		endpoints := before[reg]
		for _, e := range r.Endpoints(reg) {
			endpoints = append(endpoints, HostEndpoint{URL: e})
		}
		for _, e := range endpoints {
			fmt.Fprintf(&b, "\n[host.%q]\n  capabilities = [\"pull\", \"resolve\"]\n", e.URL)
			if e.OverridePath {
				b.WriteString("  override_path = true\n")
			}
			if e.CA != "" {
				fmt.Fprintf(&b, "  ca = %q\n", e.CA)
			}
			if e.Client != "" {
				fmt.Fprintf(&b, "  client = %q\n", e.Client)
			}
		}
		out[dir] = b.String()
	}
	for _, m := range r.Mirrors {
		add(m.Registry)
	}
	for _, reg := range slices.Sorted(maps.Keys(before)) {
		add(reg)
	}
	for _, l := range r.Logins {
		if l.Insecure {
			add(l.Host)
		}
	}
	return out
}
