package config

import (
	"fmt"
	"net/url"
	"strings"
)

// dockerHub is Docker Hub's registry as RegistryHost names it.
const dockerHub = "index.docker.io"

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

// RegistriesOf returns the Registry and Mirror documents among docs, with each password taken
// from secret, or left empty when secret is nil. It reads documents as delivered, without the
// image's defaults, as chalkd reads an upgrade's config before it checks it against the image.
// It refuses two Registry or two Mirror documents of one registry, also when they name it
// differently, such as docker.io and index.docker.io, an endpoint with a user in its URL, and an
// http:// endpoint unless the Registry of its host allows plain HTTP.
func RegistriesOf(docs Documents, secret func(SecretRef) (string, bool)) (Registries, error) {
	var r Registries
	type named struct {
		doc  Document
		host string
	}
	logins := map[string]named{}
	for _, d := range docs.Of(OCIAPI, "Registry") {
		var spec registrySpec
		if err := d.decodeSpec(&spec); err != nil {
			return Registries{}, err
		}
		reg, err := RegistryHost(spec.Host)
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
		if err := d.decodeSpec(&spec); err != nil {
			return Registries{}, err
		}
		reg, err := RegistryHost(spec.Registry)
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
			if _, err := RegistryHost(u.Host); err != nil {
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
	reg, err := RegistryHost(host)
	if err != nil {
		return RegistryCredentials{}, false
	}
	for _, l := range r.Logins {
		if h, _ := RegistryHost(l.Host); h == reg {
			return l, true
		}
	}
	return RegistryCredentials{}, false
}

// Endpoints returns the endpoints of the Mirror of host, by any of its names; none without one.
func (r Registries) Endpoints(host string) []string {
	reg, err := RegistryHost(host)
	if err != nil {
		return nil
	}
	for _, m := range r.Mirrors {
		if h, _ := RegistryHost(m.Registry); h == reg {
			return m.Endpoints
		}
	}
	return nil
}

// HostsTOML returns containerd's hosts.toml of each registry a Mirror names, or whose Registry
// allows plain HTTP, by the directory below containerd's config_path that holds it: the
// registry's host in lower case, and docker.io for Docker Hub, as containerd looks them up.
// containerd tries the endpoints in order, then the registry itself, over plain HTTP when its
// Registry allows it.
func (r Registries) HostsTOML() map[string]string {
	out := map[string]string{}
	add := func(host string, endpoints []string) {
		// RegistriesOf checked the host.
		reg, _ := RegistryHost(host)
		dir, server := reg, reg
		if reg == dockerHub {
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
		for _, e := range endpoints {
			fmt.Fprintf(&b, "\n[host.%q]\n  capabilities = [\"pull\", \"resolve\"]\n", e)
		}
		out[dir] = b.String()
	}
	for _, m := range r.Mirrors {
		add(m.Registry, m.Endpoints)
	}
	for _, l := range r.Logins {
		if l.Insecure {
			add(l.Host, nil)
		}
	}
	return out
}
