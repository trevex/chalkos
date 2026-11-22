package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/clusterregistry"
	"github.com/trevex/chalkos/pkg/kubernetes/translate"
	"github.com/trevex/chalkos/pkg/tool"
)

// Paths of the running node the units' commands write.
const (
	// etcdDir holds etcd's data on a control plane.
	etcdDir = "/var/lib/etcd"
	// certsDir is containerd's config_path, which holds a directory with a hosts.toml per
	// registry.
	certsDir = "/run/containerd/certs.d"
)

// controlPlaneTable is the nftables table, of the inet family, that marks the control plane's
// ports on a control plane.
const controlPlaneTable = "chalkos-controlplane"

// parseMark reads a packet mark bit as nft takes it, such as 0x02000000.
func parseMark(s string) (string, error) {
	n, err := strconv.ParseUint(s, 0, 32)
	if err != nil || n == 0 || n&(n-1) != 0 {
		return "", fmt.Errorf("takes a packet mark bit such as 0x02000000, not %q", s)
	}
	return fmt.Sprintf("0x%08x", n), nil
}

// controlPlaneRuleset is the table, replaced in one transaction, that marks TCP to 6443, 2379
// and 2380 with mark. Its input chain runs before the firewall's and first clears the mark, so
// only this rule sets it; its clear chain runs after the firewall's, so the mark never outlives
// it.
func controlPlaneRuleset(mark string) string {
	clear := fmt.Sprintf("meta mark & %[1]s == %[1]s meta mark set meta mark ^ %[1]s", mark)
	return fmt.Sprintf(`table inet %[1]s
delete table inet %[1]s
table inet %[1]s {
  chain input {
    type filter hook input priority filter - 1; policy accept;
    %[2]s
    tcp dport { 6443, 2379, 2380 } meta mark set meta mark | %[3]s
  }
  chain clear {
    type filter hook input priority filter + 1; policy accept;
    %[2]s
  }
}
`, controlPlaneTable, clear, mark)
}

// controlPlane makes etcdDir, readable by root alone, when the node's Node document below runDir
// makes it a control plane. With a mark, the firewall's, it also loads the table that marks the
// control plane's ports on a control plane, and deletes the table on any other node. The NixOS
// firewall drops what its own input chain does not accept, whatever other tables do, so the
// table only marks the ports and the firewall accepts what carries the mark (see
// modules/node/kubernetes/firewall.nix). Reloading the firewall leaves the table alone.
func controlPlane(ctx context.Context, r tool.Runner, runDir, etcdDir, mark string) error {
	docs, err := slot.ReadRuntime(runDir)
	if err != nil {
		return err
	}
	isControlPlane := translate.NodeType(docs) == k8s.KindControlPlane
	if isControlPlane {
		if err := os.MkdirAll(filepath.Dir(etcdDir), 0o755); err != nil {
			return err
		}
		if err := os.Mkdir(etcdDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := os.Chmod(etcdDir, 0o700); err != nil {
			return err
		}
	}
	if mark == "" {
		return nil
	}
	if isControlPlane {
		_, err := r.Exec(ctx, tool.Cmd{Name: "nft", Args: []string{"-f", "-"}, Stdin: []byte(controlPlaneRuleset(mark))})
		return err
	}
	// A node that has no table keeps none.
	if _, err := r.Exec(ctx, tool.Cmd{Name: "nft", Args: []string{"list", "table", "inet", controlPlaneTable}, Quiet: true}); err != nil {
		return nil
	}
	_, err = tool.Run(ctx, r, "nft", "delete", "table", "inet", controlPlaneTable)
	return err
}

// osCAs holds the OS CAs a node trusts, STATE's chalkd/ca.crt, and nodePEM the node
// certificate's chain and key. The cluster registry's endpoints serve node certificates, which
// containerd verifies by osCAs, and read only for a certificate of the OS CA, for which
// containerd presents nodePEM. containerd reads both files again for every pull, so a renewed
// node certificate takes effect at the next pull without a restart.
const (
	osCAs   = "/state/chalkd/ca.crt"
	nodePEM = "/state/chalkd/node.pem"
)

// registryHosts returns containerd's hosts.toml of each registry the node's Mirror, Registry and
// ClusterRegistry documents below runDir name, by its directory below containerd's config_path.
// The cluster registry's endpoints come first, the one this node serves first of them.
func registryHosts(runDir string) (map[string]string, error) {
	docs, err := slot.ReadRuntime(runDir)
	if err != nil {
		return nil, err
	}
	r, err := config.RegistriesOf(docs, nil)
	if err != nil {
		return nil, err
	}
	cluster, ok, err := translate.ClusterRegistry(docs)
	if err != nil {
		return nil, err
	}
	first := map[string][]config.HostEndpoint{}
	if ok {
		for host, urls := range cluster.MirrorURLs(clusterregistry.Addrs(config.StaticAddresses(docs))) {
			for _, u := range urls {
				first[host] = append(first[host], config.HostEndpoint{URL: u, OverridePath: true, CA: osCAs, Client: nodePEM})
			}
		}
	}
	return r.HostsTOML(first), nil
}

// registryMirrors writes containerd's hosts.toml files from the node's documents below runDir to
// dir. The files go to a new directory next to dir that is swapped in for dir in one rename, so
// containerd never reads a mix of two configs nor finds none; a document that cannot be read
// leaves dir as it was.
func registryMirrors(runDir, dir string) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+".")
	if err != nil {
		return err
	}
	// After the swap, tmp holds the old files.
	defer os.RemoveAll(tmp)
	files, err := registryHosts(runDir)
	if err != nil {
		return err
	}
	for registry, hosts := range files {
		if err := os.Mkdir(filepath.Join(tmp, registry), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(tmp, registry, "hosts.toml"), []byte(hosts), 0o644); err != nil {
			return err
		}
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	err = unix.Renameat2(unix.AT_FDCWD, tmp, unix.AT_FDCWD, dir, unix.RENAME_EXCHANGE)
	if errors.Is(err, unix.ENOENT) {
		return os.Rename(tmp, dir)
	}
	if err != nil {
		return &os.LinkError{Op: "exchange", Old: tmp, New: dir, Err: err}
	}
	return nil
}
