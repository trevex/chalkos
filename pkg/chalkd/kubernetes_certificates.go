package chalkd

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/client"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// Certificates are the Kubernetes side's certificates and trust: the CAs of its share, the
// control plane's and the kubelet's certificates, and the CAs and keys its components run with.
func (p *kubernetesLayer) Certificates(_ context.Context, now time.Time) ([]*nodev1.CertificateStatus, []*nodev1.TrustStatus) {
	return p.k.certificates(now), p.k.trust()
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
		return []*nodev1.TrustStatus{{Name: client.NameKubernetesCA, Fingerprints: fps}}
	}
	file := func(name string) string { return filepath.Join(k.Paths.PKI, name) }
	var list []*nodev1.TrustStatus
	for _, ca := range []struct{ name, bundle, leaf string }{
		{client.NameKubernetesCA, kpki.FileCA, kpki.FileAPIServer},
		{client.NameFrontProxyCA, kpki.FileFrontProxyCA, kpki.FileFrontProxyClient},
		{client.NameEtcdCA, kpki.FileEtcdCA, kpki.FileEtcdServer},
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
		if leaf, err := layer.ReadPEMCertificate(file(ca.leaf)); err == nil {
			if issuer := layer.IssuerIn(leaf, certs); issuer != nil {
				st.Issuing = pki.Fingerprint(issuer.Raw)
			}
		}
		list = append(list, st)
	}
	if pubs, err := os.ReadFile(file(kpki.FileServiceAccountPub)); err == nil {
		st := &nodev1.TrustStatus{Name: client.NameServiceAccountKeys}
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
			st := &nodev1.TrustStatus{Name: client.NameEncryptionKeys, Issuing: keys[0].Fingerprint()}
			for _, key := range keys {
				st.Fingerprints = append(st.Fingerprints, key.Fingerprint())
			}
			list = append(list, st)
		}
	}
	return list
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
		return []*nodev1.CertificateStatus{{Name: client.NameKubernetesShare, Problem: "unreadable: " + err.Error()}}
	}
	// The CAs that may have issued the node's Kubernetes certificates.
	var issuers []*x509.Certificate
	for _, bundle := range []string{share.CABundle(), share.FrontProxyCABundle(), share.EtcdCABundle()} {
		if certs, err := pki.ParseBundle(bundle); err == nil {
			issuers = append(issuers, certs...)
		}
	}
	add := func(name string, cert *x509.Certificate, problem string) {
		list = append(list, layer.CertificateStatus(name, cert, layer.IssuerIn(cert, issuers), problem))
	}
	cas := []struct {
		name    string
		ca      *pki.CertKey
		warning time.Duration
		hint    string
	}{
		{client.NameNodeCA, share.NodeCA, layer.NodeCAWarning, "; run chalkctl node-ca rotate"},
		{client.NameKubernetesCA, &share.CA, layer.CAWarning, ""},
		{client.NameFrontProxyCA, share.FrontProxyCA, layer.CAWarning, ""},
		{client.NameEtcdCA, share.EtcdCA, layer.CAWarning, ""},
	}
	for _, ca := range cas {
		if ca.ca == nil {
			continue
		}
		if cert, err := pki.ParseCertificate([]byte(ca.ca.Certificate)); err == nil {
			problem := layer.CAProblem(cert, now, ca.warning)
			if problem != "" {
				problem = ca.name + " " + problem + ca.hint
			}
			add(ca.name, cert, problem)
		}
	}
	if share.Kind == k8s.KindControlPlane {
		if _, first, err := knode.ControlPlaneRenewAt(k.Paths); err == nil {
			problem := layer.LeafProblem(first, now)
			if p := k.LeavesProblem(); p != "" {
				problem = p
			}
			add(client.NameControlPlane, first, problem)
		}
	}
	kubelet, clientErr := layer.ReadPEMCertificate(k.Paths.KubeletClient())
	if clientErr == nil {
		add(client.NameKubeletClient, kubelet, kubeletProblem(kubelet, now))
	}
	// A worker whose kubelet renewed its certificate on VAR starts from the share's again once
	// VAR is lost; if that one expired too, the kubelet cannot join until a new one arrives.
	if share.Kubelet != nil && (clientErr != nil || !now.Before(kubelet.NotAfter)) {
		if cert, err := pki.ParseCertificate([]byte(share.Kubelet.Certificate)); err == nil && !now.Before(cert.NotAfter) {
			name := "<node>"
			if n, err := k8s.ReadNode(k.Paths.NodeFile); err == nil {
				name = n.Name
			}
			add(client.NameKubeletClient, cert, "expired; deliver a new one with chalkctl apply-config "+name+" --kubernetes-share")
		}
	}
	if serving, err := layer.ReadPEMCertificate(k.Paths.KubeletServing()); err == nil {
		add(client.NameKubeletServing, serving, kubeletProblem(serving, now))
	}
	return list
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
