// Package credprovider is the kubelet's image credential provider on chalkos. chalkd writes the
// provider's config and the node's registry credentials from its Registry documents, and the
// kubelet runs chalkd credential-provider for an image of a host with credentials, which answers
// with them. The kubelet passes them to containerd with the pull, as it passes an
// imagePullSecret's.
package credprovider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config"
)

const apiVersion = "credentialprovider.kubelet.k8s.io/v1"

// cacheDuration is how long the kubelet reuses an answer for a registry. chalkos restarts the
// kubelet when a Registry or its password changes, which empties the cache, so the duration only
// bounds how long a stale answer survives a restart that did not happen. Five minutes keeps that
// short at the price of one run of chalkd per registry with credentials every five minutes.
const cacheDuration = "5m"

type login struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type provider struct {
	Name                 string   `json:"name"`
	MatchImages          []string `json:"matchImages"`
	DefaultCacheDuration string   `json:"defaultCacheDuration"`
	APIVersion           string   `json:"apiVersion"`
	Args                 []string `json:"args"`
}

// kubeletHost is the host an image's registry is as the kubelet names it: as RegistryHost names
// it, but docker.io for Docker Hub, as the kubelet normalises image names.
func kubeletHost(host string) (string, error) {
	reg, err := config.RegistryHost(host)
	if reg == "index.docker.io" {
		reg = "docker.io"
	}
	return reg, err
}

// Write writes into dir the kubelet's credential provider config, credentials.json and the file
// of the kubelet's flags. The config runs chalkd credential-provider from bin for the images of
// each host of logins with a user. credentials.json holds their users and passwords and nothing
// else, so a host whose user went is gone from it. dir and credentials.json are readable by root
// alone. The flags name the config and bin, or are empty without such a host, since the kubelet
// refuses a config without providers. The kubelet reads the config when it starts. Write refuses
// an IPv6 host without a port: the kubelet strips the brackets of an IPv6 address only together
// with a port, and otherwise reads them as a character class of its glob.
func Write(dir, bin string, logins []config.RegistryCredentials) error {
	creds := map[string]login{}
	for _, l := range logins {
		if l.Username == "" {
			continue
		}
		h, err := kubeletHost(l.Host)
		if err != nil {
			return fmt.Errorf("registry %q: %w", l.Host, err)
		}
		if strings.HasPrefix(h, "[") && !strings.Contains(h, "]:") {
			return fmt.Errorf("registry %q: the kubelet matches an IPv6 host only with its port; give the port, such as %s:443", l.Host, h)
		}
		creds[h] = login{l.Username, l.Password}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	cfgPath := filepath.Join(dir, "config.json")
	flags := "KUBELET_CREDENTIAL_PROVIDER_ARGS=\n"
	if len(creds) > 0 {
		flags = fmt.Sprintf("KUBELET_CREDENTIAL_PROVIDER_ARGS=--image-credential-provider-config=%s --image-credential-provider-bin-dir=%s\n", cfgPath, bin)
	}
	// [] rather than null, as the kubelet's config declares a list.
	matches := append([]string{}, slices.Sorted(maps.Keys(creds))...)
	cfg, err := json.Marshal(map[string]any{
		"apiVersion": "kubelet.config.k8s.io/v1",
		"kind":       "CredentialProviderConfig",
		"providers": []provider{{
			Name: "chalkd", MatchImages: matches, DefaultCacheDuration: cacheDuration,
			APIVersion: apiVersion, Args: []string{"credential-provider"},
		}},
	})
	if err != nil {
		return err
	}
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(cfgPath, cfg, 0o644); err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "flags"), []byte(flags), 0o644)
}

// Provide answers the kubelet's CredentialProviderRequest on in with the credentials in dir of
// the request's image's registry on out. It answers with none for a registry without
// credentials, matched by host and port, for an image it cannot parse, and before Write wrote
// any, so that the pull goes on without them.
func Provide(dir string, in io.Reader, out io.Writer) error {
	var req struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Image      string `json:"image"`
	}
	if err := json.NewDecoder(in).Decode(&req); err != nil {
		return fmt.Errorf("read the request: %w", err)
	}
	if req.APIVersion != apiVersion || req.Kind != "CredentialProviderRequest" {
		return fmt.Errorf("the request is a %s %s, not a %s CredentialProviderRequest", req.APIVersion, req.Kind, apiVersion)
	}
	resp := struct {
		APIVersion   string           `json:"apiVersion"`
		Kind         string           `json:"kind"`
		CacheKeyType string           `json:"cacheKeyType"`
		Auth         map[string]login `json:"auth,omitempty"`
	}{APIVersion: apiVersion, Kind: "CredentialProviderResponse", CacheKeyType: "Registry"}
	creds, err := readCredentials(dir)
	if err != nil {
		return err
	}
	if ref, err := name.ParseReference(req.Image); err == nil {
		if host, err := kubeletHost(ref.Context().RegistryStr()); err == nil {
			if l, ok := creds[host]; ok {
				resp.Auth = map[string]login{host: l}
			}
		}
	}
	return json.NewEncoder(out).Encode(resp)
}

// readCredentials returns the credentials Write wrote into dir, by registry; none before it wrote
// any.
func readCredentials(dir string) (map[string]login, error) {
	data, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var creds map[string]login
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("credentials.json: %w", err)
	}
	return creds, nil
}
