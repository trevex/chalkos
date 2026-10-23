package apply

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
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

// A request that fails, reading its Node or approving it, does not keep the others waiting.
func TestApproverContinuesPastFailingRequest(t *testing.T) {
	failApproval := request{}.csr(t, "a-fails")
	failNode := request{username: "system:node:w2", names: []string{"w2"}, ips: []string{"192.168.100.13"}}.csr(t, "b-fails")
	good := request{}.csr(t, "c-good")
	cs := fake.NewClientset(w1, failApproval, failNode, good)
	cs.PrependReactor("update", "certificatesigningrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		update := action.(k8stesting.UpdateAction)
		if update.GetSubresource() == "approval" && update.GetObject().(*certificatesv1.CertificateSigningRequest).Name == "a-fails" {
			return true, nil, errors.New("conflict")
		}
		return false, nil, nil
	})
	cs.PrependReactor("get", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.(k8stesting.GetAction).GetName() == "w2" {
			return true, nil, errors.New("timeout")
		}
		return false, nil, nil
	})
	err := (&Approver{Client: cs}).Once(context.Background())
	if err == nil || !strings.Contains(err.Error(), "a-fails") || !strings.Contains(err.Error(), "b-fails") {
		t.Errorf("err = %v, want both failures", err)
	}
	for name, want := range map[string]bool{"a-fails": false, "b-fails": false, "c-good": true} {
		got, err := cs.CertificatesV1().CertificateSigningRequests().Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if decided(got) != want {
			t.Errorf("%s: decided %v, want %v", name, decided(got), want)
		}
	}
}

func TestApproverForgetsDeletedRequests(t *testing.T) {
	spoofed := request{ips: []string{"192.168.100.11"}}.csr(t, "spoofed")
	cs := fake.NewClientset(w1, spoofed)
	a := &Approver{Client: cs}
	if err := a.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !a.refused["spoofed"] {
		t.Fatal("the refused request is not remembered")
	}
	if err := cs.CertificatesV1().CertificateSigningRequests().Delete(context.Background(), "spoofed", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.refused) != 0 {
		t.Errorf("refused %v after the request was deleted", a.refused)
	}
}

// A revoked node's serving certificate requests stay pending, whatever the time it is listed
// with; the other nodes' are approved. A list that cannot be read approves nothing, and its
// error is reported once a pass.
func TestApproverLeavesRevokedNodesPending(t *testing.T) {
	w2 := testNode("w2",
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "192.168.100.13"},
		corev1.NodeAddress{Type: corev1.NodeHostName, Address: "w2"},
	)
	w3 := testNode("w3",
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "192.168.100.14"},
		corev1.NodeAddress{Type: corev1.NodeHostName, Address: "w3"},
	)
	at := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		list revoked.List
		err  error
		want map[string]bool
		log  []string
	}{
		"revoked": {
			list: revoked.List{"w1": at, "w2": {}},
			want: map[string]bool{"w1-serving": false, "w2-serving": false, "w3-serving": true},
			log: []string{
				"leaving certificate request w1-serving pending: node w1 is revoked since 2026-10-23T10:00:00Z",
				"leaving certificate request w2-serving pending: node w2 is revoked\n",
			},
		},
		"unreadable list": {
			err:  errors.New("timeout"),
			want: map[string]bool{"w1-serving": false, "w2-serving": false, "w3-serving": false},
		},
	} {
		t.Run(name, func(t *testing.T) {
			cs := fake.NewClientset(w1, w2, w3,
				request{}.csr(t, "w1-serving"),
				request{username: "system:node:w2", names: []string{"w2"}, ips: []string{"192.168.100.13"}}.csr(t, "w2-serving"),
				request{username: "system:node:w3", names: []string{"w3"}, ips: []string{"192.168.100.14"}}.csr(t, "w3-serving"),
			)
			var logs bytes.Buffer
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			a := &Approver{Client: cs, Revoked: func(context.Context) (revoked.List, error) { return tc.list, tc.err }}
			err := a.Once(context.Background())
			if (err != nil) != (tc.err != nil) || (err != nil && strings.Count(err.Error(), "timeout") != 1) {
				t.Errorf("err = %v, want %v once", err, tc.err)
			}
			if tc.err != nil && !strings.Contains(err.Error(), "left 3 certificate requests pending") {
				t.Errorf("err = %v, want the requests counted", err)
			}
			for csr, want := range tc.want {
				got, err := cs.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csr, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if decided(got) != want {
					t.Errorf("%s: decided %v, want %v", csr, decided(got), want)
				}
			}
			for _, want := range tc.log {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("log lacks %q:\n%s", want, logs.String())
				}
			}
		})
	}
}

// Without a function of its own, the approver reads the revoked nodes from its client.
func TestApproverReadsRevokedNodesFromTheCluster(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: revoked.Namespace, Name: revoked.Name},
		Data:       map[string]string{"w1": "2026-10-23T10:00:00Z"},
	}
	cs := fake.NewClientset(w1, cm, request{}.csr(t, "w1-serving"))
	if err := (&Approver{Client: cs}).Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := cs.CertificatesV1().CertificateSigningRequests().Get(context.Background(), "w1-serving", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if decided(got) {
		t.Errorf("approved the revoked node's request: %v", got.Status.Conditions)
	}
}

// The approver applies the revoked nodes' admission policy at its first pass and again whenever
// the ConfigMap changed since, as by hand or after an apply that was cancelled; a pass whose
// apply failed is retried at the next.
func TestApproverEnforcesRevokedNodes(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	applies, failing := 0, false
	cs.PrependReactor("patch", "validatingadmissionpolicies", func(k8stesting.Action) (bool, runtime.Object, error) {
		applies++
		if failing {
			return true, nil, errors.New("connection refused")
		}
		return false, nil, nil
	})
	a := &Approver{Client: cs}
	if err := a.Enforce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := policyList(t, cs); got != "{}" {
		t.Errorf("the policy names %s, want none", got)
	}
	if err := a.Enforce(ctx); err != nil || applies != 1 {
		t.Errorf("unchanged: %v, %d applies, want 1", err, applies)
	}
	// The ConfigMap is changed by hand, past chalkctl.
	if _, err := cs.CoreV1().ConfigMaps(revoked.Namespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: revoked.Namespace, Name: revoked.Name, ResourceVersion: "1"},
		Data:       map[string]string{"w1": "2026-10-23T10:00:00Z"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	failing = true
	if err := a.Enforce(ctx); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err = %v, want the apply's", err)
	}
	failing = false
	if err := a.Enforce(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := policyList(t, cs), `{"w1": "since 2026-10-23T10:00:00Z"}`; got != want {
		t.Errorf("the policy names %s, want %s", got, want)
	}
	cm, _ := cs.CoreV1().ConfigMaps(revoked.Namespace).Get(ctx, revoked.Name, metav1.GetOptions{})
	cm.Data = nil
	cm.ResourceVersion = "2"
	if _, err := cs.CoreV1().ConfigMaps(revoked.Namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	applies = 0
	for range 2 {
		if err := a.Enforce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := policyList(t, cs); got != "{}" || applies != 1 {
		t.Errorf("after the change the policy names %s after %d applies, want none after 1", got, applies)
	}
}

// Run enforces the revoked nodes' admission policy besides approving requests.
func TestApproverRunEnforcesRevokedNodes(t *testing.T) {
	cs := fake.NewClientset()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		(&Approver{Client: cs}).Run(ctx, 10*time.Millisecond)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	deadline := time.Now().Add(time.Minute)
	for {
		_, err := cs.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(ctx, revoked.Name, metav1.GetOptions{})
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no policy applied: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
