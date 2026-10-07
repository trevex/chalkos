package apply

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"net/url"
	"testing"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// request is a certificate request; fields left empty take the values of node w1.
type request struct {
	username, cn string
	org          []string
	groups       []string
	ips          []string
	names        []string
	uris         []string
	usages       []certificatesv1.KeyUsage
	signer       string
}

func (r request) csr(t *testing.T, name string) *certificatesv1.CertificateSigningRequest {
	t.Helper()
	if r.username == "" {
		r.username = "system:node:w1"
	}
	if r.cn == "" {
		r.cn = r.username
	}
	if r.org == nil {
		r.org = []string{"system:nodes"}
	}
	if r.groups == nil {
		r.groups = []string{"system:nodes", "system:authenticated"}
	}
	if r.ips == nil {
		r.ips = []string{"192.168.100.12"}
	}
	if r.names == nil {
		r.names = []string{"w1"}
	}
	if r.usages == nil {
		r.usages = []certificatesv1.KeyUsage{certificatesv1.UsageDigitalSignature, certificatesv1.UsageServerAuth}
	}
	if r.signer == "" {
		r.signer = certificatesv1.KubeletServingSignerName
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.CertificateRequest{Subject: pkix.Name{CommonName: r.cn, Organization: r.org}, DNSNames: r.names}
	for _, ip := range r.ips {
		template.IPAddresses = append(template.IPAddresses, net.ParseIP(ip))
	}
	for _, u := range r.uris {
		parsed, _ := url.Parse(u)
		template.URIs = append(template.URIs, parsed)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatal(err)
	}
	return &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}),
			SignerName: r.signer,
			Usages:     r.usages,
			Username:   r.username,
			Groups:     r.groups,
		},
	}
}

func testNode(name string, addresses ...corev1.NodeAddress) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Addresses: addresses}}
}

var w1 = testNode("w1",
	corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "192.168.100.12"},
	corev1.NodeAddress{Type: corev1.NodeHostName, Address: "w1"},
)

func TestApprovable(t *testing.T) {
	if err := Approvable(request{}.csr(t, "ok"), w1); err != nil {
		t.Fatalf("a matching request was refused: %v", err)
	}
	for name, tc := range map[string]struct {
		req  request
		node *corev1.Node
	}{
		"no Node object":          {request{}, nil},
		"another node's name":     {request{username: "system:node:cp1", cn: "system:node:cp1"}, w1},
		"subject of another node": {request{cn: "system:node:cp1"}, w1},
		"not a node":              {request{username: "admin", groups: []string{"chalkos:cluster-admins"}}, w1},
		"node user without group": {request{groups: []string{"system:authenticated"}}, w1},
		"wrong organization":      {request{org: []string{"system:masters"}}, w1},
		"address of another node": {request{ips: []string{"192.168.100.11"}}, w1},
		"name the node lacks":     {request{names: []string{"w1", "kubernetes"}}, w1},
		"URI":                     {request{uris: []string{"spiffe://cluster/w1"}}, w1},
		"no address":              {request{ips: []string{}, names: []string{}}, w1},
		"client usage":            {request{usages: []certificatesv1.KeyUsage{certificatesv1.UsageDigitalSignature, certificatesv1.UsageServerAuth, certificatesv1.UsageClientAuth}}, w1},
		"no server usage":         {request{usages: []certificatesv1.KeyUsage{certificatesv1.UsageDigitalSignature}}, w1},
		"another signer":          {request{signer: certificatesv1.KubeAPIServerClientKubeletSignerName}, w1},
		"address the node lacks":  {request{}, testNode("w1", corev1.NodeAddress{Type: corev1.NodeHostName, Address: "w1"})},
	} {
		if err := Approvable(tc.req.csr(t, name), tc.node); err == nil {
			t.Errorf("%s: approvable", name)
		}
	}
}

func TestApproverApprovesOnlyMatchingRequests(t *testing.T) {
	good := request{}.csr(t, "good")
	spoofed := request{ips: []string{"192.168.100.11"}}.csr(t, "spoofed")
	client := request{signer: certificatesv1.KubeAPIServerClientKubeletSignerName}.csr(t, "client")
	cs := fake.NewClientset(w1, good, spoofed, client)
	a := &Approver{Client: cs}
	if err := a.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"good": true, "spoofed": false, "client": false} {
		got, err := cs.CertificatesV1().CertificateSigningRequests().Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if decided(got) != want {
			t.Errorf("%s: decided %v, want %v (conditions %v)", name, decided(got), want, got.Status.Conditions)
		}
	}
	// A second pass leaves decided requests alone.
	if err := a.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.CertificatesV1().CertificateSigningRequests().Get(context.Background(), "good", metav1.GetOptions{})
	if len(got.Status.Conditions) != 1 || got.Status.Conditions[0].Type != certificatesv1.CertificateApproved {
		t.Errorf("conditions of good = %v", got.Status.Conditions)
	}
}
