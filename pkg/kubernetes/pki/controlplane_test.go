package pki

import (
	"crypto/x509"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/pki"
)

func testCluster() kubernetes.Cluster {
	return kubernetes.Cluster{
		Kind:        kubernetes.KindControlPlane,
		Endpoint:    "https://api.lab.example:6443",
		ServiceCIDR: "10.96.0.0/12",
		Domain:      "cluster.local",
	}
}

var testNode = kubernetes.Node{Name: "cp1", IP: net.ParseIP("10.0.0.11"), Addresses: []net.IP{net.ParseIP("10.0.0.11"), net.ParseIP("10.0.1.11")}}

func leaf(t *testing.T, files map[string][]byte, name string) *x509.Certificate {
	t.Helper()
	cert, err := pki.ParseCertificate(files[name])
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return cert
}

func ips(cert *x509.Certificate) []string {
	var out []string
	for _, ip := range cert.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

func TestControlPlaneCertificates(t *testing.T) {
	k := secrets(t)
	files, err := ControlPlane(ControlPlaneShare(k), testCluster(), testNode, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file   string
		ca     pki.CertKey
		cn     string
		usages []x509.ExtKeyUsage
	}{
		{FileAPIServer, k.CA, "kube-apiserver", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}},
		{FileAPIServerKubeletClient, k.CA, APIServerKubeletClientUser, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
		{FileAPIServerEtcdClient, k.EtcdCA, APIServerEtcdClientUser, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
		{FileFrontProxyClient, k.FrontProxyCA, FrontProxyClientUser, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
		{FileEtcdServer, k.EtcdCA, "cp1", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}},
		{FileEtcdPeer, k.EtcdCA, "cp1", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}},
	} {
		cert := leaf(t, files, tc.file)
		if cert.Subject.CommonName != tc.cn || !slices.Equal(cert.ExtKeyUsage, tc.usages) {
			t.Errorf("%s: CN %q, usages %v", tc.file, cert.Subject.CommonName, cert.ExtKeyUsage)
		}
		if cert.KeyUsage != x509.KeyUsageDigitalSignature {
			t.Errorf("%s: key usage %v", tc.file, cert.KeyUsage)
		}
		if slices.Contains(cert.Subject.Organization, MastersGroup) {
			t.Errorf("%s: in %s", tc.file, MastersGroup)
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots(t, tc.ca), CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
			t.Errorf("%s: not issued by its CA: %v", tc.file, err)
		}
		if cert.NotAfter.Sub(now) != pki.LeafValidity {
			t.Errorf("%s: valid until %v", tc.file, cert.NotAfter)
		}
		key := tc.file[:len(tc.file)-len(".crt")] + ".key"
		if _, _, err := (pki.CertKey{Certificate: string(files[tc.file]), Key: string(files[key])}).Parse(); err != nil {
			t.Errorf("%s: %v", key, err)
		}
	}

	api := leaf(t, files, FileAPIServer)
	wantNames := []string{"localhost", "kubernetes", "kubernetes.default", "kubernetes.default.svc", "kubernetes.default.svc.cluster.local", "cp1", "api.lab.example"}
	if !slices.Equal(api.DNSNames, wantNames) {
		t.Errorf("API server names %v, want %v", api.DNSNames, wantNames)
	}
	if got := strings.Join(ips(api), ","); got != "10.96.0.1,127.0.0.1,::1,10.0.0.11,10.0.1.11" {
		t.Errorf("API server addresses %s", got)
	}
	etcd := leaf(t, files, FileEtcdServer)
	if got := strings.Join(ips(etcd), ","); got != "127.0.0.1,::1,10.0.0.11,10.0.1.11" || !slices.Equal(etcd.DNSNames, []string{"localhost", "cp1"}) {
		t.Errorf("etcd names %v, addresses %s", etcd.DNSNames, got)
	}

	if string(files[FileCA]) != k.CA.Certificate || string(files[FileCAKey]) != k.CA.Key ||
		string(files[FileEtcdCA]) != k.EtcdCA.Certificate || string(files[FileFrontProxyCA]) != k.FrontProxyCA.Certificate {
		t.Error("the CA files differ from the share")
	}
	if string(files[FileServiceAccountKey]) != k.ServiceAccountKey || !strings.Contains(string(files[FileServiceAccountPub]), "PUBLIC KEY") {
		t.Error("the service account key files are wrong")
	}

	var enc struct {
		Kind      string `json:"kind"`
		Resources []struct {
			Resources []string `json:"resources"`
			Providers []struct {
				Secretbox *struct {
					Keys []struct {
						Secret []byte `json:"secret"`
					} `json:"keys"`
				} `json:"secretbox"`
				Identity *struct{} `json:"identity"`
			} `json:"providers"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(files[FileEncryptionConfig], &enc); err != nil {
		t.Fatal(err)
	}
	// The configuration holds the encryption key, so failures describe it rather than print it.
	if enc.Kind != "EncryptionConfiguration" || len(enc.Resources) != 1 || !slices.Equal(enc.Resources[0].Resources, []string{"secrets"}) {
		t.Errorf("encryption configuration: kind %q, %d resource entries, want secrets alone", enc.Kind, len(enc.Resources))
	} else if p := enc.Resources[0].Providers; len(p) != 2 || p[0].Secretbox == nil || p[0].Identity != nil || p[1].Identity == nil || p[1].Secretbox != nil {
		t.Errorf("encryption configuration: %d providers, want secretbox then the identity fallback", len(p))
	} else if len(p[0].Secretbox.Keys) != 1 || string(p[0].Secretbox.Keys[0].Secret) != string(k.EncryptionKey) {
		t.Error("encryption configuration: secretbox does not hold the share's encryption key")
	}

	for file, user := range map[string]string{FileControllerManagerConfig: ControllerManagerUser, FileSchedulerConfig: SchedulerUser} {
		var kc struct {
			Clusters []struct {
				Cluster struct {
					Server string `json:"server"`
				} `json:"cluster"`
			} `json:"clusters"`
			Users []struct {
				User struct {
					CertData []byte `json:"client-certificate-data"`
				} `json:"user"`
			} `json:"users"`
		}
		if err := json.Unmarshal(files[file], &kc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		cert, err := pki.ParseCertificate(kc.Users[0].User.CertData)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if kc.Clusters[0].Cluster.Server != LocalAPIServer || cert.Subject.CommonName != user {
			t.Errorf("%s: server %s, user %s", file, kc.Clusters[0].Cluster.Server, cert.Subject.CommonName)
		}
		clientOf(t, file, cert, k.CA)
		if slices.Contains(cert.Subject.Organization, MastersGroup) {
			t.Errorf("%s: in %s", file, MastersGroup)
		}
	}

	chalkd, err := IssueChalkd(ControlPlaneShare(k), now)
	if err != nil {
		t.Fatal(err)
	}
	chalkdCert, _, err := chalkd.Parse()
	if err != nil {
		t.Fatal(err)
	}
	clientOf(t, "chalkd", chalkdCert, k.CA)
	if !slices.Equal(chalkdCert.Subject.Organization, []string{MastersGroup}) {
		t.Errorf("chalkd: groups %v, want %s", chalkdCert.Subject.Organization, MastersGroup)
	}

	admin, err := IssueAdmin(k.CA, "admin", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	adminCert, _, err := admin.Parse()
	if err != nil {
		t.Fatal(err)
	}
	clientOf(t, "admin", adminCert, k.CA)
	if !slices.Equal(adminCert.Subject.Organization, []string{ClusterAdminGroup}) {
		t.Errorf("admin: groups %v, want %s", adminCert.Subject.Organization, ClusterAdminGroup)
	}
}

// clientOf checks that cert is a client certificate issued by ca.
func clientOf(t *testing.T, name string, cert *x509.Certificate, ca pki.CertKey) {
	t.Helper()
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots(t, ca), CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("%s: not a client certificate of the Kubernetes CA: %v", name, err)
	}
}

func TestControlPlaneWithAddressEndpoint(t *testing.T) {
	c := testCluster()
	c.Endpoint = "https://10.0.0.10:6443"
	files, err := ControlPlane(ControlPlaneShare(secrets(t)), c, testNode, now)
	if err != nil {
		t.Fatal(err)
	}
	api := leaf(t, files, FileAPIServer)
	if !slices.Contains(ips(api), "10.0.0.10") || slices.Contains(api.DNSNames, "10.0.0.10") {
		t.Errorf("API server names %v, addresses %v", api.DNSNames, ips(api))
	}
}

func TestControlPlaneNeedsControlPlaneShare(t *testing.T) {
	k := secrets(t)
	w, err := WorkerShare(k, "w1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ControlPlane(w, testCluster(), testNode, now); err == nil {
		t.Error("issued control-plane certificates from a worker share")
	}
	if _, err := IssueChalkd(w, now); err == nil {
		t.Error("issued chalkd's certificate from a worker share")
	}
	ck, err := IssueChalkd(ControlPlaneShare(k), now)
	if err != nil {
		t.Fatal(err)
	}
	cert, _, _ := ck.Parse()
	if cert.Subject.CommonName != ChalkdUser || strings.Join(cert.Subject.Organization, ",") != MastersGroup {
		t.Errorf("chalkd certificate subject %v", cert.Subject)
	}
}
