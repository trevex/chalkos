package chalkd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log"
	"net/http"
	"time"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/vip"
)

// superviseVIP takes part in the VIP election until ctx ends.
func (k *Kubernetes) superviseVIP(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		err := k.runVIP(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errEtcdTrustChanged) {
			log.Print("kubernetes: the node's share changed the etcd CAs; starting the VIP election again")
			continue
		}
		log.Printf("kubernetes: the VIP election stopped: %v; starting it again", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (k *Kubernetes) runVIP(ctx context.Context) error {
	if err := knode.CheckPrepared(k.Paths); err != nil {
		return err
	}
	share, err := knode.ReadShare(k.Paths)
	if err != nil {
		return err
	}
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return err
	}
	n, err := k8s.ReadNode(k.Paths.NodeFile)
	if err != nil {
		return err
	}
	addrs, err := k.hooks.VIPAddresses(c, k.Paths)
	if err != nil {
		return err
	}
	cli, err := k.dialEtcd(c, share)
	if err != nil {
		return err
	}
	// The share is read for each check: while the Kubernetes CAs rotate, the API server may serve
	// a certificate of a CA the share this election started with does not hold.
	healthy := func(ctx context.Context) bool {
		current, err := knode.ReadShare(k.Paths)
		if err != nil {
			current = share
		}
		return k.hooks.APIServerReady(ctx, c, current)
	}
	election := &vip.Election{
		Client:    cli,
		Name:      n.Name,
		Healthy:   healthy,
		Addresses: addrs,
		TTL:       k.timing.VIPTTL,
		Interval:  k.timing.VIPInterval,
		Holding:   &k.vipHolder,
	}
	defer election.Close()
	// The client verifies etcd with the CAs of the share it was dialled with and renews its
	// credential from that share's etcd CA, so a rotation of the etcd CA would leave it verifying
	// etcd's new certificate against the old CA, and authenticating with a CA etcd stops trusting
	// at the finish. On such a change the election ends as when chalkd stops, releasing the VIPs
	// and resigning, and starts again with the new share.
	ectx, restart := context.WithCancelCause(ctx)
	defer restart(nil)
	go watchEtcdTrust(ectx, k.Paths, share, k.timing.VIPInterval, restart)
	err = election.Run(ectx)
	if ctx.Err() == nil && errors.Is(context.Cause(ectx), errEtcdTrustChanged) {
		return errEtcdTrustChanged
	}
	return err
}

// errEtcdTrustChanged means the node's share changed the etcd CAs it trusts or the one that
// issues its credential.
var errEtcdTrustChanged = errors.New("the node's share changed the etcd CAs")

// watchEtcdTrust calls restart with errEtcdTrustChanged once the share in STATE differs from
// started in its etcd CAs or the issuing one, looking every interval until ctx ends. A share that
// cannot be read changes nothing: the election keeps what it has.
func watchEtcdTrust(ctx context.Context, p knode.Paths, started kpki.Share, interval time.Duration, restart context.CancelCauseFunc) {
	want := etcdTrust(started)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		if current, err := knode.ReadShare(p); err == nil && etcdTrust(current) != want {
			restart(errEtcdTrustChanged)
			return
		}
	}
}

// etcdTrust is what chalkd's etcd client takes from the share: the issuing etcd CA and the
// bundle it verifies etcd with.
func etcdTrust(s kpki.Share) string {
	if s.EtcdCA == nil {
		return ""
	}
	return s.EtcdCA.Certificate + "\n" + s.EtcdCABundle()
}

// apiServerReady reports whether the node's API server answers ready.
func apiServerReady(ctx context.Context, c k8s.Cluster, share kpki.Share) bool {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(share.CABundle())) {
		return false
	}
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.LocalAPIServer()+"/readyz", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
