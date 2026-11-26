package chalkctl

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/chalkd"
	kubernetesplugin "github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	routerplugin "github.com/trevex/chalkos/pkg/chalkd/layers/router"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
)

// commands are the commands of chalkctl's tree other than groups, by their names below chalkctl.
func commands(t *testing.T) map[string]*cobra.Command {
	t.Helper()
	all := map[string]*cobra.Command{}
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if !cmd.HasSubCommands() && cmd.HasParent() {
			all[commandName(cmd)] = cmd
		}
		for _, c := range cmd.Commands() {
			walk(c)
		}
	}
	walk(newCommand(&app{}))
	return all
}

// Every command declares its credentials, takes --config exactly when a client file works for it,
// and takes --yes exactly when it confirms; client new's help names the commands a client file
// works for, and no command registers --force, whose meaning would differ from one to the next.
func TestCommandMeta(t *testing.T) {
	var clientCommands []string
	for name, cmd := range commands(t) {
		m := layer.MetaOf(cmd)
		if !slices.Contains(credentialsOrder, m.Credentials) {
			t.Errorf("%s declares the credentials %q", name, m.Credentials)
		}
		if client := m.Credentials.Role() != ""; client != (cmd.Flags().Lookup("config") != nil) {
			t.Errorf("%s declares the credentials %s, and takes --config: %v", name, m.Credentials, !client)
		}
		if m.Credentials.Role() != "" {
			clientCommands = append(clientCommands, name)
		}
		if confirms := m.Confirms != ""; confirms != (cmd.Flags().Lookup("yes") != nil) {
			t.Errorf("%s confirms: %v, but takes --yes: %v", name, confirms, !confirms)
		}
		if cmd.Flags().Lookup("force") != nil {
			t.Errorf("%s registers --force; name the flag after what it overrides", name)
		}
		if !strings.Contains(strings.Join(strings.Fields(cmd.Long), " "), "The command needs "+needs(m.Credentials)+".") {
			t.Errorf("%s's help does not name its credentials:\n%s", name, cmd.Long)
		}
	}
	slices.Sort(clientCommands)
	clientNew := commands(t)["client new"]
	if want := "A client file works for " + strings.Join(clientCommands[:len(clientCommands)-1], ", ") + " and " + clientCommands[len(clientCommands)-1] + ";"; !strings.Contains(strings.Join(strings.Fields(clientNew.Long), " "), want) {
		t.Errorf("client new's help does not say %q:\n%s", want, clientNew.Long)
	}
}

// registryRead and registryWrite stand for the cluster registry's reads and writes, which
// chalkos-registry grants to a client of the reader and of the admin role.
const (
	registryRead  = "registry read"
	registryWrite = "registry write"
)

