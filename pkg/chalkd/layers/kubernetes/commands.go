package kubernetes

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/credprovider"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/vxlan"
)

var _ layer.Commands = (*Plugin)(nil)

// Commands are the Kubernetes units' commands, run as chalkd kubernetes <name>.
func (p *Plugin) Commands() map[string]layer.Command {
	return map[string]layer.Command{
		"prepare": {
			Args: "[--vxlan-rule]",
			Help: "pick the node's addresses and write its Kubernetes\ncertificates and configuration; with --vxlan-rule, then fill\nthe firewall's VXLAN table from them",
			Run:  prepareKubernetes,
		},
		"vxlan-rule": {
			Args: "[FILE]",
			Help: "fill the firewall's VXLAN table from FILE, as the preparation\nwrites it, or empty it without FILE",
			Run:  vxlanRule,
		},
		"containerd-hosts": {
			Help: "print containerd's hosts.toml of each registry the node's\nMirror and Registry documents name, each after a line\n\"=== <directory>\"",
			Run:  layer.NoArgs(func() error { return containerdHosts(os.Stdout, "/run/chalkos") }),
		},
		"registry-credentials": {
			Help: "write the kubelet's credential provider config and the node's\nregistry credentials from its Registry documents",
			Run: layer.NoArgs(func() error {
				return registryCredentials("/run/chalkos", os.Getenv("CREDENTIALS_DIRECTORY"), k8s.CredentialProviderDir)
			}),
		},
		"credential-provider": {
			Help: "answer the kubelet's request for an image's credentials",
			Run: layer.NoArgs(func() error {
				return credprovider.Provide(k8s.CredentialProviderDir, os.Stdin, os.Stdout)
			}),
		},
	}
}

// prepareKubernetes prepares the node for Kubernetes, and with --vxlan-rule then fills the
// firewall's VXLAN table.
func prepareKubernetes(args []string) error {
	var firewall knode.Firewall
	switch {
	case len(args) == 1 && args[0] == "--vxlan-rule":
		firewall = knode.VXLANRule(vxlan.Default())
	case len(args) > 0:
		return layer.UsageError{Err: fmt.Errorf("takes --vxlan-rule alone, not %q", args)}
	}
	return knode.Prepare(knode.DefaultPaths(), time.Now(), knode.WaitForAddresses, firewall)
}

// vxlanRule fills the firewall's VXLAN table from the file given, or empties it without one.
func vxlanRule(args []string) error {
	if len(args) > 1 {
		return layer.UsageError{Err: fmt.Errorf("takes one file, not %q", args)}
	}
	var file string
	if len(args) == 1 {
		file = args[0]
	}
	return vxlan.Default().Apply(file)
}

// containerdHosts prints containerd's hosts.toml of each registry the node's Mirror and Registry
// documents below runDir name, by its directory below containerd's config_path, each after a line
// "=== <directory>", for chalkos-registry-mirrors to write.
func containerdHosts(w io.Writer, runDir string) error {
	docs, err := slot.ReadRuntime(runDir)
	if err != nil {
		return err
	}
	r, err := config.RegistriesOf(docs, nil)
	if err != nil {
		return err
	}
	files := r.HostsTOML()
	for _, dir := range slices.Sorted(maps.Keys(files)) {
		if _, err := fmt.Fprintf(w, "=== %s\n%s", dir, files[dir]); err != nil {
			return err
		}
	}
	return nil
}

// registryCredentials writes the kubelet's credential provider files into dir from the node's
// Registry documents below runDir, with the passwords among the unit's credentials, which systemd
// loads into the directory credentials: the unit is granted the Registry kind. The provider is
// this chalkd.
func registryCredentials(runDir, credentials, dir string) error {
	docs, err := slot.ReadRuntime(runDir)
	if err != nil {
		return err
	}
	r, err := config.RegistriesOf(docs, slot.CredentialSecrets(credentials))
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return credprovider.Write(dir, filepath.Dir(exe), r.Logins)
}
