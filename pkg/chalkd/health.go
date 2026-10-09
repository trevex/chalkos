package chalkd

import (
	"bytes"
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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/install"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/storage/node"
	"github.com/trevex/chalkos/pkg/uki"
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
	// IgnoreUnits are units whose failure leaves a node without Kubernetes healthy.
	IgnoreUnits []string
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

// chalkdServes checks that chalkd completes a TLS handshake with the node's certificate: the node
// is reachable and managed. The check presents that certificate too, so chalkd does not log a
// refused handshake every few seconds.
func (h *Health) chalkdServes(ctx context.Context) error {
	data, err := os.ReadFile(h.NodeCertificate)
	if err != nil {
		return fmt.Errorf("read the node certificate: %w", err)
	}
	cert, err := tls.X509KeyPair(data, data)
	if err != nil {
		return fmt.Errorf("the node certificate: %w", err)
	}
	cfg := &tls.Config{
		// The certificate is compared with the node's own instead.
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{cert},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !bytes.Equal(cs.PeerCertificates[0].Raw, cert.Certificate[0]) {
				return errors.New("the server does not present the node's certificate")
			}
			return nil
		},
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: cfg}
	conn, err := d.DialContext(ctx, "tcp", h.Chalkd)
	if err != nil {
		return fmt.Errorf("chalkd does not serve: %w", err)
	}
	return conn.Close()
}

// applied checks that the node's identity was applied and that no unit of the boot failed: none
// that multi-user.target or sysinit.target pull in, or what those need. Jobs a timer or a socket
// started, which may fail at any time, do not count, nor the units the image ignores.
func (h *Health) applied(ctx context.Context) error {
	if _, err := h.Run.Run(ctx, "systemctl", "is-active", "--quiet", "chalkos-identity.service"); err != nil {
		return errors.New("the identity was not applied")
	}
	out, err := h.Run.Run(ctx, "systemctl", "list-units", "--state=failed", "--plain", "--no-legend", "--no-pager")
	if err != nil {
		return fmt.Errorf("list failed units: %w", err)
	}
	failedUnits := unitNames(out)
	if len(failedUnits) == 0 {
		return nil
	}
	out, err = h.Run.Run(ctx, "systemctl", "list-dependencies", "--all", "--plain", "--no-legend", "--no-pager", "multi-user.target", "sysinit.target")
	if err != nil {
		return fmt.Errorf("list the units of the boot: %w", err)
	}
	boot := unitNames(out)
	failedUnits = slices.DeleteFunc(failedUnits, func(u string) bool {
		return !slices.Contains(boot, u) || slices.Contains(h.IgnoreUnits, u)
	})
	if len(failedUnits) > 0 {
		return fmt.Errorf("units failed: %s", strings.Join(failedUnits, ", "))
	}
	return nil
}

// unitNames reads the unit names of systemctl's lines: the first word naming a unit, past any
// mark of its state.
func unitNames(out []byte) []string {
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		for _, f := range strings.Fields(line) {
			if strings.Contains(f, ".") {
				names = append(names, f)
				break
			}
		}
	}
	return names
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
// try of this image, or to the image it falls back to. The checks end at the timeout, so Wait
// decides before systemd's own timeout of the health check stops it.
func (h *Health) Wait(ctx context.Context, timeout time.Duration) error {
	interval := h.Interval
	if interval == 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	checks, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var last string
	for {
		err := h.Check(checks)
		if err == nil {
			log.Print("the node is healthy")
			h.forgetFailedBoot()
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
	return h.unhealthy(ctx, err)
}

// Stopped handles the end of a health check that did not decide, as when systemd stopped it at
// its unit's timeout or it crashed: result and status are how the check ended, as systemd tells
// ExecStopPost= in $SERVICE_RESULT and $EXIT_STATUS. Like Wait after an unhealthy boot, it
// records the boot and reboots only when that leads the boot loader on, so a boot with nothing to
// fall back to is never rebooted in a loop. A check that succeeded, or failed with status 1,
// decided by itself.
func (h *Health) Stopped(ctx context.Context, result, status string) error {
	if result == "" || result == "success" || result == "exit-code" && status == "1" {
		return nil
	}
	return h.unhealthy(ctx, fmt.Errorf("the health check ended before it decided: %s, status %s", result, status))
}

// unhealthy records a boot that was not found healthy and reboots the node when that leads the
// boot loader on.
func (h *Health) unhealthy(ctx context.Context, err error) error {
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

// booted finds the entry the node booted and the root hash of the store it runs.
func (h *Health) booted() (upgrade.Entry, []byte, error) {
	cmdline, err := os.ReadFile(h.Cmdline)
	if err != nil {
		return upgrade.Entry{}, nil, err
	}
	running, err := uki.UsrHash(string(cmdline))
	if err != nil {
		return upgrade.Entry{}, nil, fmt.Errorf("the running store: %w", err)
	}
	entries, err := upgrade.Entries(h.ESP)
	if err != nil {
		return upgrade.Entry{}, nil, err
	}
	booted, err := upgrade.Booted(entries, h.EFIVars, running)
	return booted, running, err
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

// forgetFailedBoot removes the record of a failed boot once a boot the loader counts was found
// healthy: the node boots that image for good, and the record would describe an image it tried
// before.
func (h *Health) forgetFailedBoot() {
	booted, _, err := h.booted()
	if err != nil || booted.TriesLeft < 0 {
		return
	}
	if err := os.Remove(h.Record); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("remove the record of a failed boot: %v", err)
	}
}

// recordLines is how much of the boot's journal a failed boot keeps.
const recordLines = 30

// record keeps the journal lines chalkd and the health check wrote during this boot, with the
// failure, in a file on VAR, where the image the node falls back to finds them. The journal does
// not serve: the read-only image makes a new machine ID at every boot, under which the journal of
// a boot is kept apart from the others. The record names the version and the store of the boot.
func (h *Health) record(ctx context.Context, failure error) {
	var running []byte
	if cmdline, err := os.ReadFile(h.Cmdline); err == nil {
		running, _ = uki.UsrHash(string(cmdline))
	}
	out, err := h.Run.Run(ctx, "journalctl", "--boot", "--no-pager", "--output=short-iso", "--lines="+strconv.Itoa(recordLines), "--unit=chalkd.service", "--unit=chalkos-health.service")
	text := recordHeader(h.Version, running) + "\n" + string(out)
	if err != nil {
		text += fmt.Sprintf("the journal could not be read: %v\n", err)
	}
	text += failure.Error() + "\n"
	if err := install.WriteFile(h.Record, []byte(text), 0o600); err != nil {
		log.Printf("record the failed boot: %v", err)
	}
}