// procedures are the procedures each command that takes a client file calls to do its work.
var procedures = map[string][]string{
	"bootstrap":          {kubernetesv1connect.KubernetesServiceStatusProcedure, kubernetesv1connect.KubernetesServiceBootstrapProcedure},
	"disks":              {nodev1connect.NodeServiceInfoProcedure, nodev1connect.NodeServiceDisksProcedure},
	"etcd leave":         {kubernetesv1connect.KubernetesServiceEtcdLeaveProcedure},
	"etcd members":       {kubernetesv1connect.KubernetesServiceEtcdMembersProcedure},
	"etcd remove-member": {kubernetesv1connect.KubernetesServiceEtcdMembersProcedure, kubernetesv1connect.KubernetesServiceEtcdRemoveMemberProcedure},
	"logs":               {nodev1connect.NodeServiceLogsProcedure},
	"node activate":      {nodev1connect.NodeServiceStatusProcedure, nodev1connect.NodeServiceActivateImageProcedure},
	"node diag":          {nodev1connect.NodeServiceDiagnosticsProcedure, nodev1connect.NodeServiceRunDiagnosticProcedure},
	"node reboot": {nodev1connect.NodeServiceStatusProcedure, nodev1connect.NodeServiceRebootProcedure,
		kubernetesv1connect.KubernetesServiceDrainNodeProcedure, kubernetesv1connect.KubernetesServiceUncordonNodeProcedure},
	"node revoke": {kubernetesv1connect.KubernetesServiceStatusProcedure, kubernetesv1connect.KubernetesServiceEtcdMembersProcedure,
		kubernetesv1connect.KubernetesServiceRevokeNodeProcedure},
	"node routes":       {routerv1connect.RouterServiceRoutesProcedure},
	"node unrevoke":     {kubernetesv1connect.KubernetesServiceUnrevokeNodeProcedure},
	"registry delete":   {registryRead, registryWrite},
	"registry gc":       {registryWrite},
	"registry list":     {registryRead},
	"registry push":     {registryRead, registryWrite},
	"secureboot enroll": {nodev1connect.NodeServiceInfoProcedure, nodev1connect.NodeServiceSecureBootDriversProcedure, nodev1connect.NodeServiceEnrollSecureBootProcedure},
	"status":            {nodev1connect.NodeServiceInfoProcedure, nodev1connect.NodeServiceStatusProcedure, kubernetesv1connect.KubernetesServiceStatusProcedure},
	"storage reset":     {nodev1connect.NodeServiceContractProcedure, nodev1connect.NodeServiceStatusProcedure, nodev1connect.NodeServiceResetVolumeProcedure},
	"upgrade": {nodev1connect.NodeServiceInfoProcedure, nodev1connect.NodeServiceStatusProcedure, nodev1connect.NodeServiceUpgradeProcedure,
		nodev1connect.NodeServiceActivateImageProcedure, nodev1connect.NodeServiceRebootProcedure,
		kubernetesv1connect.KubernetesServiceDrainNodeProcedure, kubernetesv1connect.KubernetesServiceUncordonNodeProcedure},
}

// The credentials each command declares are the least role that chalkd's procedures, and the
// cluster registry's reads and writes, grant everything the command calls.
func TestCredentialsMatchChalkdRoles(t *testing.T) {
	roles := map[string]pki.Role{registryRead: pki.RoleReader, registryWrite: pki.RoleAdmin}
	perms := chalkd.Permissions()
	for procedure, p := range (&kubernetesplugin.Plugin{}).Permissions() {
		perms[procedure] = p
	}
	for procedure, p := range (&routerplugin.Plugin{}).Permissions() {
		perms[procedure] = p
	}
	for procedure, p := range perms {
		roles[procedure] = p.Role
	}
	all := commands(t)
	for name, calls := range procedures {
		cmd, ok := all[name]
		if !ok {
			t.Errorf("no command %s", name)
			continue
		}
		need := pki.RoleReader
		for _, procedure := range calls {
			role, ok := roles[procedure]
			if !ok {
				t.Errorf("%s calls %s, which chalkd does not serve", name, procedure)
				continue
			}
			if !need.Allows(role) {
				need = role
			}
		}
		if got := layer.MetaOf(cmd).Credentials.Role(); got != need {
			t.Errorf("%s declares the %q role, its procedures need %s", name, got, need)
		}
	}
	for name, cmd := range all {
		if _, ok := procedures[name]; !ok && layer.MetaOf(cmd).Credentials.Role() != "" {
			t.Errorf("%s takes a client file; name the procedures it calls", name)
		}
	}
}

// storage reset refuses without a terminal unless --yes is given, and on a terminal asks for the
// node's name, which must be typed as it is.
func TestStorageResetConfirms(t *testing.T) {
	ta := newTestApp(t)
	withPassword(t, ta, storage.EncryptionNone)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	reset := ta.args([]string{"storage", "reset", "n1", "data"}, addr)
	// The node refuses the reset of a volume it has no partition of, which shows the reset reached it.
	reached := "volume data has no recorded partition"

	err := ta.run(context.Background(), reset)
	if err == nil || !strings.Contains(err.Error(), "storage reset wipes the volume data of n1") || !strings.Contains(err.Error(), "pass --yes") || strings.Contains(err.Error(), reached) {
		t.Errorf("without a terminal: %v, want a refusal naming --yes", err)
	}
	if err := ta.run(context.Background(), append(slices.Clone(reset), "--yes")); err == nil || !strings.Contains(err.Error(), reached) {
		t.Errorf("with --yes: %v, want the node to refuse", err)
	}

	var prompts []string
	answer := "n2"
	ta.ask = func(_ context.Context, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		return answer, nil
	}
	if err := ta.run(context.Background(), reset); err == nil || err.Error() != errNotConfirmed.Error() {
		t.Errorf("typing n2: %v, want %v", err, errNotConfirmed)
	}
	answer = "n1"
	if err := ta.run(context.Background(), reset); err == nil || !strings.Contains(err.Error(), reached) {
		t.Errorf("typing n1: %v, want the node to refuse", err)
	}
	if len(prompts) != 2 || !strings.HasSuffix(prompts[0], "and its data is lost.\nType n1 to go ahead: ") {
		t.Errorf("prompts = %q, want the node's name asked for twice", prompts)
	}
}

