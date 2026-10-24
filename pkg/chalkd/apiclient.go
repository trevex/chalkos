package chalkd

import (
	"time"

	"connectrpc.com/connect"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// apiConn is chalkd's client for the node's own API server with its own credential, kept until
// the credential is at its half-life or the share or the API server's address changes.
type apiConn struct {
	key       string
	cred      *credential
	transport interface{ CloseIdleConnections() }
	client    kubernetes.Interface
}

// apiKey identifies the share's Kubernetes CA, the CAs the API server's certificate may chain
// to, and the API server's address.
func apiKey(c k8s.Cluster, share kpki.Share) string {
	return share.CA.Certificate + "\x00" + share.CABundle() + "\x00" + c.LocalAPIServer()
}

// apiClient checks the node with need, such as k.etcdMember, and returns a client for the node's
// own API server and what need read.
func (k *Kubernetes) apiClient(need func() (controlPlaneNode, error)) (kubernetes.Interface, controlPlaneNode, error) {
	cp, err := need()
	if err != nil {
		return nil, controlPlaneNode{}, err
	}
	client, err := k.hooks.APIClient(cp.cluster, cp.share)
	if err != nil {
		return nil, controlPlaneNode{}, failed(connect.CodeInternal, "reach the API server: %v", err)
	}
	return client, cp, nil
}

// ownAPIClient returns chalkd's client for the API server, with a credential in system:masters.
// One client serves every request until its certificate is at its half-life or the share or the
// address changes. Then a new client with a new credential replaces it:
//   - the old client's idle connections are closed;
//   - requests in flight on it finish on their connections, whose certificate is still valid.
//
// Building a client per request would issue a certificate and open a connection each time.
func (k *Kubernetes) ownAPIClient(c k8s.Cluster, share kpki.Share) (kubernetes.Interface, error) {
	key := apiKey(c, share)
	k.apiMu.Lock()
	defer k.apiMu.Unlock()
	if k.api != nil && k.api.key == key && !k.api.cred.due() {
		return k.api.client, nil
	}
	cred := newCredential(share, credentialValidity, time.Now)
	if _, err := cred.GetClientCertificate(nil); err != nil {
		return nil, err
	}
	transport, err := cred.transport()
	if err != nil {
		return nil, err
	}
	// Rewriting every encrypted object takes longer than one request.
	client, err := kubernetes.NewForConfig(&rest.Config{Host: c.LocalAPIServer(), Transport: transport, Timeout: time.Minute})
	if err != nil {
		return nil, err
	}
	if k.api != nil {
		k.api.transport.CloseIdleConnections()
	}
	k.api = &apiConn{key: key, cred: cred, transport: transport, client: client}
	return client, nil
}
