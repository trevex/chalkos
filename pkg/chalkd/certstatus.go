package chalkd

import (
	"context"
	"crypto/x509"
	"encoding/csv"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage/node"
)

// How long before they expire CAs are reported: a year, and for the node CA 18 months, as node
// certificates never outlive it and it needs replacing well before.
const (
	caWarning     = 365 * 24 * time.Hour
	nodeCAWarning = 548 * 24 * time.Hour
)

// certificateStatus describes a certificate with its fingerprint and, when known, its issuer's.
func certificateStatus(name string, cert, issuer *x509.Certificate, problem string) *nodev1.CertificateStatus {
	st := &nodev1.CertificateStatus{Name: name, NotAfter: timestamppb.New(cert.NotAfter), Problem: problem, Fingerprint: pki.Fingerprint(cert.Raw)}
	if issuer != nil {
		st.Issuer = pki.Fingerprint(issuer.Raw)
	}
	return st
}

// issuerIn returns the certificate of cas that signed cert; nil when none did.
func issuerIn(cert *x509.Certificate, cas []*x509.Certificate) *x509.Certificate {
	for _, ca := range cas {
		if cert.CheckSignatureFrom(ca) == nil {
			return ca
		}
	}
	return nil
}

// osCAs returns the OS CAs the node trusts.
func (s *Server) osCAs() string {
	if s.Certificate != nil {
		return s.Certificate.OSCA()
	}
	return string(s.readOSCA())
}

// trust lists what the node trusts and issues with, by fingerprint.
func (s *Server) trust() []*nodev1.TrustStatus {
	var list []*nodev1.TrustStatus
	if fps, err := pki.Fingerprints(s.osCAs()); err == nil {
		list = append(list, &nodev1.TrustStatus{Name: "OS CA", Fingerprints: fps})
	}
	if s.Kubernetes != nil {
		list = append(list, s.Kubernetes.trust()...)
	}
	return list
}

// trust lists the Kubernetes CAs and keys the node's components run with, from the files the
// preparation wrote: the kubelet's CAs on every node, the control plane's CAs and keys on a
// control plane. A CA issues if it issued the control plane's certificate of its kind.
func (k *Kubernetes) trust() []*nodev1.TrustStatus {
	c, err := k8s.ReadCluster(k.Paths.Cluster)
	if err != nil {
		return nil
	}
	if prepared, err := knode.Prepared(k.Paths); err != nil || !prepared {
		return nil
	}
	if c.Kind != k8s.KindControlPlane {
		data, err := os.ReadFile(filepath.Join(k.Paths.KubeletDir(), "ca.crt"))
		if err != nil {
			return nil
		}
		fps, err := pki.Fingerprints(string(data))
		if err != nil {
			return nil
		}
		return []*nodev1.TrustStatus{{Name: "Kubernetes CA", Fingerprints: fps}}
	}
	file := func(name string) string { return filepath.Join(k.Paths.PKI, name) }
	var list []*nodev1.TrustStatus
	for _, ca := range []struct{ name, bundle, leaf string }{
		{"Kubernetes CA", kpki.FileCA, kpki.FileAPIServer},
		{"front-proxy CA", kpki.FileFrontProxyCA, kpki.FileFrontProxyClient},
		{"etcd CA", kpki.FileEtcdCA, kpki.FileEtcdServer},
	} {
		data, err := os.ReadFile(file(ca.bundle))
		if err != nil {
			continue
		}
		certs, err := pki.ParseBundle(string(data))
		if err != nil {
			continue
		}
		st := &nodev1.TrustStatus{Name: ca.name}
		for _, cert := range certs {
			st.Fingerprints = append(st.Fingerprints, pki.Fingerprint(cert.Raw))
		}
		if leaf, err := readPEMCertificate(file(ca.leaf)); err == nil {
			if issuer := issuerIn(leaf, certs); issuer != nil {
				st.Issuing = pki.Fingerprint(issuer.Raw)
			}
		}
		list = append(list, st)
	}
	if pubs, err := os.ReadFile(file(kpki.FileServiceAccountPub)); err == nil {
		st := &nodev1.TrustStatus{Name: "service-account keys"}
		rest := pubs
		for {
			var block *pem.Block
			if block, rest = pem.Decode(rest); block == nil {
				break
			}
			if fp, err := pki.PublicKeyFingerprint(string(pem.EncodeToMemory(block))); err == nil {
				st.Fingerprints = append(st.Fingerprints, fp)
			}
		}
		if private, err := os.ReadFile(file(kpki.FileServiceAccountKey)); err == nil {
			if pub, err := pki.ServiceAccountPublicKey(string(private)); err == nil {
				st.Issuing, _ = pki.PublicKeyFingerprint(pub)
			}
		}
		list = append(list, st)
	}
	if config, err := os.ReadFile(file(kpki.FileEncryptionConfig)); err == nil {
		if keys, err := kpki.ParseEncryptionConfig(config); err == nil && len(keys) > 0 {
			st := &nodev1.TrustStatus{Name: "encryption keys", Issuing: keys[0].Fingerprint()}
			for _, key := range keys {
				st.Fingerprints = append(st.Fingerprints, key.Fingerprint())
			}
			list = append(list, st)
		}
	}
	return list
}

