package kubernetes

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/internal/cli"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/credprovider"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/translate"
	"github.com/trevex/chalkos/pkg/kubernetes/vxlan"
)

var _ layer.Commands = (*Plugin)(nil)

// Commands are the Kubernetes units' commands, run as chalkd kubernetes <name>.
func (p *Plugin) Commands() []*cobra.Command {
	var withRule bool
	prepare := &cobra.Command{
		Use:   "prepare",
		Short: "Pick the node's addresses and write its Kubernetes certificates and configuration",
		Long: "Pick the node's addresses and write its Kubernetes certificates and configuration. With\n" +
			"--vxlan-rule, then fill the firewall's VXLAN table from them.",
		Args: cli.NoArgs,
		RunE: func(*cobra.Command, []string) error { return prepareKubernetes(withRule) },
	}
	prepare.Flags().BoolVar(&withRule, "vxlan-rule", false, "fill the firewall's VXLAN table once the node is prepared")
	return []*cobra.Command{
		prepare,
		{
			Use:   "vxlan-rule [FILE]",
			Short: "Fill the firewall's VXLAN table from FILE, or empty it",
			Long:  "Fill the firewall's VXLAN table from FILE, as the preparation writes it, or empty it without FILE.",
			Args:  cli.RangeArgs(0, 1),
			RunE: func(_ *cobra.Command, args []string) error {
				var file string
				if len(args) == 1 {
					file = args[0]
				}
				return vxlan.Default().Apply(file)
			},
		},
		{
			Use:   "containerd-hosts",
			Short: "Print containerd's hosts.toml of each registry the node's documents name",
			Long: "Print containerd's hosts.toml of each registry the node's Mirror and Registry documents\n" +
				"name, each after a line \"=== <directory>\".",
			Args: cli.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return containerdHosts(cmd.OutOrStdout(), "/run/chalkos") },
		},
		{
			Use:   "containerd-config FILE",
			Short: "Write containerd's settings from the node's Cluster document to FILE",
			Long: "Write containerd's settings from the node's Cluster document, the pod sandbox's image, to\n" +
				"FILE, which containerd imports.",
			Args: cli.ExactArgs(1),
			RunE: func(_ *cobra.Command, args []string) error { return containerdConfig("/run/chalkos", args[0]) },
		},
		{
			Use:   "registry-credentials",
			Short: "Write the kubelet's credential provider files from the node's Registry documents",
			Long: "Write the kubelet's credential provider config and the node's registry credentials from its\n" +
				"Registry documents.",
			Args: cli.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				return registryCredentials("/run/chalkos", os.Getenv("CREDENTIALS_DIRECTORY"), k8s.CredentialProviderDir)
			},
		},
		{
			Use:   "credential-provider",
			Short: "Answer the kubelet's request for an image's credentials",
			Long: "Answer the kubelet's request for an image's credentials: read a CredentialProviderRequest\n" +
				"on standard input and write the CredentialProviderResponse to standard output.",
			Args: cli.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return credprovider.Provide(k8s.CredentialProviderDir, cmd.InOrStdin(), cmd.OutOrStdout())
			},
		},
	}
}

// prepareKubernetes prepares the node for Kubernetes, and with withRule then fills the firewall's
// VXLAN table.
func prepareKubernetes(withRule bool) error {
	var firewall knode.Firewall
	if withRule {
		firewall = knode.VXLANRule(vxlan.Default())
	}
	return knode.State{Paths: knode.DefaultPaths()}.Prepare(time.Now(), knode.WaitForAddresses, firewall)
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

// containerdConfig writes the containerd settings that come from the node's Cluster document
// below runDir to file, which containerd's config imports: the pod sandbox's image. Without a
// Cluster document the file sets nothing, and containerd keeps its own sandbox image.
func containerdConfig(runDir, file string) error {
	docs, err := slot.ReadRuntime(runDir)
	if err != nil {
		return err
	}
	pause, err := translate.SandboxImage(docs)
	if err != nil {
		return err
	}
	// containerd refuses an imported file of a newer version than its own config's, which
	// containerd.nix sets to 4.
	text := "version = 4\n"
	if pause != "" {
		text += fmt.Sprintf("\n[plugins.\"io.containerd.cri.v1.images\".pinned_images]\n  sandbox = %q\n", pause)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(file, []byte(text), 0o644)
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
	logins := make([]credprovider.Login, len(r.Logins))
	for i, l := range r.Logins {
		logins[i] = credprovider.Login{Host: l.Host, Username: l.Username, Password: l.Password}
	}
	return credprovider.Write(dir, filepath.Dir(exe), logins)
}
