package config

import (
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// RegistryCredentials are what a Registry document gives a node for its host: Username and
// Password are empty for a registry pulled from anonymously, and Insecure allows plain HTTP to
// the host.
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