// An upgrade of every node of the inventory asks once it printed the nodes, before the first
// change; one of the nodes --nodes names, or one that stops at the stage step, does not.
func TestUpgradeConfirmsEveryNode(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	err := l.run("upgrade", "--image", img)
	if err == nil || !strings.Contains(err.Error(), "upgrade without --nodes changes every node of") || !strings.Contains(err.Error(), "pass --yes") {
		t.Fatalf("without a terminal: %v, want a refusal naming --yes", err)
	}
	if !strings.Contains(l.ta.stdout.String(), "upgrading w-metal on metal") {
		t.Errorf("the refusal came before the nodes were printed:\n%s", l.ta.stdout)
	}
	if events := l.takeEvents(); len(events) != 0 {
		t.Errorf("a refused upgrade changed the nodes: %q", events)
	}
	for name, v := range l.images() {
		if v != "0.1.0" {
			t.Errorf("%s runs %s after a refused upgrade", name, v)
		}
	}
	if err := l.run("upgrade", "--image", img, "--until", "stage"); err != nil {
		t.Errorf("--until stage: %v", err)
	}
	var asked []string
	l.ta.ask = func(_ context.Context, prompt string) (string, error) {
		asked = append(asked, prompt)
		return "y", nil
	}
	if err := l.run("upgrade", "--image", img, "--nodes", "w1"); err != nil {
		t.Errorf("--nodes w1: %v", err)
	}
	if len(asked) != 0 {
		t.Errorf("--nodes asked %q", asked)
	}
	if err := l.run("upgrade", "--image", img); err != nil {
		t.Errorf("answering y: %v", err)
	}
	if len(asked) != 1 || !strings.HasSuffix(asked[0], "Go ahead? [y/N] ") {
		t.Errorf("asked %q, want one question", asked)
	}
}

// install --wipe-disk asks for the node's name once it printed the disk, before the installer is
// sent anything, and refuses without a terminal unless --yes is given.
func TestInstallWipeDiskConfirms(t *testing.T) {
	ta := newTestApp(t)
	imageDir := testUpgradeImage(t, "gpu-metal", "0.1.0")
	copySchemas(t, filepath.Join(imageDir, "schemas"))
	writeFile(t, filepath.Join(imageDir, "repart.d", "50-state.conf"), "[Partition]\nLabel=state\n")
	s := maintenanceNode()
	s.Installer = true
	var installs []install.PartsRequest
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		installs = append(installs, req)
		_, err := io.Copy(io.Discard, req.Parts)
		return err
	}
	addr := ta.startNode(t, s)
	args := ta.args([]string{"install", "n1", "--fingerprint", s.Fingerprint, "--image", imageDir, "--wipe-disk"}, addr)

	err := ta.run(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "the installer replaces whatever the disk") || !strings.Contains(err.Error(), "pass --yes") {
		t.Errorf("without a terminal: %v, want a refusal naming --yes", err)
	}
	if !strings.Contains(ta.stdout.String(), "installing n1 onto") || len(installs) != 0 {
		t.Errorf("printed %q and installed %d times, want the disk printed and nothing sent", ta.stdout, len(installs))
	}
	var prompts []string
	ta.ask = func(_ context.Context, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		return "n1", nil
	}
	if err := ta.run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "Type n1 to go ahead") || len(installs) != 1 || !installs[0].WipeDisk {
		t.Errorf("prompts %q, installs %+v; want one question and a wiping install", prompts, installs)
	}
}