// nodeName is the node's name for the commands its messages advise: the subject of its node
// certificate, which chalkctl issues to the name the cluster definition gives the node. A node
// without one says <node>, which chalkctl replaces with the name it was called with.
func (s *Server) nodeName() string {
	if s.Certificate != nil {
		if leaf := s.Certificate.Current().Leaf; leaf != nil && leaf.Subject.CommonName != "" {
			return leaf.Subject.CommonName
		}
	}
	return "<node>"
}

// certificates lists every certificate the node holds or issues with what needs doing about it.
func (s *Server) certificates(now time.Time) []*nodev1.CertificateStatus {
	var list []*nodev1.CertificateStatus
	add := func(name string, cert, issuer *x509.Certificate, problem string) {
		list = append(list, certificateStatus(name, cert, issuer, problem))
	}
	if s.Certificate != nil {
		leaf := s.Certificate.Current().Leaf
		problem := leafProblem(leaf, now)
		switch {
		case s.Renewal != nil && s.Renewal.Problem() != "":
			problem = s.Renewal.Problem()
		case problem != "" && s.Renewal == nil:
			// Only nodes with Kubernetes reach a control plane to renew it.
			problem += "; renew it with chalkctl node renew " + s.nodeName()
		}
		var issuer *x509.Certificate
		if chain := s.Certificate.Current().TLS.Certificate; len(chain) > 1 {
			issuer, _ = x509.ParseCertificate(chain[1])
		}
		add("node", leaf, issuer, problem)
	}
	if osCAs, err := pki.ParseBundle(s.osCAs()); err == nil {
		for _, osCA := range osCAs {
			problem := caProblem(osCA, now, caWarning)
			if problem != "" {
				problem = "OS CA " + problem
			}
			add("OS CA", osCA, osCA, problem)
		}
	}
	if s.Kubernetes != nil {
		list = append(list, s.Kubernetes.certificates(now)...)
	}
	return list
}

func (s *Server) readOSCA() []byte {
	data, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "chalkd", CAFile))
	return data
}

// certificates lists the certificates of the node's Kubernetes side: the CAs of its share, the
// control plane's leaf certificates and the kubelet's.
func (k *Kubernetes) certificates(now time.Time) []*nodev1.CertificateStatus {
	var list []*nodev1.CertificateStatus
	share, err := knode.ReadShare(k.Paths)
	if errors.Is(err, knode.ErrNoShare) {
		return nil
	}
	if err != nil {
		// Its expiry is unknown, as are those of the certificates the share holds.
		return []*nodev1.CertificateStatus{{Name: "Kubernetes share", Problem: "unreadable: " + err.Error()}}
	}
	// The CAs that may have issued the node's Kubernetes certificates.
	var issuers []*x509.Certificate
	for _, bundle := range []string{share.CABundle(), share.FrontProxyCABundle(), share.EtcdCABundle()} {
		if certs, err := pki.ParseBundle(bundle); err == nil {
			issuers = append(issuers, certs...)
		}
	}
	add := func(name string, cert *x509.Certificate, problem string) {
		list = append(list, certificateStatus(name, cert, issuerIn(cert, issuers), problem))
	}
	cas := []struct {
		name    string
		ca      *pki.CertKey
		warning time.Duration
		hint    string
	}{
		{"node CA", share.NodeCA, nodeCAWarning, "; run chalkctl node-ca rotate"},
		{"Kubernetes CA", &share.CA, caWarning, ""},
		{"front-proxy CA", share.FrontProxyCA, caWarning, ""},
		{"etcd CA", share.EtcdCA, caWarning, ""},
	}
	for _, ca := range cas {
		if ca.ca == nil {
			continue
		}
		if cert, err := pki.ParseCertificate([]byte(ca.ca.Certificate)); err == nil {
			problem := caProblem(cert, now, ca.warning)
			if problem != "" {
				problem = ca.name + " " + problem + ca.hint
			}
			add(ca.name, cert, problem)
		}
	}
	if share.Kind == k8s.KindControlPlane {
		if _, first, err := knode.ControlPlaneRenewAt(k.Paths); err == nil {
			problem := leafProblem(first, now)
			if p := k.LeavesProblem(); p != "" {
				problem = p
			}
			add("Kubernetes control plane", first, problem)
		}
	}
	client, clientErr := readPEMCertificate(k.Paths.KubeletClient())
	if clientErr == nil {
		add("kubelet client", client, kubeletProblem(client, now))
	}
	// A worker whose kubelet renewed its certificate on VAR starts from the share's again once
	// VAR is lost; if that one expired too, the kubelet cannot join until a new one arrives.
	if share.Kubelet != nil && (clientErr != nil || !now.Before(client.NotAfter)) {
		if cert, err := pki.ParseCertificate([]byte(share.Kubelet.Certificate)); err == nil && !now.Before(cert.NotAfter) {
			name := "<node>"
			if n, err := k8s.ReadNode(k.Paths.NodeFile); err == nil {
				name = n.Name
			}
			add("kubelet client", cert, "expired; deliver a new one with chalkctl apply-identity "+name+" --kubernetes-share")
		}
	}
	if serving, err := readPEMCertificate(k.Paths.KubeletServing()); err == nil {
		add("kubelet serving", serving, kubeletProblem(serving, now))
	}
	return list
}

