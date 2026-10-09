package pki

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/pki"
)

// rotating returns secrets in the accept phase of a rotation of kind, and, switched, the same
// rotation after its switch.
func rotating(t *testing.T, kind string) (accepting, switched *pki.Secrets) {
	t.Helper()
	s, err := pki.GenerateSecrets(now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeginRotation(kind, now); err != nil {
		t.Fatal(err)
	}
	after := s
	r := *s.Rotation
	r.Applied = true
	after.Rotation = &r
	if err := after.SwitchRotation(); err != nil {
		t.Fatal(err)
	}
	return &s, &after
}

func fingerprints(t *testing.T, bundle string) []string {
	t.Helper()
	fps, err := pki.Fingerprints(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fps
}

// TestSharesCarryWhatIsAccepted checks that control-plane shares carry every accepted value and
// worker shares the Kubernetes CAs alone, that both read back, and that their bundles put the
// issuing value first.
func TestSharesCarryWhatIsAccepted(t *testing.T) {
	accepting, switched := rotating(t, pki.RotateKubernetesCA)
	for name, s := range map[string]*pki.Secrets{"accepting": accepting, "switched": switched} {
		k := s.Kubernetes
		cp, err := ShareFor(s, kubernetes.KindControlPlane, "cp1", now)
		if err != nil {
			t.Fatal(err)
		}
		w, err := ShareFor(s, kubernetes.KindWorker, "w1", now)
		if err != nil {
			t.Fatal(err)
		}
		for kind, share := range map[string]Share{"control plane": cp, "worker": w} {
			data, err := share.Encode()
			if err != nil {
				t.Fatal(err)
			}
			read, err := ParseShare(data)
			if err != nil {
				t.Fatalf("%s %s: %v", name, kind, err)
			}
			if got := fingerprints(t, read.CABundle()); !slices.Equal(got, fingerprints(t, k.CABundle())) || len(got) != 2 {
				t.Errorf("%s %s: Kubernetes CAs %v", name, kind, got)
			}
		}
		if !slices.Equal(fingerprints(t, cp.EtcdCABundle()), fingerprints(t, k.EtcdCABundle())) || !slices.Equal(fingerprints(t, cp.FrontProxyCABundle()), fingerprints(t, k.FrontProxyCABundle())) {
			t.Errorf("%s: the control plane's etcd or front-proxy CAs differ from the secrets file's", name)
		}
		if w.Accepted == nil || len(w.Accepted.EtcdCA)+len(w.Accepted.FrontProxyCA) > 0 || w.EtcdCABundle() != "" {
			t.Errorf("%s: the worker share accepts %v", name, w.Accepted)
		}
		if issuer, _ := pki.ParseCertificate([]byte(k.CA.Certificate)); w.Kubelet != nil {
			if cert, _ := pki.ParseCertificate([]byte(w.Kubelet.Certificate)); cert.CheckSignatureFrom(issuer) != nil {
				t.Errorf("%s: the worker's kubelet certificate is not the issuing CA's", name)
			}
		}
	}

	for kind := range map[string]bool{pki.RotateServiceAccountKey: true, pki.RotateEncryptionKey: true} {
		s, _ := rotating(t, kind)
		cp, err := ShareFor(s, kubernetes.KindControlPlane, "cp1", now)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := cp.Encode()
		read, err := ParseShare(data)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		pubs, err := read.ServiceAccountPublicKeys()
		if err != nil {
			t.Fatal(err)
		}
		want, _ := s.Kubernetes.ServiceAccountPublicKeys()
		if !slices.Equal(pubs, want) || !slices.EqualFunc(read.EncryptionKeys(), s.Kubernetes.EncryptionKeys(), func(a, b pki.EncryptionKey) bool { return a.Name == b.Name && string(a.Key) == string(b.Key) }) {
			t.Errorf("%s: the share's keys differ from the secrets file's", kind)
		}
	}
}

// TestShareRefusesWhatItMustNotAccept checks the shares a node refuses.
func TestShareRefusesWhatItMustNotAccept(t *testing.T) {
	accepting, _ := rotating(t, pki.RotateKubernetesCA)
	other := secrets(t)
	for name, edit := range map[string]func(cp, w *Share){
		"a worker accepting an etcd CA": func(_, w *Share) { w.Accepted.EtcdCA = []string{other.EtcdCA.Certificate} },
		"a worker with an encryption key name": func(_, w *Share) {
			w.EncryptionKeyName = "chalkos-2"
		},
		"a control plane accepting its issuing CA": func(cp, _ *Share) { cp.Accepted.CA = []string{cp.CA.Certificate} },
		"an accepted CA holding a key":             func(cp, _ *Share) { cp.Accepted.CA = []string{other.CA.Certificate + other.CA.Key} },
		"two encryption keys of one name": func(cp, _ *Share) {
			cp.Accepted.EncryptionKeys = []pki.EncryptionKey{{Name: pki.DefaultEncryptionKeyName, Key: other.EncryptionKey}}
		},
		"an accepted encryption key that is short": func(cp, _ *Share) {
			cp.Accepted.EncryptionKeys = []pki.EncryptionKey{{Name: "old", Key: []byte("short")}}
		},
	} {
		cp, err := ShareFor(accepting, kubernetes.KindControlPlane, "cp1", now)
		if err != nil {
			t.Fatal(err)
		}
		w, err := ShareFor(accepting, kubernetes.KindWorker, "w1", now)
		if err != nil {
			t.Fatal(err)
		}
		edit(&cp, &w)
		cpErr, wErr := cp.Validate(), w.Validate()
		if cpErr == nil && wErr == nil {
			t.Errorf("%s: accepted", name)
		}
		for _, err := range []error{cpErr, wErr} {
			if err != nil && strings.Contains(err.Error(), "PRIVATE KEY") {
				t.Errorf("%s: the error holds a key", name)
			}
		}
	}
}

// TestControlPlaneFilesHoldBundles checks that the control plane's components get every CA and
// key accepted, the issuing one first, while their certificates come from the issuing CAs and
// the controller-manager signs with the issuing CA alone.
func TestControlPlaneFilesHoldBundles(t *testing.T) {
	accepting, switched := rotating(t, pki.RotateKubernetesCA)
	for name, s := range map[string]*pki.Secrets{"accepting": accepting, "switched": switched} {
		k := s.Kubernetes
		cp, _ := ShareFor(s, kubernetes.KindControlPlane, "cp1", now)
		files, err := ControlPlane(cp, testCluster(), testNode, now)
		if err != nil {
			t.Fatal(err)
		}
		for file, bundle := range map[string]string{FileCA: k.CABundle(), FileFrontProxyCA: k.FrontProxyCABundle(), FileEtcdCA: k.EtcdCABundle()} {
			if got := fingerprints(t, string(files[file])); !slices.Equal(got, fingerprints(t, bundle)) || len(got) != 2 {
				t.Errorf("%s: %s holds %v", name, file, got)
			}
		}
		if string(files[FileCASigning]) != k.CA.Certificate {
			t.Errorf("%s: the controller-manager does not sign with the issuing CA alone", name)
		}
		for file, ca := range map[string]pki.CertKey{FileAPIServer: k.CA, FileEtcdServer: k.EtcdCA, FileFrontProxyClient: k.FrontProxyCA} {
			issuer, _ := pki.ParseCertificate([]byte(ca.Certificate))
			if leaf(t, files, file).CheckSignatureFrom(issuer) != nil {
				t.Errorf("%s: %s is not the issuing CA's", name, file)
			}
		}
		for _, file := range Kubeconfigs {
			var kc struct {
				Clusters []struct {
					Cluster struct {
						CA []byte `json:"certificate-authority-data"`
					} `json:"cluster"`
				} `json:"clusters"`
			}
			if err := json.Unmarshal(files[file], &kc); err != nil {
				t.Fatal(err)
			}
			if string(kc.Clusters[0].Cluster.CA) != k.CABundle() {
				t.Errorf("%s: %s does not trust both CAs", name, file)
			}
		}
	}

	_, switchedKey := rotating(t, pki.RotateServiceAccountKey)
	cp, _ := ShareFor(switchedKey, kubernetes.KindControlPlane, "cp1", now)
	files, err := ControlPlane(cp, testCluster(), testNode, now)
	if err != nil {
		t.Fatal(err)
	}
	pubs, _ := switchedKey.Kubernetes.ServiceAccountPublicKeys()
	if string(files[FileServiceAccountPub]) != strings.Join(pubs, "") || len(pubs) != 2 || string(files[FileServiceAccountKey]) != switchedKey.Kubernetes.ServiceAccountKey {
		t.Error("the API server does not accept both service-account keys, or signs with another")
	}

	acceptingEnc, switchedEnc := rotating(t, pki.RotateEncryptionKey)
	for name, s := range map[string]*pki.Secrets{"accepting": acceptingEnc, "switched": switchedEnc} {
		cp, _ := ShareFor(s, kubernetes.KindControlPlane, "cp1", now)
		files, err := ControlPlane(cp, testCluster(), testNode, now)
		if err != nil {
			t.Fatal(err)
		}
		keys, err := ParseEncryptionConfig(files[FileEncryptionConfig])
		if err != nil {
			t.Fatal(err)
		}
		var got, want []string
		for _, key := range keys {
			got = append(got, key.Name+"="+key.Fingerprint())
		}
		for _, key := range s.Kubernetes.EncryptionKeys() {
			want = append(want, key.Name+"="+key.Fingerprint())
		}
		if !slices.Equal(got, want) || len(got) != 2 {
			t.Errorf("%s: the encryption configuration's keys are %v, want %v", name, got, want)
		}
	}
	if _, err := ParseEncryptionConfig([]byte(`{"secret": "c2VjcmV0"`)); err == nil || strings.Contains(err.Error(), "c2Vj") {
		t.Errorf("a broken configuration: %v", err)
	}
}
