package config

import (
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// RegistryCredentials are what a Registry document gives a node to pull from its host with:
// Username and Password are empty for a registry pulled from anonymously, and Insecure allows
// plain HTTP to the host.
type RegistryCredentials struct {
	Host, Username, Password string
	Insecure                 bool
}

// RegistryHost is the registry an image reference's host names, as go-containerregistry names
// registries: in lower case, and index.docker.io for docker.io.
func RegistryHost(host string) (string, error) {
	r, err := name.NewRegistry(strings.ToLower(host), name.StrictValidation)
	if err != nil {
		return "", err
	}
	return r.RegistryStr(), nil
}

// RegistryCredentialsOf returns the credentials of each Registry document among docs, in their
// order, with the password its secretRef names taken from the Secrets among docs. It reads
// documents as delivered, without the image's defaults, as chalkd reads an upgrade's config
// before it checks it against the image. Two documents of one registry are refused, also when
// they name it differently, such as docker.io and index.docker.io.
func RegistryCredentialsOf(docs []Document) ([]RegistryCredentials, error) {
	secrets := map[string]map[string]string{}
	for _, d := range docsOf(docs, CoreAPI, "Secret") {
		var spec secretSpec
		if err := d.decodeSpec(&spec); err != nil {
			return nil, err
		}
		secrets[d.Name] = spec.Data
	}
	var out []RegistryCredentials
	type named struct {
		doc  Document
		host string
	}
	by := map[string]named{}
	for _, d := range docsOf(docs, CoreAPI, "Registry") {
		var spec registrySpec
		if err := d.decodeSpec(&spec); err != nil {
			return nil, err
		}
		reg, err := RegistryHost(spec.Host)
		if err != nil {
			return nil, fmt.Errorf("%s: host %q: %w", d, spec.Host, err)
		}
		if other, ok := by[reg]; ok {
			if other.host == spec.Host {
				return nil, fmt.Errorf("%s and %s both name the host %s; give one Registry document per host", other.doc, d, spec.Host)
			}
			return nil, fmt.Errorf("%s and %s name the same registry %s, as %s and %s; give one Registry document per host", other.doc, d, reg, other.host, spec.Host)
		}
		by[reg] = named{d, spec.Host}
		c := RegistryCredentials{Host: spec.Host, Insecure: spec.Insecure}
		if spec.Username != nil {
			c.Username = *spec.Username
		}
		if spec.Password != nil {
			ref := spec.Password.SecretRef
			v, ok := secrets[ref.Name][ref.Key]
			if !ok {
				return nil, fmt.Errorf("%s names key %q of Secret %q, which the config does not hold", d, ref.Key, ref.Name)
			}
			c.Password = v
		}
		out = append(out, c)
	}
	return out, nil
}
