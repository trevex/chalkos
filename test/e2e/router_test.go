package e2e

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/lab"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/router"
)

// routerLabName is the metadata.name of chalklab/lab-router.yaml, by which chalklab keys the
// lab's state directory.
const routerLabName = "chalklab-router"

// bgpPassword is the password of the routers' session, the value of the Secret bgp of
// chalklab/groups/router.yaml.
const bgpPassword = "lab-s3cret"

// TestRouterLab brings up through chalklab r1 and r2 of the main cluster, routers of the router
// image on the lab's switch, with IPv6 link-local addresses alone between them. They peer
// unnumbered over BGP on enp0s3, with BFD and a password. Each phase is a subtest; a failed phase
// stops the ones after it.
//   - bgp: both routers report the session Established and BFD up in their status, which shows a
//     reader no password, r2 learns the default route r1 originates and r1's NAT64 prefix, in its
//     kernel and in BGP, the bgp-summary diagnostic lists the session, the filled frr.conf is
//     root's with mode 0600, no unit failed, and r1, an image without the Kubernetes layer,
//     answers Bootstrap with Unimplemented.
//   - reload: a delivery that has r1 announce the address of its edge0 reaches r2 while the
//     session stays up: FRR reloads, its password filled in again into a frr.conf that is still
//     root's with mode 0600, and restarts nothing.
//   - nat64: from the address of its edge0, r2 asks r1's resolver for the AAAA records of
//     ipv4only.arpa and gets those DNS64 synthesizes in the NAT64 prefix, and connects through
//     r1's NAT64, whose empty pool4 r1 masquerades, and QEMU's user-mode network to a listener of
//     the test on the host. r1's status, for an admin and for a reader, shows the connection's
//     session and binding and its resolver running, the nat64-sessions diagnostic shows the
//     session, and no unit of r1 failed.
//   - node-ca: chalkctl rotate node-ca over the lab's inventory, on a copy of the secrets file,
//     gives r1 and r2 certificates of the new node CA, which a new connection verifies, and
//     their status reports the node certificate without a problem.
//   - rollback: apply-config --rollback, without an inventory, brings r1 back to the config it
//     ran before the reload, so r2 loses the route to r1's edge0, and a second rollback brings
//     the route back; frr.conf stays root's with mode 0600.
//   - trial: apply-config --try with the base inventory is confirmed over a new connection; one
//     that accepts chalkd's port on enp0s3 alone cuts the host off, so chalkctl cannot confirm it,
//     r1 rolls it back at the 30 s deadline and new connections reach it again.
//   - vrf: apply-config --try with an inventory that puts both routers' enp0s3, and r2's edge0,
//     into the VRF fabric of table 100, with their BGP sessions, and r2's resolver of fabric, is
//     confirmed over a new connection, so chalkd still answers on enp0s2. The sessions run in
//     fabric, r1 routes r2's edge0 in table 100 and, through its default VRF's import of fabric,
//     in the main table, with the path imported from fabric, and table 100 ends in unreachable
//     default routes of both families; r2's resolver of fabric runs and answers a query from
//     within fabric, chalkctl shows the VRF in the status and with --vrf, bgp-summary vrf=fabric
//     lists the session, and no unit failed. r1's PolicyRoute edge leaves a rule of priority 900
//     per target table, table 300 with the default route through its next hop and an
//     unreachable default, and the firewall's chain policy-routing, and so does a boot of r1,
//     after which the session comes back in fabric and no unit failed. r2's fabric then moves to
//     table 200, which removes the VRF and creates it anew: the session comes back in fabric,
//     r2's routes, edge0's address and the unreachable defaults sit in table 200, none of the
//     defaults stays in table 100, r1 learns edge0 again and r2's resolver of fabric runs and
//     answers again. The base inventory then brings the sessions and edge0 back to the default
//     VRF, and removes r1's policy rules and chain.
//   - default: a delivery has r1 send r2 its default route only while r1 routes 2001:db8:fff0::/48,
//     a networkd route on its dummy uplink0. Delivered without the route, it makes r2 lose the
//     unconditional default of the base config at once and get none back for 15 s. Delivered with
//     the route, r1's BGP holds it, marked no-advertise and with the tracked large community, r2
//     routes ::/0 through r1 again and never learns the tracked prefix. The base inventory then
//     brings the unconditional default back.
//   - dhcp: a delivery gives both routers a VLAN dhcp0 on enp0s3. r1's, in the VRF lan, has a DHCP
//     server that reserves 192.0.2.10 for r2's MAC address, with leases of two minutes, and r2's
//     takes its address from DHCP. r1's status shows the server of lan running with r2's lease, r2
//     holds the address, and the diagnostic dhcp-leases lists the lease. After a boot of r1 the
//     server runs again with the lease and r2 renews it there. The base inventory stops the server;
//     networkd keeps both routers' dhcp0 devices, as it keeps every netdev, and no diagnostic
//     deletes a link, so the phase runs last.
//
// Two VMs of 768 MiB and 2 CPUs each; about 85 s once chalklab installed both, a few seconds for
// the node CA, about 10 s for the rollbacks, 45 s for the trials, two minutes for the VRF with
// r1's boot, half a minute for the default route and three minutes for DHCP with r1's boot,
// about 10.5 minutes with the copies of the images. chalklab destroy removes the lab when the
// test ends.
func TestRouterLab(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_CHALKLAB", "CHALKLAB_ROUTER_IMAGE_DIR"}, chalkdEnv...)...)
	dir := vmDir(t)
	startNTP(t)
	nodes, _, _, _ := startLab(t, dir, routerLabName, "lab-router.yaml")
	admin := clientCertificate(t, pki.RoleAdmin)
	reader := clientCertificate(t, pki.RoleReader)
	// The routers' connections for an admin and for a reader.
	conns := map[string]*client.Conn{}
	readers := map[string]*client.Conn{}
	for _, name := range []string{"r1", "r2"} {
		conns[name] = dialNode(t, nodes[name], name, admin)
		t.Cleanup(func() { conns[name].Close() })
		readers[name] = dialNode(t, nodes[name], name, reader)
		t.Cleanup(func() { readers[name].Close() })
	}
	// routerOf is the client of the router layer of c.
	routerOf := func(c *client.Conn) routerv1connect.RouterServiceClient {
		return routerv1connect.NewRouterServiceClient(c.HTTPClient(), c.BaseURL())
	}
	// session is the router's session of the BGPPeer on enp0s3, as its status reports it.
	session := func(name, peer string) (*routerv1.BGPPeerStatus, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := routerOf(conns[name]).Status(ctx, connect.NewRequest(&routerv1.StatusRequest{}))
		if err != nil {
			return nil, err
		}
		r := resp.Msg
		switch {
		case r.BgpError != "":
			return nil, fmt.Errorf("its BGP state is unknown: %s", r.BgpError)
		}
		for _, p := range r.Peers {
			if p.Name == peer && p.Neighbor == "enp0s3" {
				return p, nil
			}
		}
		return nil, fmt.Errorf("%s has no session of %s on enp0s3: %v", name, peer, r.Peers)
	}
	established := func(name, peer string) error {
		p, err := session(name, peer)
		if err != nil {
			return err
		}
		if p.State != "Established" || p.Bfd != "up" || p.Vrf != "default" {
			return fmt.Errorf("the session of %s on %s is %s in the VRF %q, its BFD %q", peer, name, p.State, p.Vrf, p.Bfd)
		}
		return nil
	}
	// inFabric is nil once the router's session of the peer is up in the VRF fabric and has been
	// for at most the time since start.
	inFabric := func(name, peer string, start time.Time) error {
		p, err := session(name, peer)
		if err != nil {
			return err
		}
		if p.State != "Established" || p.Bfd != "up" || p.Vrf != "fabric" {
			return fmt.Errorf("the session of %s on %s is %s in the VRF %q, its BFD %q", peer, name, p.State, p.Vrf, p.Bfd)
		}
		if elapsed := time.Since(start); time.Duration(p.UptimeSeconds)*time.Second > elapsed+2*time.Second {
			return fmt.Errorf("the session of %s on %s is up for %ds, since before the delivery %v ago", peer, name, p.UptimeSeconds, elapsed.Round(time.Second))
		}
		return nil
	}
	// learnedFrom is nil once the router name routes the prefix in the VRF through its peer of the
	// AS as: zebra installed it over enp0s3 to an IPv6 link-local next hop, and BGP's best path came
	// from as on enp0s3. An empty VRF is the default one.
	learnedFrom := func(name, vrf, prefix, as string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := routerOf(conns[name]).Routes(ctx, connect.NewRequest(&routerv1.RoutesRequest{Prefix: prefix, Protocol: "bgp", Vrf: vrf}))
		if err != nil {
			return err
		}
		kernel := slices.ContainsFunc(resp.Msg.Kernel, func(k *routerv1.KernelRoute) bool {
			return k.Destination == prefix && slices.ContainsFunc(k.NextHops, func(h *routerv1.NextHop) bool {
				return h.Interface == "enp0s3" && strings.HasPrefix(h.Gateway, "fe80:")
			})
		})
		bgp := slices.ContainsFunc(resp.Msg.Bgp, func(b *routerv1.BGPRoute) bool {
			return b.Prefix == prefix && b.Best && b.AsPath == as && b.Peer == "enp0s3"
		})
		if !kernel || !bgp {
			return fmt.Errorf("%s's routes to %s in the VRF %q: kernel %v, BGP %v, BGP's error %q", name, prefix, vrf, resp.Msg.Kernel, resp.Msg.Bgp, resp.Msg.BgpError)
		}
		return nil
	}
	// learned is nil once r2 routes the prefix through r1.
	learned := func(prefix string) error { return learnedFrom("r2", "", prefix, "64512") }
	// leaked is nil once r1's default VRF routes the prefix over enp0s3, a link of the VRF from,
	// and its BGP instance holds the best path imported from that VRF.
	leaked := func(prefix, from string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := routerOf(conns["r1"]).Routes(ctx, connect.NewRequest(&routerv1.RoutesRequest{Prefix: prefix, Protocol: "bgp"}))
		if err != nil {
			return err
		}
		kernel := slices.ContainsFunc(resp.Msg.Kernel, func(k *routerv1.KernelRoute) bool {
			return k.Destination == prefix && k.Table == 254 && slices.ContainsFunc(k.NextHops, func(h *routerv1.NextHop) bool { return h.Interface == "enp0s3" })
		})
		bgp := slices.ContainsFunc(resp.Msg.Bgp, func(b *routerv1.BGPRoute) bool {
			return b.Prefix == prefix && b.Best && b.ImportedFrom == from
		})
		if !kernel || !bgp {
			return fmt.Errorf("r1's default VRF's routes to %s: kernel %v, BGP %v, BGP's error %q", prefix, resp.Msg.Kernel, resp.Msg.Bgp, resp.Msg.BgpError)
		}
		return nil
	}
	// inTable is nil once the router's VRF fabric routes the prefix over the link in the table.
	inTable := func(name, prefix, link string, table uint32) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := routerOf(conns[name]).Routes(ctx, connect.NewRequest(&routerv1.RoutesRequest{Prefix: prefix, Vrf: "fabric"}))
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(resp.Msg.Kernel, func(k *routerv1.KernelRoute) bool {
			return k.Destination == prefix && k.Table == table && slices.ContainsFunc(k.NextHops, func(h *routerv1.NextHop) bool { return h.Interface == link })
		}) {
			return fmt.Errorf("%s's VRF fabric routes %s over %s in no table %d: %v", name, prefix, link, table, resp.Msg.Kernel)
		}
		return nil
	}
	// rootsConfig checks that r1's filled frr.conf, which holds the password, is root's alone, as
	// the test image's chalktest-frr-conf diagnostic shows it.
	rootsConfig := func(t *testing.T) {
		t.Helper()
		out, err := chalkctl(t, nodes["r1"], "base", "node", "diag", "r1", "chalktest-frr-conf")
		if err != nil || strings.TrimSpace(out) != "root root 600" {
			t.Errorf("/run/frr/frr.conf on r1 is not root's with mode 0600: %v\n%s", err, out)
		}
	}

	// failedUnit tells whether the router's status out shows a failed unit, and logs the journal
	// of each one it shows.
	failedUnit := func(t *testing.T, name, inventory, out string) bool {
		t.Helper()
		units := regexp.MustCompile(`(?m)^failed unit (\S+)$`).FindAllStringSubmatch(out, -1)
		for _, u := range units {
			_, _ = chalkctl(t, nodes[name], inventory, "logs", name, "--unit", u[1])
		}
		return len(units) > 0
	}

	// failedJobs logs the journal of each unit whose job failed, as systemctl names it in the
	// output out of a delivery that failed, which may repeat it.
	failedJobs := func(t *testing.T, name, inventory, out string) {
		t.Helper()
		logged := map[string]bool{}
		for _, u := range regexp.MustCompile(`Job for (\S+) failed`).FindAllStringSubmatch(out, -1) {
			if !logged[u[1]] {
				logged[u[1]] = true
				_, _ = chalkctl(t, nodes[name], inventory, "logs", name, "--unit", u[1])
			}
		}
	}

	if !t.Run("bgp", func(t *testing.T) {
		waitFor(t, 10*time.Minute, "r1's session with r2", func() error { return established("r1", "fabric") })
		waitFor(t, 2*time.Minute, "r2's session with r1", func() error { return established("r2", "edge") })
		waitFor(t, 2*time.Minute, "r2's default route from r1", func() error { return learned("::/0") })
		waitFor(t, 2*time.Minute, "r2's route to r1's NAT64 prefix", func() error { return learned("64:ff9b::/96") })
		sessionLine := regexp.MustCompile(`(?m)^  default\s+fabric\s+enp0s3\s+Established\s+\S+\s+\d+\s+[1-9]\d*\s+up$`)
		if out, err := chalkctl(t, nodes["r1"], "base", "status", "r1"); err != nil || !sessionLine.MatchString(out) {
			t.Errorf("status of r1 shows no established session sending routes: %v\n%s", err, out)
		}
		defaultLine := regexp.MustCompile(`(?m)^::/0\s+\*\s+64512\s+enp0s3\s`)
		if out, err := chalkctl(t, nodes["r2"], "base", "node", "routes", "r2", "::/0"); err != nil || !defaultLine.MatchString(out) {
			t.Errorf("node routes of r2 shows no default route from r1: %v\n%s", err, out)
		}
		if out, err := chalkctl(t, nodes["r1"], "base", "node", "diag", "r1", "bgp-summary"); err != nil || !strings.Contains(out, "enp0s3") {
			t.Errorf("bgp-summary of r1 lists no session on enp0s3: %v\n%s", err, out)
		}
		rootsConfig(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, name := range []string{"r1", "r2"} {
			if out, err := chalkctl(t, nodes[name], "base", "status", name); err != nil || failedUnit(t, name, "base", out) {
				t.Errorf("status of %s shows a failed unit: %v\n%s", name, err, out)
			}
			// A reader sees the sessions, and the password nowhere.
			resp, err := routerOf(readers[name]).Status(ctx, connect.NewRequest(&routerv1.StatusRequest{}))
			if err != nil {
				t.Errorf("router status of %s for a reader: %v", name, err)
				continue
			}
			if len(resp.Msg.GetPeers()) == 0 {
				t.Errorf("router status of %s for a reader shows no BGP session: %v", name, resp.Msg)
			}
			if b, err := protojson.Marshal(resp.Msg); err != nil || strings.Contains(string(b), bgpPassword) {
				t.Errorf("router status of %s for a reader holds the BGP password: %v\n%s", name, err, b)
			}
			node, err := readers[name].Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				t.Errorf("status of %s for a reader: %v", name, err)
				continue
			}
			if b, err := protojson.Marshal(node.Msg); err != nil || strings.Contains(string(b), bgpPassword) {
				t.Errorf("status of %s for a reader holds the BGP password: %v\n%s", name, err, b)
			}
		}
		k := kubernetesv1connect.NewKubernetesServiceClient(conns["r1"].HTTPClient(), conns["r1"].BaseURL())
		if _, err := k.Bootstrap(ctx, connect.NewRequest(&kubernetesv1.BootstrapRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
			t.Errorf("Bootstrap on r1: %v, want unimplemented", err)
		}
	}) {
		return
	}

	if !t.Run("reload", func(t *testing.T) {
		before, err := session("r1", "fabric")
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if _, err := chalkctl(t, nodes["r1"], "announce", "apply-config", "r1"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 2*time.Minute, "r2's route to r1's edge0", func() error { return learned("2001:db8:ffff::1/128") })
		after, err := session("r1", "fabric")
		if err != nil {
			t.Fatal(err)
		}
		// A session that stayed up aged by the time the delivery took, give or take the whole
		// seconds FRR rounds its uptime to.
		if elapsed := time.Since(start); after.State != "Established" || after.UptimeSeconds+2 < before.UptimeSeconds+uint64(elapsed.Seconds()) {
			t.Errorf("the session restarted: up %ds before the delivery, %s and up %ds %v later", before.UptimeSeconds, after.State, after.UptimeSeconds, elapsed.Round(time.Second))
		}
		rootsConfig(t)
	}) {
		return
	}

	if !t.Run("nat64", func(t *testing.T) {
		// r1 answers r2's edge0, the source of r2's connections and queries, through BGP.
		waitFor(t, 2*time.Minute, "r1's route to r2's edge0", func() error { return learnedFrom("r1", "", "2001:db8:ffff::2/128", "64513") })
		out, err := chalkctl(t, nodes["r2"], "base", "node", "diag", "r2", "dns-query", "name=ipv4only.arpa", "type=AAAA", "server=2001:db8:ffff::1")
		if err != nil || !strings.Contains(out, "64:ff9b::c000:aa") || !strings.Contains(out, "64:ff9b::c000:ab") {
			t.Errorf("r2 got no synthesized AAAA records of ipv4only.arpa from r1's resolver: %v\n%s", err, out)
		}
		// QEMU's user-mode network gives r1 the host's loopback at 10.0.2.2, which the NAT64
		// prefix embeds as 64:ff9b::a00:202.
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		accepted := make(chan error, 1)
		go func() {
			c, err := l.Accept()
			if err == nil {
				c.Close()
			}
			accepted <- err
		}()
		port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
		out, err = chalkctl(t, nodes["r2"], "base", "node", "diag", "r2", "tcp-connect", "address=64:ff9b::a00:202", "port="+port)
		if err != nil || !strings.Contains(out, "connected to 64:ff9b::a00:202 port "+port) {
			t.Fatalf("r2 did not connect through r1's NAT64: %v\n%s", err, out)
		}
		select {
		case err := <-accepted:
			if err != nil {
				t.Fatalf("the host's listener: %v", err)
			}
		case <-time.After(time.Minute):
			t.Fatal("the host's listener saw no connection")
		}
		// Jool keeps the closed connection's session and binding for minutes. The empty pool4
		// takes the NAT64's inner address with the ports 61001-65535, 4535 of each protocol, which
		// r1 masquerades to enp0s2's address.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for role, c := range map[string]*client.Conn{"an admin": conns["r1"], "a reader": readers["r1"]} {
			resp, err := routerOf(c).Status(ctx, connect.NewRequest(&routerv1.StatusRequest{}))
			if err != nil {
				t.Errorf("router status of r1 for %s: %v", role, err)
				continue
			}
			r := resp.Msg
			edge := slices.ContainsFunc(r.GetNat64(), func(n *routerv1.NAT64Status) bool {
				return n.Name == "edge" && n.Sessions > 0 && n.Bindings > 0 && slices.EqualFunc(n.Pools, router.NAT64Protocols, func(p *routerv1.NAT64PoolStatus, protocol string) bool {
					return p.Protocol == protocol && p.Size == 4535
				})
			})
			if !edge || r.GetNat64Error() != "" || !r.GetResolver().GetRunning() {
				t.Errorf("status of r1 for %s shows no session and binding of its NAT64 or no running resolver: %v", role, r)
			}
		}
		statusLines := regexp.MustCompile(`(?m)^  edge\s+[1-9]\d*\s+[1-9]\d*\s+4535\s+4535\s+4535$[\s\S]*^resolver: running$`)
		if out, err := chalkctl(t, nodes["r1"], "base", "status", "r1"); err != nil || !statusLines.MatchString(out) || failedUnit(t, "r1", "base", out) {
			t.Errorf("status of r1 shows no session of its NAT64, no running resolver or a failed unit: %v\n%s", err, out)
		}
		if out, err := chalkctl(t, nodes["r1"], "base", "node", "diag", "r1", "nat64-sessions", "instance=edge"); err != nil || !strings.Contains(out, "10.0.2.2#"+port) {
			t.Errorf("nat64-sessions of r1 lists no session to the host's listener: %v\n%s", err, out)
		}
	}) {
		return
	}

	if !t.Run("node-ca", func(t *testing.T) {
		// The rotation writes the secrets file, so it runs on a copy; the OS CA stays, so the
		// test's client certificates keep working.
		t.Setenv("CHALKLAB_SECRETS", copySecrets(t, t.TempDir()))
		before, err := readSecrets()
		if err != nil {
			t.Fatal(err)
		}
		state, err := lab.StateDir(routerLabName)
		if err != nil {
			t.Fatal(err)
		}
		out, err := chalkctl(t, nil, "", "rotate", "node-ca", "--inventory", filepath.Join(state, "chalkos.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"r1 serves a certificate of the new node CA", "r2 serves a certificate of the new node CA", "the rotation of the node CA is finished"} {
			if !strings.Contains(out, want) {
				t.Errorf("chalkctl rotate node-ca does not say %q", want)
			}
		}
		after, err := readSecrets()
		if err != nil {
			t.Fatal(err)
		}
		if after.NodeCA.Certificate == before.NodeCA.Certificate || after.Rotation != nil {
			t.Fatalf("the secrets file: node CA changed %v, rotation %v", after.NodeCA.Certificate != before.NodeCA.Certificate, after.Rotation)
		}
		ca, err := pki.ParseCertificate([]byte(after.NodeCA.Certificate))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, name := range []string{"r1", "r2"} {
			// A new connection verifies the chain through the new node CA.
			c := dialNode(t, nodes[name], name, reader)
			defer c.Close()
			st, err := c.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				t.Errorf("status of %s: %v", name, err)
				continue
			}
			if !slices.ContainsFunc(st.Msg.Certificates, func(c *nodev1.CertificateStatus) bool { return c.Name == client.NameNode }) {
				t.Errorf("status of %s lists no node certificate", name)
			}
			for _, cert := range st.Msg.Certificates {
				if cert.Name == client.NameNode && (cert.Issuer != pki.Fingerprint(ca.Raw) || cert.Problem != "") {
					t.Errorf("%s serves a node certificate of issuer %s with problem %q, want the new node CA's and none", name, cert.Issuer, cert.Problem)
				}
			}
		}
	}) {
		return
	}

	if !t.Run("rollback", func(t *testing.T) {
		status := func() *nodev1.StatusResponse {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			st, err := conns["r1"].Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			return st.Msg
		}
		before := status()
		announced, previous := before.ConfigVersion, before.PreviousConfigVersion
		if previous == "" || previous == announced {
			t.Fatalf("r1 runs config %s and keeps %q as its previous one, want the config from before the reload", announced, previous)
		}
		// The node keeps the config it rolls back to, so no inventory gives it.
		out, err := chalkctl(t, nodes["r1"], "", "apply-config", "--rollback", "r1")
		if err != nil || !strings.Contains(out, "r1 runs config "+previous+" again; config "+announced+" is its previous one now") {
			t.Fatalf("apply-config --rollback: %v", err)
		}
		waitFor(t, 2*time.Minute, "r2 to lose r1's edge0", func() error {
			if learned("2001:db8:ffff::1/128") == nil {
				return errors.New("r2 still routes it through r1")
			}
			return nil
		})
		if after := status(); after.ConfigVersion != previous || after.PreviousConfigVersion != announced {
			t.Errorf("after the rollback r1 runs %s and keeps %s, want %s and %s", after.ConfigVersion, after.PreviousConfigVersion, previous, announced)
		}
		// A second rollback undoes the first.
		out, err = chalkctl(t, nodes["r1"], "", "apply-config", "--rollback", "r1")
		if err != nil || !strings.Contains(out, "r1 runs config "+announced+" again; config "+previous+" is its previous one now") {
			t.Fatalf("a second apply-config --rollback: %v", err)
		}
		waitFor(t, 2*time.Minute, "r2's route to r1's edge0", func() error { return learned("2001:db8:ffff::1/128") })
		rootsConfig(t)
	}) {
		return
	}

	if !t.Run("trial", func(t *testing.T) {
		// fresh is r1's status over a new connection, which proves that a client reaches r1: the
		// test's own connections stay up through a firewall change, as established ones do.
		fresh := func() (*nodev1.StatusResponse, error) {
			c, err := dialInstalled(nodes["r1"], "r1", admin)
			if err != nil {
				return nil, err
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			st, err := c.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				return nil, err
			}
			return st.Msg, nil
		}
		before, err := fresh()
		if err != nil {
			t.Fatal(err)
		}
		announced := before.ConfigVersion
		// A trial that leaves r1 reachable is confirmed: r1 runs the base inventory's config again,
		// without the address of edge0.
		out, err := chalkctl(t, nodes["r1"], "base", "apply-config", "--try", "1m", "r1")
		if err != nil || !regexp.MustCompile(`(?m)^r1 keeps config \S+: the trial is confirmed$`).MatchString(out) {
			t.Fatalf("apply-config --try: %v\n%s", err, out)
		}
		st, err := fresh()
		if err != nil || st.Trial != nil || st.TrialError != "" || st.ConfigVersion == announced || st.PreviousConfigVersion != announced {
			t.Fatalf("after a confirmed trial r1's status is %v, %v; want no trial and config %s as the previous one", st, err, announced)
		}
		base := st.ConfigVersion
		// A trial that accepts chalkd's port on enp0s3 alone cannot be confirmed: chalkctl's new
		// connections through enp0s2 fail, r1 rolls the config back at the deadline and answers new
		// connections again. The answer to the delivery may be lost as well, which ends the same.
		start := time.Now()
		out, err = chalkctl(t, nodes["r1"], "lockout", "apply-config", "--try", "30s", "r1")
		if err == nil || !strings.Contains(out, "r1: config ") || !strings.Contains(out, " was not confirmed within its trial: ") ||
			!strings.Contains(out, "r1 rolled back to config "+base+" at the trial's deadline\n") {
			t.Fatalf("a trial that cuts chalkctl off: %v\n%s", err, out)
		}
		if elapsed := time.Since(start); elapsed < 30*time.Second {
			t.Errorf("the trial ended after %v, before its deadline", elapsed.Round(time.Second))
		}
		st, err = fresh()
		if err != nil || st.ConfigVersion != base || st.Trial != nil || st.TrialError != "" || st.PreviousConfigVersion == base || st.PreviousConfigVersion == "" {
			t.Fatalf("after the trial's rollback r1's status is %v, %v; want config %s, no trial and the config that cut chalkctl off as the previous one", st, err, base)
		}
		rootsConfig(t)
	}) {
		return
	}

	if !t.Run("vrf", func(t *testing.T) {
		// A confirmed trial proves that chalkctl's new connections reach chalkd on enp0s2, in the
		// default VRF, once enp0s3 is in fabric. r1's BGP instance of r2's session moves from the
		// default VRF into fabric, and r2's only one with it.
		start := time.Now()
		for _, name := range []string{"r1", "r2"} {
			out, err := chalkctl(t, nodes[name], "vrf", "apply-config", "--try", "2m", name)
			if err != nil || !regexp.MustCompile(`(?m)^`+name+` keeps config \S+: the trial is confirmed$`).MatchString(out) {
				t.Fatalf("apply-config --try of %s's VRF: %v\n%s", name, err, out)
			}
		}
		waitFor(t, 2*time.Minute, "r1's session with r2 in fabric", func() error { return inFabric("r1", "fabric", start) })
		waitFor(t, 2*time.Minute, "r2's session with r1 in fabric", func() error { return inFabric("r2", "edge", start) })
		waitFor(t, 2*time.Minute, "r1's route to r2's edge0 in fabric", func() error { return learnedFrom("r1", "fabric", "2001:db8:ffff::2/128", "64513") })
		waitFor(t, 2*time.Minute, "r1's leaked route to r2's edge0", func() error { return leaked("2001:db8:ffff::2/128", "fabric") })
		// fabric's resolver runs bound to the VRF's device, which chalkos-resolvers started, and
		// answers a query from within fabric, which the test image's chalktest-fabric-dns sends
		// with the ID 0x1234.
		resolverRunning := func(inventory string) func() error {
			return func() error {
				out, err := chalkctl(t, nodes["r2"], inventory, "status", "r2")
				if err != nil {
					return err
				}
				if !regexp.MustCompile(`(?m)^resolver fabric: running$`).MatchString(out) {
					return fmt.Errorf("status of r2 shows no running resolver of fabric:\n%s", out)
				}
				out, err = chalkctl(t, nodes["r2"], inventory, "node", "diag", "r2", "chalktest-fabric-dns")
				if err != nil || !regexp.MustCompile(`^\s*12 34 8`).MatchString(out) {
					return fmt.Errorf("r2's resolver of fabric did not answer from within fabric: %v\n%s", err, out)
				}
				return nil
			}
		}
		waitFor(t, time.Minute, "r2's resolver of fabric", resolverRunning("vrf"))
		statusLine := regexp.MustCompile(`(?m)^  fabric\s+fabric\s+enp0s3\s+Established\s+\S+\s+\d+\s+[1-9]\d*\s+up$`)
		if out, err := chalkctl(t, nodes["r1"], "vrf", "status", "r1"); err != nil || !statusLine.MatchString(out) || failedUnit(t, "r1", "vrf", out) {
			t.Errorf("status of r1 shows no session in fabric, or a failed unit: %v\n%s", err, out)
		}
		if out, err := chalkctl(t, nodes["r2"], "vrf", "status", "r2"); err != nil || failedUnit(t, "r2", "vrf", out) {
			t.Errorf("status of r2 shows a failed unit: %v\n%s", err, out)
		}
		tableLine := regexp.MustCompile(`(?m)^2001:db8:ffff::2/128\s+unicast\s+bgp\s+100\s`)
		if out, err := chalkctl(t, nodes["r1"], "vrf", "node", "routes", "r1", "2001:db8:ffff::2/128", "--vrf", "fabric"); err != nil || !tableLine.MatchString(out) {
			t.Errorf("node routes --vrf fabric of r1 shows no route in table 100: %v\n%s", err, out)
		}
		// unreachable matches the unreachable default routes of both families that end a table.
		unreachable := func(table string) []*regexp.Regexp {
			return []*regexp.Regexp{
				regexp.MustCompile(`(?m)^0\.0\.0\.0/0\s+unreachable\s+static\s+` + table + `\s`),
				regexp.MustCompile(`(?m)^::/0\s+unreachable\s+static\s+` + table + `\s`),
			}
		}
		if out, err := chalkctl(t, nodes["r1"], "vrf", "node", "routes", "r1", "--vrf", "fabric", "--protocol", "static"); err != nil {
			t.Errorf("node routes --vrf fabric of r1: %v\n%s", err, out)
		} else {
			for _, re := range unreachable("100") {
				if !re.MatchString(out) {
					t.Errorf("node routes --vrf fabric of r1 shows no route matching %s:\n%s", re, out)
				}
			}
		}
		importedLine := regexp.MustCompile(`(?m)^2001:db8:ffff::2/128\s+\*\s+64513\s+vrf fabric\s`)
		if out, err := chalkctl(t, nodes["r1"], "vrf", "node", "routes", "r1", "2001:db8:ffff::2/128"); err != nil || !importedLine.MatchString(out) {
			t.Errorf("node routes of r1 shows no path imported from fabric: %v\n%s", err, out)
		}
		if out, err := chalkctl(t, nodes["r1"], "vrf", "node", "diag", "r1", "bgp-summary", "vrf=fabric"); err != nil || !strings.Contains(out, "enp0s3") {
			t.Errorf("bgp-summary vrf=fabric of r1 lists no session on enp0s3: %v\n%s", err, out)
		}
		rootsConfig(t)

		// policyHeld is nil once r1 holds what its PolicyRoute edge renders: networkd's rule per
		// target table, fabric's 100 and the next hop's 300, the next hop's table, and the
		// firewall's chain.
		policyHeld := func() error {
			diag := func(args ...string) (string, error) {
				return chalkctl(t, nodes["r1"], "vrf", append([]string{"node", "diag", "r1"}, args...)...)
			}
			out, err := diag("ip-rules", "family=inet6")
			if err != nil {
				return fmt.Errorf("ip-rules of r1: %w\n%s", err, out)
			}
			for _, want := range []string{
				`{"priority":900,"src":"all","fwmark":"0x10000","fwmask":"0xff0000","table":"100","protocol":"static"}`,
				`{"priority":900,"src":"all","fwmark":"0x20000","fwmask":"0xff0000","table":"300","protocol":"static"}`,
			} {
				if !strings.Contains(out, want) {
					return fmt.Errorf("ip-rules of r1 lacks %s:\n%s", want, out)
				}
			}
			out, err = diag("routes", "family=inet6", "table=300")
			if err != nil {
				return fmt.Errorf("routes of r1's table 300: %w\n%s", err, out)
			}
			for _, want := range []string{`"dst":"default","gateway":"2001:db8:fffe::2","dev":"policy0"`, `"type":"unreachable","dst":"default"`} {
				if !strings.Contains(out, want) {
					return fmt.Errorf("r1's table 300 lacks %s:\n%s", want, out)
				}
			}
			out, err = diag("nft-ruleset")
			if err != nil || !strings.Contains(out, "chain policy-routing {") {
				return fmt.Errorf("r1's ruleset holds no chain policy-routing: %v\n%s", err, out)
			}
			return nil
		}
		waitFor(t, time.Minute, "r1's policy routing", policyHeld)
		// A boot of r1 brings them back, with the session in fabric and no failed unit.
		start = time.Now()
		if out, err := chalkctl(t, nodes["r1"], "vrf", "reboot", "r1"); err != nil {
			t.Fatalf("reboot of r1: %v\n%s", err, out)
		}
		waitFor(t, 5*time.Minute, "r1's session with r2 in fabric after its boot", func() error { return inFabric("r1", "fabric", start) })
		waitFor(t, time.Minute, "r1's policy routing after its boot", policyHeld)
		if out, err := chalkctl(t, nodes["r1"], "vrf", "status", "r1"); err != nil || failedUnit(t, "r1", "vrf", out) {
			t.Errorf("status of r1 after its boot shows a failed unit: %v\n%s", err, out)
		}

		// networkd changes no running VRF's table, so chalkos-vrf removes r2's fabric, which
		// releases enp0s3 and edge0, and reloads networkd, which creates fabric in table 200 and
		// puts them back. The session restarts in fabric, and edge0's static address comes back.
		start = time.Now()
		if out, err := chalkctl(t, nodes["r2"], "vrf-table", "apply-config", "r2"); err != nil {
			failedJobs(t, "r2", "vrf-table", out)
			t.Fatalf("apply-config of r2's VRF in table 200: %v\n%s", err, out)
		}
		waitFor(t, 2*time.Minute, "r2's session with r1 in fabric of table 200", func() error { return inFabric("r2", "edge", start) })
		waitFor(t, 2*time.Minute, "r2's default route from r1 in fabric", func() error { return learnedFrom("r2", "fabric", "::/0", "64512") })
		waitFor(t, 2*time.Minute, "r2's route to edge0's address in table 200", func() error { return inTable("r2", "2001:db8:ffff::2/128", "edge0", 200) })
		waitFor(t, 2*time.Minute, "r1's route to r2's edge0 in fabric", func() error { return learnedFrom("r1", "fabric", "2001:db8:ffff::2/128", "64513") })
		// chalkos-resolvers, which runs after chalkos-vrf and networkd, restarted the resolver on
		// the device networkd created.
		waitFor(t, time.Minute, "r2's resolver of fabric in table 200", resolverRunning("vrf-table"))
		defaultLine := regexp.MustCompile(`(?m)^::/0\s+unicast\s+bgp\s+200\s`)
		if out, err := chalkctl(t, nodes["r2"], "vrf-table", "node", "routes", "r2", "::/0", "--vrf", "fabric"); err != nil || !defaultLine.MatchString(out) {
			t.Errorf("node routes --vrf fabric of r2 shows no default route in table 200: %v\n%s", err, out)
		}
		// chalkos-vrf flushes the old table's unreachable defaults before it removes the VRF.
		if out, err := chalkctl(t, nodes["r2"], "vrf-table", "node", "routes", "r2", "--table", "all", "--protocol", "static"); err != nil {
			t.Errorf("node routes --table all of r2: %v\n%s", err, out)
		} else {
			for _, re := range unreachable("200") {
				if !re.MatchString(out) {
					t.Errorf("node routes --table all of r2 shows no route matching %s:\n%s", re, out)
				}
			}
			for _, re := range unreachable("100") {
				if re.MatchString(out) {
					t.Errorf("node routes --table all of r2 still shows a route matching %s:\n%s", re, out)
				}
			}
		}
		if out, err := chalkctl(t, nodes["r2"], "vrf-table", "status", "r2"); err != nil || failedUnit(t, "r2", "vrf-table", out) {
			t.Errorf("status of r2 shows a failed unit: %v\n%s", err, out)
		}

		// The base inventory brings the sessions and edge0 back to the default VRF, and r2's BGP
		// instance with them. r1's policy routing goes: its rules, table 300's routes and the
		// chain. The dummy link policy0 stays, as networkd never deletes a netdev.
		for _, name := range []string{"r1", "r2"} {
			if out, err := chalkctl(t, nodes[name], "base", "apply-config", name); err != nil {
				t.Fatalf("apply-config of %s's base config: %v\n%s", name, err, out)
			}
		}
		waitFor(t, 2*time.Minute, "r1's session with r2 in the default VRF", func() error { return established("r1", "fabric") })
		waitFor(t, 2*time.Minute, "r2's session with r1 in the default VRF", func() error { return established("r2", "edge") })
		waitFor(t, 2*time.Minute, "r1's route to r2's edge0 in the default VRF", func() error { return learnedFrom("r1", "", "2001:db8:ffff::2/128", "64513") })
		waitFor(t, 2*time.Minute, "r2's default route from r1 in the default VRF", func() error { return learned("::/0") })
		waitFor(t, time.Minute, "r1's policy routing to go", func() error {
			out, err := chalkctl(t, nodes["r1"], "base", "node", "diag", "r1", "ip-rules", "family=inet6")
			if err != nil || strings.Contains(out, `"priority":900`) {
				return fmt.Errorf("ip-rules of r1 after the base config: %v\n%s", err, out)
			}
			for _, family := range []string{"inet", "inet6"} {
				out, err = chalkctl(t, nodes["r1"], "base", "node", "diag", "r1", "routes", "family="+family, "table=300")
				if err != nil || strings.Contains(out, `"dst":`) {
					return fmt.Errorf("routes of r1's table 300 after the base config: %v\n%s", err, out)
				}
			}
			out, err = chalkctl(t, nodes["r1"], "base", "node", "diag", "r1", "nft-ruleset")
			if err != nil || strings.Contains(out, "policy-routing") {
				return fmt.Errorf("r1's ruleset after the base config: %v\n%s", err, out)
			}
			return nil
		})
		for _, name := range []string{"r1", "r2"} {
			if out, err := chalkctl(t, nodes[name], "base", "status", name); err != nil || failedUnit(t, name, "base", out) {
				t.Errorf("status of %s shows a failed unit: %v\n%s", name, err, out)
			}
		}
	}) {
		return
	}

	if !t.Run("default", func(t *testing.T) {
		const tracked = "2001:db8:fff0::/48"
		// trackedInBGP is nil once r1's BGP holds the tracked route as its own, marked so that
		// no peer learns it, which shows that FRR runs the delivered config.
		trackedInBGP := func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resp, err := routerOf(conns["r1"]).Routes(ctx, connect.NewRequest(&routerv1.RoutesRequest{Prefix: tracked}))
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(resp.Msg.Bgp, func(b *routerv1.BGPRoute) bool {
				return b.Prefix == tracked && b.Best && b.Peer == "" && slices.Contains(b.Communities, "noAdvertise") && slices.Contains(b.Communities, "4200000000:0:1")
			}) {
				return fmt.Errorf("r1's BGP paths to %s: %v, BGP's error %q", tracked, resp.Msg.Bgp, resp.Msg.BgpError)
			}
			return nil
		}
		// r2LacksTracked is nil once r2 neither routes the tracked prefix nor holds a BGP path to
		// it.
		r2LacksTracked := func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resp, err := routerOf(conns["r2"]).Routes(ctx, connect.NewRequest(&routerv1.RoutesRequest{Prefix: tracked}))
			if err != nil {
				return err
			}
			if len(resp.Msg.Kernel) > 0 || len(resp.Msg.Bgp) > 0 {
				return fmt.Errorf("r2 learned %s: kernel %v, BGP %v", tracked, resp.Msg.Kernel, resp.Msg.Bgp)
			}
			return nil
		}
		// r1Tracked describes what r1 holds for the tracked prefix: its kernel routes, which
		// networkd keeps, and its BGP paths, which bgpd keeps.
		r1Tracked := func() string {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resp, err := routerOf(conns["r1"]).Routes(ctx, connect.NewRequest(&routerv1.RoutesRequest{Prefix: tracked}))
			if err != nil {
				return fmt.Sprintf("r1's routes to %s: %v", tracked, err)
			}
			return fmt.Sprintf("r1's kernel routes to %s: %v, BGP paths %v, BGP's error %q", tracked, resp.Msg.Kernel, resp.Msg.Bgp, resp.Msg.BgpError)
		}

		// r2Default is nil once a call to r2 succeeds and finds no default route, neither in its
		// kernel table nor among its BGP paths; errDefault means it found one.
		errDefault := errors.New("r2 holds a default route")
		r2Default := func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resp, err := routerOf(conns["r2"]).Routes(ctx, connect.NewRequest(&routerv1.RoutesRequest{Prefix: "::/0", Protocol: "bgp"}))
			if err != nil {
				return err
			}
			if slices.ContainsFunc(resp.Msg.Kernel, func(k *routerv1.KernelRoute) bool { return k.Destination == "::/0" }) ||
				slices.ContainsFunc(resp.Msg.Bgp, func(b *routerv1.BGPRoute) bool { return b.Prefix == "::/0" }) {
				return fmt.Errorf("%w: kernel %v, BGP %v; %s", errDefault, resp.Msg.Kernel, resp.Msg.Bgp, r1Tracked())
			}
			return nil
		}

		// r2 holds the unconditional default from the base config when the phase starts. It
		// loses it at once when r1's family gains defaultOriginateWhen, and FRR's scan, every
		// 5 s, must not send it again while r1 lacks the tracked route.
		if out, err := chalkctl(t, nodes["r1"], "default-gone", "apply-config", "r1"); err != nil {
			t.Fatalf("apply-config of r1's conditional default without the tracked route: %v\n%s", err, out)
		}
		waitFor(t, 2*time.Minute, "r2 to lose the default route", r2Default)
		for end := time.Now().Add(15 * time.Second); time.Now().Before(end); {
			time.Sleep(3 * time.Second)
			if err := r2Default(); errors.Is(err, errDefault) {
				t.Fatalf("r2's default route came back without the tracked route: %v", err)
			}
		}
		if err := trackedInBGP(); err == nil {
			t.Errorf("r1's BGP holds %s without its route", tracked)
		}
		if err := r2LacksTracked(); err != nil {
			t.Error(err)
		}
		if out, err := chalkctl(t, nodes["r1"], "default-gone", "status", "r1"); err != nil || failedUnit(t, "r1", "default-gone", out) {
			t.Errorf("status of r1 shows a failed unit: %v\n%s", err, out)
		}

		if out, err := chalkctl(t, nodes["r1"], "default", "apply-config", "r1"); err != nil {
			t.Fatalf("apply-config of r1's conditional default with the tracked route: %v\n%s", err, out)
		}
		waitFor(t, 2*time.Minute, "r1's tracked route in BGP", trackedInBGP)
		waitFor(t, 2*time.Minute, "r2's default route from r1 once the route exists", func() error {
			if err := learned("::/0"); err != nil {
				return fmt.Errorf("%w; %s", err, r1Tracked())
			}
			return nil
		})
		if err := r2LacksTracked(); err != nil {
			t.Error(err)
		}

		// The base inventory sends the default route whatever r1's table holds.
		if out, err := chalkctl(t, nodes["r1"], "base", "apply-config", "r1"); err != nil {
			t.Fatalf("apply-config of r1's base config: %v\n%s", err, out)
		}
		waitFor(t, 2*time.Minute, "r2's default route from r1 again", func() error { return learned("::/0") })
		rootsConfig(t)
	}) {
		return
	}

	t.Run("dhcp", func(t *testing.T) {
		// r1 first, so its server answers r2's first request.
		for _, name := range []string{"r1", "r2"} {
			if out, err := chalkctl(t, nodes[name], "dhcp", "apply-config", name); err != nil {
				failedJobs(t, name, "dhcp", out)
				t.Fatalf("apply-config of %s's DHCP: %v\n%s", name, err, out)
			}
		}
		// serving is nil once r1's status shows the server of lan running, with one lease of
		// lab's 100 pool addresses: r2's reserved one.
		serving := func() error {
			out, err := chalkctl(t, nodes["r1"], "dhcp", "status", "r1")
			if err != nil {
				return err
			}
			if !regexp.MustCompile(`(?m)^dhcp4 lan: running$`).MatchString(out) || !regexp.MustCompile(`(?m)^  lab\s+192\.0\.2\.0/24\s+1\s+100$`).MatchString(out) {
				return fmt.Errorf("status of r1 shows no running DHCP server of lan with r2's lease:\n%s", out)
			}
			return nil
		}
		// leased is nil once r2's dhcp0 holds 192.0.2.10/24, the address its MAC address reserves,
		// with the lifetime of a DHCP lease.
		leased := func() error {
			out, err := chalkctl(t, nodes["r2"], "dhcp", "node", "diag", "r2", "addresses")
			if err != nil {
				return err
			}
			if !regexp.MustCompile(`(?s)"ifname":"dhcp0".*?"local":"192\.0\.2\.10","prefixlen":24,[^}]*"dynamic":true`).MatchString(out) {
				return fmt.Errorf("r2's dhcp0 holds no address from DHCP:\n%s", out)
			}
			return nil
		}
		// leaseEnd is when r1's lease of r2's address ends, as the diagnostic dhcp-leases lists it.
		leaseEnd := func() (time.Time, error) {
			out, err := chalkctl(t, nodes["r1"], "dhcp", "node", "diag", "r1", "dhcp-leases", "mac=52:54:00:00:02:02")
			if err != nil {
				return time.Time{}, err
			}
			m := regexp.MustCompile(`(?m)^lan\tlab\t192\.0\.2\.10\t52:54:00:00:02:02\tr2\t(\S+)$`).FindStringSubmatch(out)
			if m == nil {
				return time.Time{}, fmt.Errorf("dhcp-leases of r1 lists no lease of r2's address:\n%s", out)
			}
			return time.Parse(time.RFC3339, m[1])
		}
		waitFor(t, 2*time.Minute, "r1's DHCP server of lan with r2's lease", serving)
		waitFor(t, 2*time.Minute, "r2's address from r1", leased)
		if _, err := leaseEnd(); err != nil {
			t.Fatal(err)
		}
		if out, err := chalkctl(t, nodes["r1"], "dhcp", "status", "r1"); err != nil || failedUnit(t, "r1", "dhcp", out) {
			t.Errorf("status of r1 shows a failed unit: %v\n%s", err, out)
		}

		// After a boot of r1, chalkos-dhcp starts the server once dhcp0 and lan exist, the lease
		// file kept r2's lease, and r2's next renewal, a minute into its two-minute lease, reaches
		// the server. A renewal before the boot ends at most two minutes after start; one after the
		// boot, which takes half a minute at least, ends later than 140 s after it.
		start := time.Now()
		if out, err := chalkctl(t, nodes["r1"], "dhcp", "reboot", "r1"); err != nil {
			t.Fatalf("reboot of r1: %v\n%s", err, out)
		}
		waitFor(t, 5*time.Minute, "r1's DHCP server of lan after its boot", serving)
		waitFor(t, 3*time.Minute, "r2's renewal at r1 after its boot", func() error {
			end, err := leaseEnd()
			if err != nil {
				return err
			}
			if !end.After(start.Add(140 * time.Second)) {
				return fmt.Errorf("r2's lease ends at %v, renewed before r1's boot at %v", end, start)
			}
			return nil
		})
		waitFor(t, time.Minute, "r2's address from r1 after r1's boot", leased)
		if out, err := chalkctl(t, nodes["r1"], "dhcp", "status", "r1"); err != nil || failedUnit(t, "r1", "dhcp", out) {
			t.Errorf("status of r1 after its boot shows a failed unit: %v\n%s", err, out)
		}

		// The base inventory stops the server; r2 first, so its client does not ask a server that
		// is gone.
		for _, name := range []string{"r2", "r1"} {
			if out, err := chalkctl(t, nodes[name], "base", "apply-config", name); err != nil {
				t.Fatalf("apply-config of %s's base config: %v\n%s", name, err, out)
			}
		}
		waitFor(t, time.Minute, "r1's DHCP server to stop", func() error {
			out, err := chalkctl(t, nodes["r1"], "base", "status", "r1")
			if err != nil || strings.Contains(out, "dhcp4 ") {
				return fmt.Errorf("status of r1 after the base config: %v\n%s", err, out)
			}
			return nil
		})
		for _, name := range []string{"r1", "r2"} {
			if out, err := chalkctl(t, nodes[name], "base", "status", name); err != nil || failedUnit(t, name, "base", out) {
				t.Errorf("status of %s shows a failed unit: %v\n%s", name, err, out)
			}
		}
	})
}
