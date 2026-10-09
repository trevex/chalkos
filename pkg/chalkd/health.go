package chalkd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/install"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// Health decides whether a boot of the node is healthy, which a boot of a new image must be to
// be blessed: chalkd serves, and then, by the node's part in Kubernetes,
//
//   - a control plane: its etcd member is healthy, its API server ready and its kubelet healthy;
//   - a worker: its kubelet is healthy and its Node registered;
//   - a node without Kubernetes, or one not part of a cluster yet: its identity was applied and
//     no unit failed.
type Health struct {
	Run node.Runner
	// Kubernetes is the node's Kubernetes side; nil on a node of a role without Kubernetes.
	Kubernetes *Kubernetes
	// Chalkd is where chalkd serves, and Kubelet the kubelet's health endpoint.
	Chalkd, Kubelet string
	// NodeCertificate is the file of the node's certificate and key, which the check presents to
	// chalkd: a handshake without a client certificate would fill its log with refusals.
	NodeCertificate string
	// ESP, EFIVars and Cmdline are what the reboot after an unhealthy boot reads.
	ESP, EFIVars, Cmdline string
	// Version is the running image's, and Record the file the journal of a boot that was not found
	// healthy is kept in.
	Version, Record string
	// Interval is the time between two checks while waiting; zero is five seconds.
	Interval time.Duration
}

// DefaultHealth checks the node's own services.
func DefaultHealth(k *Kubernetes) *Health {
	paths := DefaultPaths()
	return &Health{
		Run:             node.ExecRunner{},
		Kubernetes:      k,
		Chalkd:          "127.0.0.1:50000",
		NodeCertificate: filepath.Join(paths.StateDir, "chalkd", NodeCertificateFile),
		Kubelet:         "http://127.0.0.1:10248/healthz",
		ESP:             paths.ESP,
		EFIVars:         paths.EFIVars,
		Cmdline:         paths.Cmdline,
		Version:         readOSRelease(paths.OSRelease)["IMAGE_VERSION"],
		Record:          paths.FailedBoot,
	}
}

// Check returns nil when the node is healthy, or what is not.
func (h *Health) Check(ctx context.Context) error {
	if err := h.chalkdServes(ctx); err != nil {
		return err
	}
	k := h.Kubernetes
	if k == nil {
		return h.applied(ctx)
	}
	if _, err := os.Stat(k.Paths.Share()); errors.Is(err, fs.ErrNotExist) {
		return h.applied(ctx)
	}
	switch problem, err := preparation(k.Paths); {
	case err != nil:
		return err
	case problem != "":
		return errors.New(problem)
	}
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return err
	}
	if c.Kind == k8s.KindWorker {
		if err := h.kubeletHealthy(ctx); err != nil {
			return err
		}
		if _, err := k.Node(ctx); err != nil {
			return fmt.Errorf("the Node is not registered: %w", err)
		}
		return nil
	}
	// A control plane that is no etcd member yet waits for a bootstrap or a cluster to join.
	if bootstrapped, err := knode.Bootstrapped(k.Paths); err != nil {
		return err
	} else if !bootstrapped {
		return h.applied(ctx)
	}
	share, err := knode.ReadShare(k.Paths)
	if err != nil {
		return err
	}
	if err := h.etcdHealthy(ctx, c, share); err != nil {
		return err
	}
	if !k.APIServerReady(ctx, c, share) {
		return errors.New("the API server is not ready")
	}
	return h.kubeletHealthy(ctx)
}

// chalkdServes checks that chalkd completes a TLS handshake: the node is reachable and managed.
// chalkd is not verified; the check only looks for it to answer.
func (h *Health) chalkdServes(ctx context.Context) error {
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}
	if data, err := os.ReadFile(h.NodeCertificate); err == nil {
		if cert, err := tls.X509KeyPair(data, data); err == nil {
			cfg.Certificates = []tls.Certificate{cert}
		}
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: cfg}
	conn, err := d.DialContext(ctx, "tcp", h.Chalkd)
	if err != nil {
		return fmt.Errorf("chalkd does not serve: %w", err)
	}
	return conn.Close()
}

