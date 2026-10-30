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
//     r1's NAT64 and QEMU's user-mode network to a listener of the test on the host. r1's status,
//     for an admin and for a reader, shows the connection's session and binding and its resolver
//     running, the nat64-sessions diagnostic shows the session, and no unit of r1 failed.
//   - node-ca: chalkctl rotate node-ca over the lab's inventory, on a copy of the secrets file,
//     gives r1 and r2 certificates of the new node CA, which a new connection verifies, and
//     their status reports the node certificate without a problem.
//   - rollback: apply-config --rollback, without an inventory, brings r1 back to the config it
//     ran before the reload, so r2 loses the route to r1's edge0, and a second rollback brings
//     the route back; frr.conf stays root's with mode 0600.
//   - trial: apply-config --try with the base inventory is confirmed over a new connection; one
//     that accepts chalkd's port on enp0s3 alone cuts the host off, so chalkctl cannot confirm it,
//     r1 rolls it back at the 30 s deadline and new connections reach it again.
//
// Two VMs of 768 MiB and 2 CPUs each; about 85 s once chalklab installed both, a few seconds for
// the node CA, about 10 s for the rollbacks and 45 s for the trials, about 6 minutes with the
// copies of the images. chalklab destroy removes the lab when the test ends.
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
		if p.State != "Established" || p.Bfd != "up" {
			return fmt.Errorf("the session of %s on %s is %s, its BFD %q", peer, name, p.State, p.Bfd)
		}
		return nil
	}
	// learnedFrom is nil once the router name routes the prefix through its peer of the AS as: zebra
	// installed it over enp0s3 to an IPv6 link-local next hop, and BGP's best path came from as on
	// enp0s3.
	learnedFrom := func(name, prefix, as string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := routerOf(conns[name]).Routes(ctx, connect.NewRequest(&routerv1.RoutesRequest{Prefix: prefix, Protocol: "bgp"}))
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
			return fmt.Errorf("%s's routes to %s: kernel %v, BGP %v, BGP's error %q", name, prefix, resp.Msg.Kernel, resp.Msg.Bgp, resp.Msg.BgpError)
		}
		return nil
	}
	// learned is nil once r2 routes the prefix through r1.
	learned := func(prefix string) error { return learnedFrom("r2", prefix, "64512") }
	// rootsConfig checks that r1's filled frr.conf, which holds the password, is root's alone, as
	// the test image's chalktest-frr-conf diagnostic shows it.
	rootsConfig := func(t *testing.T) {
		t.Helper()
		out, err := chalkctl(t, nodes["r1"], "base", "node", "diag", "r1", "chalktest-frr-conf")
		if err != nil || strings.TrimSpace(out) != "root root 600" {
			t.Errorf("/run/frr/frr.conf on r1 is not root's with mode 0600: %v\n%s", err, out)
		}
	}

	if !t.Run("bgp", func(t *testing.T) {
		waitFor(t, 10*time.Minute, "r1's session with r2", func() error { return established("r1", "fabric") })
		waitFor(t, 2*time.Minute, "r2's session with r1", func() error { return established("r2", "edge") })
		waitFor(t, 2*time.Minute, "r2's default route from r1", func() error { return learned("::/0") })
		waitFor(t, 2*time.Minute, "r2's route to r1's NAT64 prefix", func() error { return learned("64:ff9b::/96") })
		sessionLine := regexp.MustCompile(`(?m)^  fabric\s+enp0s3\s+Established\s+\S+\s+\d+\s+[1-9]\d*\s+up$`)
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
			if out, err := chalkctl(t, nodes[name], "base", "status", name); err != nil || strings.Contains(out, "failed unit") {
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
		waitFor(t, 2*time.Minute, "r1's route to r2's edge0", func() error { return learnedFrom("r1", "2001:db8:ffff::2/128", "64513") })
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
		// Jool keeps the closed connection's session and binding for minutes. 10.0.2.15/32 with
		// the ports 61001-65535 holds 4535 of each protocol.
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
		if out, err := chalkctl(t, nodes["r1"], "base", "status", "r1"); err != nil || !statusLines.MatchString(out) || strings.Contains(out, "failed unit") {
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

	t.Run("trial", func(t *testing.T) {
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
			t.Fatalf("apply-config --try: %v", err)
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
			t.Fatalf("a trial that cuts chalkctl off: %v", err)
		}
		if elapsed := time.Since(start); elapsed < 30*time.Second {
			t.Errorf("the trial ended after %v, before its deadline", elapsed.Round(time.Second))
		}
		st, err = fresh()
		if err != nil || st.ConfigVersion != base || st.Trial != nil || st.TrialError != "" || st.PreviousConfigVersion == base || st.PreviousConfigVersion == "" {
			t.Fatalf("after the trial's rollback r1's status is %v, %v; want config %s, no trial and the config that cut chalkctl off as the previous one", st, err, base)
		}
		rootsConfig(t)
	})
}
