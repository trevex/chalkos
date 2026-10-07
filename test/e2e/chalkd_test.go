package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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

// startNode boots a VM with a user-mode NIC that forwards a host port to chalkd, besides the
// forwards the configuration has.
func startNode(t *testing.T, c lab.VMConfig) *node {
	t.Helper()
	port, err := lab.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	c.Forwards = append([]lab.Forward{{Host: port, Guest: 50000}}, c.Forwards...)
	return &node{vm: bootVM(t, c), addr: fmt.Sprintf("127.0.0.1:%d", port)}
}

func secrets(t *testing.T) pki.Secrets {
	t.Helper()
	s, err := readSecrets()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func readSecrets() (pki.Secrets, error) {
	data, err := os.ReadFile(os.Getenv("CHALKLAB_SECRETS"))
	if err != nil {
		return pki.Secrets{}, err
	}
	return pki.ReadSecrets(data, nil)
}

// clientCertificate issues a client certificate of the role from the test OS CA.
func clientCertificate(t *testing.T, role string) *tls.Certificate {
	t.Helper()
	cert, err := issueClientCertificate(role)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func issueClientCertificate(role string) (*tls.Certificate, error) {
	s, err := readSecrets()
	if err != nil {
		return nil, err
	}
	ck := s.Admin
	if role != pki.RoleAdmin {
		if ck, err = pki.IssueClient(s.OSCA, role, role, time.Now()); err != nil {
			return nil, err
		}
	}
	pair, err := tls.X509KeyPair([]byte(ck.Certificate), []byte(ck.Key))
	if err != nil {
		return nil, err
	}
	return &pair, nil
}

// waitForChalkd polls chalkd until it answers Info, as a node in maintenance mode does once it
// booted, and returns the answer.
func waitForChalkd(t *testing.T, n *node, timeout time.Duration) *nodev1.InfoResponse {
	t.Helper()
	info, err := chalkdInfo(n, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func chalkdInfo(n *node, timeout time.Duration) (*nodev1.InfoResponse, error) {
	cert, err := issueClientCertificate(pki.RoleAdmin)
	if err != nil {
		return nil, err
	}
	c, err := client.Dial(n.addr, client.Options{Insecure: true, Certificate: cert})
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := c.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
		cancel()
		if err == nil {
			return resp.Msg, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("chalkd at %s did not answer: %w", n.addr, err)
		}
		time.Sleep(2 * time.Second)
	}
}

// chalkctl runs the chalkctl binary for the node, or for the cluster when n is nil, against the
// test cluster, with the named manifest of CHALKLAB_MANIFESTS.
func chalkctl(t *testing.T, n *node, manifest string, args ...string) (string, error) {
	t.Helper()
	args = append(args,
		"--manifest", filepath.Join(os.Getenv("CHALKLAB_MANIFESTS"), manifest+".json"),
		"--secrets", os.Getenv("CHALKLAB_SECRETS"))
	if n != nil {
		args = append(args, "--endpoint", n.addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, os.Getenv("CHALKLAB_CHALKCTL"), args...).CombinedOutput()
	t.Logf("chalkctl %s:\n%s", strings.Join(args, " "), out)
	return string(out), err
}

// installInPlace waits for the node's maintenance boot and installs it in place.
func installInPlace(t *testing.T, n *node, name string) {
	t.Helper()
	if err := install(t, n, name); err != nil {
		t.Fatal(err)
	}
}

// install is installInPlace for other goroutines than the test's: it only logs to t.
func install(t *testing.T, n *node, name string) error {
	return installFrom(t, n, name, "base")
}

// installFrom installs the node as the named manifest of CHALKLAB_MANIFESTS defines it.
func installFrom(t *testing.T, n *node, name, manifest string) error {
	info, err := chalkdInfo(n, 5*time.Minute)
	if err != nil {
		return err
	}
	if info.Mode != nodev1.Mode_MODE_MAINTENANCE {
		return fmt.Errorf("node %s is in mode %v, want maintenance", name, info.Mode)
	}
	if _, err := chalkctl(t, n, manifest, "install", name, "--insecure"); err != nil {
		return fmt.Errorf("install %s: %w", name, err)
	}
	return nil
}

var fingerprintRE = regexp.MustCompile(`certificate fingerprint ([0-9a-f]{64})`)

// dialNode connects to an installed node as a client with the certificate, verifying the node
// by the test OS CA and its name.
func dialNode(t *testing.T, n *node, name string, cert *tls.Certificate) *client.Conn {
	t.Helper()
	c, err := dialInstalled(n, name, cert)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func dialInstalled(n *node, name string, cert *tls.Certificate) (*client.Conn, error) {
	s, err := readSecrets()
	if err != nil {
		return nil, err
	}
	ca, err := pki.ParseCertificate([]byte(s.OSCA.Certificate))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return client.Dial(n.addr, client.Options{CA: pool, ServerName: name, Certificate: cert})
}

func info(c *client.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := c.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
	return err
}

// waitForNode waits until the installed node serves its node certificate.
func waitForNode(t *testing.T, n *node, name string) {
	t.Helper()
	if err := installedNodeAnswers(n, name); err != nil {
		t.Fatal(err)
	}
}

func installedNodeAnswers(n *node, name string) error {
	cert, err := issueClientCertificate(pki.RoleAdmin)
	if err != nil {
		return err
	}
	c, err := dialInstalled(n, name, cert)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err := info(c)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the installed node %s did not answer: %w", name, err)
		}
		time.Sleep(2 * time.Second)
	}
}
