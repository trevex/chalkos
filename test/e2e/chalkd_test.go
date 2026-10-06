package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/lab"
	"github.com/trevex/chalkos/pkg/pki"
)

// chalkdEnv is what tests that talk to chalkd need besides the firmware and an image.
var chalkdEnv = []string{"CHALKLAB_CHALKCTL", "CHALKLAB_SECRETS", "CHALKLAB_MANIFESTS"}

// node is a VM whose chalkd is reachable from the host.
type node struct {
	vm   *lab.VM
	addr string
}

// startNode boots a VM with a user-mode NIC that forwards a host port to chalkd.
func startNode(t *testing.T, c lab.VMConfig) *node {
	t.Helper()
	port, err := lab.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	c.Forwards = []lab.Forward{{Host: port, Guest: 50000}}
	return &node{vm: bootVM(t, c), addr: fmt.Sprintf("127.0.0.1:%d", port)}
}

func secrets(t *testing.T) pki.Secrets {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("CHALKLAB_SECRETS"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := pki.ReadSecrets(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// clientCertificate issues a client certificate of the role from the test OS CA.
func clientCertificate(t *testing.T, role string) *tls.Certificate {
	t.Helper()
	s := secrets(t)
	ck := s.Admin
	if role != pki.RoleAdmin {
		var err error
		if ck, err = pki.IssueClient(s.OSCA, role, role, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	pair, err := tls.X509KeyPair([]byte(ck.Certificate), []byte(ck.Key))
	if err != nil {
		t.Fatal(err)
	}
	return &pair
}

// waitForChalkd polls chalkd until it answers Info, as a node in maintenance mode does once it
// booted, and returns the answer.
func waitForChalkd(t *testing.T, n *node, timeout time.Duration) *nodev1.InfoResponse {
	t.Helper()
	c, err := client.Dial(n.addr, client.Options{Insecure: true, Certificate: clientCertificate(t, pki.RoleAdmin)})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := c.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
		cancel()
		if err == nil {
			return resp.Msg
		}
		if time.Now().After(deadline) {
			t.Fatalf("chalkd at %s did not answer: %v", n.addr, err)
		}
		time.Sleep(2 * time.Second)
	}
}

// chalkctl runs the chalkctl binary for the node against the test cluster, with the named
// manifest of CHALKLAB_MANIFESTS.
func chalkctl(t *testing.T, n *node, manifest string, args ...string) (string, error) {
	t.Helper()
	args = append(args,
		"--manifest", filepath.Join(os.Getenv("CHALKLAB_MANIFESTS"), manifest+".json"),
		"--secrets", os.Getenv("CHALKLAB_SECRETS"),
		"--endpoint", n.addr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, os.Getenv("CHALKLAB_CHALKCTL"), args...).CombinedOutput()
	t.Logf("chalkctl %s:\n%s", strings.Join(args, " "), out)
	return string(out), err
}

// installInPlace waits for the node's maintenance boot and installs it in place.
func installInPlace(t *testing.T, n *node, name string) {
	t.Helper()
	if info := waitForChalkd(t, n, 5*time.Minute); info.Mode != nodev1.Mode_MODE_MAINTENANCE {
		t.Fatalf("node is in mode %v, want maintenance", info.Mode)
	}
	if _, err := chalkctl(t, n, "base", "install", name, "--insecure"); err != nil {
		t.Fatalf("install %s: %v", name, err)
	}
}

var fingerprintRE = regexp.MustCompile(`certificate fingerprint ([0-9a-f]{64})`)
