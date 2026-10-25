package e2e

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/pki"
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
//     reader no password, r2 learns the default route r1 originates, in its kernel and in BGP, the
//     bgp-summary diagnostic lists the session, the filled frr.conf is root's with mode 0600, no
//     unit failed, and r1, an image without the Kubernetes layer, answers Bootstrap with
//     Unimplemented.
//   - reload: a delivery that has r1 announce the address of its edge0 reaches r2 while the
//     session stays up: FRR reloads, its password filled in again into a frr.conf that is still
//     root's with mode 0600, and restarts nothing.
//
// Two VMs of 768 MiB and 2 CPUs each; about 80 s once chalklab installed both, about 5 minutes
// with the copies of the images. chalklab destroy removes the lab when the test ends.
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
	// session is the router's session of the BGPPeer on enp0s3, as its status reports it.
	session := func(name, peer string) (*nodev1.BGPPeerStatus, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := conns[name].Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
		if err != nil {
			return nil, err
		}
		r := resp.Msg.Router
		switch {
		case r == nil:
			return nil, errors.New("the status has no router part")
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
	// learned is nil once r2 routes the prefix through r1: zebra installed it over enp0s3 to an
	// IPv6 link-local next hop, and BGP's best path came from r1's AS on enp0s3.
	learned := func(prefix string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resp, err := conns["r2"].Routes(ctx, connect.NewRequest(&nodev1.RoutesRequest{Prefix: prefix, Protocol: "bgp"}))
		if err != nil {
			return err
		}
		kernel := slices.ContainsFunc(resp.Msg.Kernel, func(k *nodev1.KernelRoute) bool {
			return k.Destination == prefix && slices.ContainsFunc(k.NextHops, func(h *nodev1.NextHop) bool {
				return h.Interface == "enp0s3" && strings.HasPrefix(h.Gateway, "fe80:")
			})
		})
		bgp := slices.ContainsFunc(resp.Msg.Bgp, func(b *nodev1.BGPRoute) bool {
			return b.Prefix == prefix && b.Best && b.AsPath == "64512" && b.Peer == "enp0s3"
		})
		if !kernel || !bgp {
			return fmt.Errorf("r2's routes to %s: kernel %v, BGP %v, BGP's error %q", prefix, resp.Msg.Kernel, resp.Msg.Bgp, resp.Msg.BgpError)
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

	if !t.Run("bgp", func(t *testing.T) {
		waitFor(t, 10*time.Minute, "r1's session with r2", func() error { return established("r1", "fabric") })
		waitFor(t, 2*time.Minute, "r2's session with r1", func() error { return established("r2", "edge") })
		waitFor(t, 2*time.Minute, "r2's default route from r1", func() error { return learned("::/0") })
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
			resp, err := readers[name].Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				t.Errorf("status of %s for a reader: %v", name, err)
				continue
			}
			if len(resp.Msg.GetRouter().GetPeers()) == 0 {
				t.Errorf("status of %s for a reader shows no BGP session: %v", name, resp.Msg.Router)
			}
			if b, err := protojson.Marshal(resp.Msg); err != nil || strings.Contains(string(b), bgpPassword) {
				t.Errorf("status of %s for a reader holds the BGP password: %v\n%s", name, err, b)
			}
		}
		if _, err := conns["r1"].Bootstrap(ctx, connect.NewRequest(&nodev1.BootstrapRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
			t.Errorf("Bootstrap on r1: %v, want unimplemented", err)
		}
	}) {
		return
	}

	t.Run("reload", func(t *testing.T) {
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
	})
}