// leafProblem warns once less than a third of a certificate's lifetime remains, when it is due
// for renewal.
func leafProblem(cert *x509.Certificate, now time.Time) string {
	switch {
	case !now.Before(cert.NotAfter):
		return "expired"
	case !now.Before(pki.RenewAt(cert)):
		return "less than a third of its lifetime remains"
	}
	return ""
}

// kubeletProblem warns once less than a tenth of a kubelet certificate's lifetime remains: the
// kubelet renews it itself once 70 to 90 % passed, so a warning before would be noise.
func kubeletProblem(cert *x509.Certificate, now time.Time) string {
	switch {
	case !now.Before(cert.NotAfter):
		return "expired"
	case cert.NotAfter.Sub(now) < cert.NotAfter.Sub(cert.NotBefore)/10:
		return "less than a tenth of its lifetime remains, though the kubelet renews it itself"
	}
	return ""
}

// caProblem warns when a CA has less than warning left.
func caProblem(cert *x509.Certificate, now time.Time, warning time.Duration) string {
	switch {
	case !now.Before(cert.NotAfter):
		return "expired " + cert.NotAfter.UTC().Format(time.DateOnly)
	case cert.NotAfter.Sub(now) < warning:
		return "expires " + cert.NotAfter.UTC().Format(time.DateOnly)
	}
	return ""
}

// readPEMCertificate reads the first certificate of a PEM file, such as one of the kubelet's,
// which hold their key too.
func readPEMCertificate(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return pki.ParseCertificate(data)
}

// timeStatus reads chrony's tracking state.
func (s *Server) timeStatus(ctx context.Context) *nodev1.TimeStatus {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// chronyc fails on every request while chronyd is down: its error goes into the status, not
	// once more into chalkd's log.
	run := s.Run.Run
	if quiet, ok := s.Run.(node.QuietRunner); ok {
		run = quiet.RunQuiet
	}
	out, err := run(ctx, "chronyc", "-n", "-c", "tracking")
	if err != nil {
		return &nodev1.TimeStatus{Error: err.Error()}
	}
	st, err := parseTracking(out)
	if err != nil {
		return &nodev1.TimeStatus{Error: err.Error()}
	}
	return st
}

// parseTracking reads chronyc's tracking report in its CSV form: reference ID, source, stratum,
// reference time, the system clock's offset (positive when it is slow), ... and the leap status
// as field 13. Later fields, which a newer chrony may add, are ignored.
func parseTracking(out []byte) (*nodev1.TimeStatus, error) {
	records, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil || len(records) != 1 || len(records[0]) < 14 {
		return nil, fmt.Errorf("chronyc tracking printed %q", strings.TrimSpace(string(out)))
	}
	r := records[0]
	stratum, err := strconv.Atoi(r[2])
	if err != nil {
		return nil, fmt.Errorf("chronyc tracking: stratum %q", r[2])
	}
	slow, err := strconv.ParseFloat(r[4], 64)
	if err != nil {
		return nil, fmt.Errorf("chronyc tracking: offset %q", r[4])
	}
	st := &nodev1.TimeStatus{
		Synchronised:  r[13] != "Not synchronised" && stratum > 0 && stratum < 16,
		OffsetSeconds: -slow,
	}
	if st.Synchronised {
		st.Source = r[1]
	}
	return st, nil
}