// applied checks that the node's identity was applied and that no unit failed.
func (h *Health) applied(ctx context.Context) error {
	if _, err := h.Run.Run(ctx, "systemctl", "is-active", "--quiet", "chalkos-identity.service"); err != nil {
		return errors.New("the identity was not applied")
	}
	out, err := h.Run.Run(ctx, "systemctl", "list-units", "--state=failed", "--plain", "--no-legend", "--no-pager")
	if err != nil {
		return fmt.Errorf("list failed units: %w", err)
	}
	var failedUnits []string
	for _, line := range strings.Split(string(out), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			failedUnits = append(failedUnits, fields[0])
		}
	}
	if len(failedUnits) > 0 {
		return fmt.Errorf("units failed: %s", strings.Join(failedUnits, ", "))
	}
	return nil
}

func (h *Health) kubeletHealthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.Kubelet, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("the kubelet is not healthy: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the kubelet is not healthy: %s", resp.Status)
	}
	return nil
}

// etcdHealthy checks that the node's own etcd member answers.
func (h *Health) etcdHealthy(ctx context.Context, c k8s.Cluster, share kpki.Share) error {
	k := h.Kubernetes
	cli, err := k.dialEtcd(c, share)
	if err != nil {
		return fmt.Errorf("etcd: %w", err)
	}
	defer cli.Close()
	ctx, cancel := k.etcdRequest(ctx)
	defer cancel()
	own, err := etcd.Local(ctx, cli, k.localEtcd(c))
	if err != nil {
		return fmt.Errorf("the etcd member is not healthy: %w", err)
	}
	if err := etcd.Health(ctx, cli, []etcd.Member{own})[own.ID]; err != nil {
		return fmt.Errorf("the etcd member is not healthy: %w", err)
	}
	return nil
}

// Wait checks until the node is healthy or the timeout passed. After an unhealthy boot it reboots
// the node, but only while the boot loader counts this boot and a reboot leads it on: to another
// try of this image, or to the image it falls back to.
func (h *Health) Wait(ctx context.Context, timeout time.Duration) error {
	interval := h.Interval
	if interval == 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var last string
	for {
		err := h.Check(ctx)
		if err == nil {
			log.Print("the node is healthy")
			return nil
		}
		if err.Error() != last {
			last = err.Error()
			log.Printf("not healthy yet: %v", err)
		}
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(interval, time.Until(deadline))):
		}
	}
	err := fmt.Errorf("the node did not become healthy within %v: %s", timeout, last)
	h.record(ctx, err)
	reboot, why := h.rebootHelps()
	if !reboot {
		log.Printf("%v; not rebooting: %s", err, why)
		return err
	}
	log.Printf("%v; rebooting: %s", err, why)
	if _, rerr := h.Run.Run(ctx, "systemctl", "reboot", "--no-block"); rerr != nil {
		return fmt.Errorf("%w; the reboot failed: %v", err, rerr)
	}
	return err
}

// rebootHelps says whether a reboot after an unhealthy boot leads the boot loader on, and why.
func (h *Health) rebootHelps() (bool, string) {
	entries, err := upgrade.Entries(h.ESP)
	if err != nil {
		return false, err.Error()
	}
	cmdline, err := os.ReadFile(h.Cmdline)
	if err != nil {
		return false, err.Error()
	}
	return upgrade.RebootHelps(entries, h.EFIVars, string(cmdline))
}

// recordLines is how much of the boot's journal a failed boot keeps.
const recordLines = 30

// record keeps the journal lines chalkd and the health check wrote during this boot, with the
// failure, in a file on VAR, where the image the node falls back to finds them. The journal does
// not serve: the read-only image makes a new machine ID at every boot, under which the journal of
// a boot is kept apart from the others.
func (h *Health) record(ctx context.Context, failure error) {
	out, err := h.Run.Run(ctx, "journalctl", "--boot", "--no-pager", "--output=short-iso", "--lines="+strconv.Itoa(recordLines), "--unit=chalkd.service", "--unit=chalkos-health.service")
	text := "version " + h.Version + "\n" + string(out)
	if err != nil {
		text += fmt.Sprintf("the journal could not be read: %v\n", err)
	}
	text += failure.Error() + "\n"
	if err := install.WriteFile(h.Record, []byte(text), 0o600); err != nil {
		log.Printf("record the failed boot: %v", err)
	}
}
