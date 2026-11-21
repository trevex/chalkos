// Package units holds the chalkd commands that the units and diagnostics of every image run, in
// place of shell scripts: chalkd <name>, with no layer's name before it. modules/node/runtime/effects.nix
// lists their names among chalkd's own commands.
package units

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/tool"
)

// Paths of the running node the commands read.
const (
	// runDir holds the node's config as chalkd writes it for the node's services.
	runDir = "/run/chalkos"
	// sysClassNet lists the node's network devices.
	sysClassNet = "/sys/class/net"
	// netifLinks holds networkd's state of each link, with the time servers DHCP announced.
	netifLinks = "/run/systemd/netif/links"
)

// Commands are the commands, run with r.
func Commands(r tool.Runner) []*cobra.Command {
	return []*cobra.Command{
		{
			Use:   "apply-links",
			Short: "Apply the node's Link documents to its physical network devices",
			Long: "Have udev read its rules and links again, and replay the add events of the network devices\n" +
				"with a bus parent, so their Link documents apply.",
			Args: cli.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return ApplyLinks(cmd.Context(), r, sysClassNet) },
		},
		{
			Use:   "chrony-dhcp FILE",
			Short: "Write the time servers DHCP announced to chrony's sources FILE, when the TimeSync takes them",
			Long: "Write the time servers DHCP announced on each link to chrony's sources FILE when the node's\n" +
				"TimeSync sets dhcp, or an empty FILE otherwise, and have chronyd read its sources again.",
			Args: cli.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return ChronyDHCP(cmd.Context(), r, runDir, netifLinks, args[0], cmd.ErrOrStderr())
			},
		},
		{
			Use:   "chrony-reload-sources",
			Short: "Have a running chronyd read its sources again",
			Long:  "Have chronyd read its sources again while it runs; one that does not run reads them when it starts.",
			Args:  cli.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return ReloadSources(cmd.Context(), r) },
		},
		{
			Use:   "tcp-connect ADDRESS PORT",
			Short: "Open a TCP connection to ADDRESS and PORT, close it again and say whether it connected",
			Long: "Open a TCP connection to ADDRESS and PORT and close it again. Print whether it connected, and\n" +
				"exit with status 124 when it did not within 5 seconds, or 1 when it failed otherwise.",
			Args: cli.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return TCPConnect(cmd.Context(), dial, args[0], args[1], connectTimeout, cmd.OutOrStdout(), cmd.ErrOrStderr())
			},
		},
	}
}

// connectTimeout bounds tcp-connect's connect, which would otherwise wait for TCP's own timeout.
const connectTimeout = 5 * time.Second
