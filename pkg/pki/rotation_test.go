package pki

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// roundTrip encodes the secrets and reads them back, as chalkctl does between two phases.
func roundTrip(t *testing.T, s Secrets) Secrets {
	t.Helper()
	data, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadSecrets(data, noIdentities)
	if err != nil {
		t.Fatal(err)
	}
	return read
}

// trusted is what each kind's bundle holds, as fingerprints of certificates and public keys and
// names of encryption keys, the issuing value first.
func trusted(t *testing.T, s Secrets, kind string) []string {
	t.Helper()
	k := s.Kubernetes
	var out []string
	certs := func(bundles ...string) {
		for _, b := range bundles {
			fps, err := Fingerprints(b)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, fps...)
		}
	}
	switch kind {
	case RotateOSCA:
		certs(s.OSCABundle())
	case RotateKubernetesCA:
		certs(k.CABundle(), k.FrontProxyCABundle(), k.EtcdCABundle())
	case RotateServiceAccountKey:
		pubs, err := k.ServiceAccountPublicKeys()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pubs {
			fp, err := PublicKeyFingerprint(p)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, fp)
		}
	case RotateEncryptionKey:
		for _, key := range k.EncryptionKeys() {
			out = append(out, key.Name)
		}
	}
	return out
}

// TestRotationPhases runs each kind's phases on the secrets file: accept adds the new value
// after the issuing one, switch makes it the issuing one and accepts the old one, finish drops
// the old one, and nothing else in the file changes.
func TestRotationPhases(t *testing.T) {
	for _, kind := range RotationKinds {
		t.Run(kind, func(t *testing.T) {
			s := generate(t)
			before := trusted(t, s, kind)
			if len(before) == 0 || (kind != RotateKubernetesCA && len(before) != 1) {
				t.Fatalf("trusted before: %v", before)
			}
			others := map[string][]string{}
			for _, k := range RotationKinds {
				if k != kind {
					others[k] = trusted(t, s, k)
				}
			}
			if err := s.BeginRotation(kind, now); err != nil {
				t.Fatal(err)
			}
			s = roundTrip(t, s)
			accepting := trusted(t, s, kind)
			old := len(before)
			if len(accepting) != 2*old || !slices.Equal(accepting[:old], firstOfEach(before, kind)) && kind != RotateKubernetesCA {
				t.Fatalf("trusted while accepting: %v, want %v followed by the new values", accepting, before)
			}
			if s.Rotation.Phase != PhaseAccept || s.Rotation.Applied {
				t.Fatalf("rotation %v, want the accept phase, not applied", s.Rotation)
			}
			// The issuing values change only at the switch.
			if !slices.Equal(firstOfEach(accepting, kind), firstOfEach(before, kind)) {
				t.Errorf("the accept phase changed the issuing value")
			}
			if err := s.SwitchRotation(now); err == nil {
				t.Fatal("switched before every node applied the accept phase")
			}
			s.Rotation.Applied = true
			later := now.Add(time.Minute)
			if err := s.SwitchRotation(later); err != nil {
				t.Fatal(err)
			}
			s = roundTrip(t, s)
			switched := trusted(t, s, kind)
			if !sameSet(switched, accepting) || slices.Equal(firstOfEach(switched, kind), firstOfEach(before, kind)) {
				t.Errorf("trusted after the switch: %v, want the same values as %v with the new one issuing", switched, accepting)
			}
			if !s.Rotation.Switched.Equal(later) || s.Rotation.New != nil {
				t.Errorf("rotation after the switch: switched %v, new values %v", s.Rotation.Switched, s.Rotation.New)
			}
			if err := s.FinishRotation(); err == nil {
				t.Fatal("finished before the refresh")
			}
			s.Rotation.Applied = true
			if err := s.RefreshRotation(); err != nil {
				t.Fatal(err)
			}
			if err := s.FinishRotation(); err == nil {
				t.Fatal("finished before every node applied the refresh")
			}
			s.Rotation.Applied = true
			if err := s.FinishRotation(); err != nil {
				t.Fatal(err)
			}
			s = roundTrip(t, s)
			finished := trusted(t, s, kind)
			if !slices.Equal(finished, firstOfEach(switched, kind)) {
				t.Errorf("trusted after the finish: %v, want only the new values %v", finished, firstOfEach(switched, kind))
			}
			if err := s.EndRotation(); err == nil {
				t.Fatal("ended before every node applied the finish")
			}
			s.Rotation.Applied = true
			if err := s.EndRotation(); err != nil {
				t.Fatal(err)
			}
			s = roundTrip(t, s)
			if s.Rotation != nil || !slices.Equal(trusted(t, s, kind), finished) {
				t.Errorf("after the rotation: %v, trusted %v", s.Rotation, trusted(t, s, kind))
			}
			for k, want := range others {
				if got := trusted(t, s, k); !slices.Equal(got, want) {
					t.Errorf("rotating the %s changed the %s: %v, want %v", kind, k, got, want)
				}
			}
		})
	}
}

// firstOfEach returns the issuing values: for the Kubernetes CAs the first of each of the three
// bundles, which hold the same number of values; for the others the first.
func firstOfEach(values []string, kind string) []string {
	if kind != RotateKubernetesCA {
		return values[:1]
	}
	n := len(values) / 3
	return []string{values[0], values[n], values[2*n]}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// TestOSCARotationIssuesFromTheNewRoot checks that the switch makes the new OS CA and its node CA
// the ones that issue, and that the node CA chains to the new root.
func TestOSCARotationIssuesFromTheNewRoot(t *testing.T) {
	s := generate(t)
	oldRoot := s.OSCA.Certificate
	if err := s.BeginRotation(RotateOSCA, now); err != nil {
		t.Fatal(err)
	}
	newRoot := s.Rotation.New.OSCA.Certificate
	s.Rotation.Applied = true
	if err := s.SwitchRotation(now); err != nil {
		t.Fatal(err)
	}
	if s.OSCA.Certificate != newRoot || !slices.Equal(s.Accepted.OSCA, []string{oldRoot}) {
		t.Error("the switch did not make the new OS CA the issuing one")
	}
	if err := ValidateNodeCA(s.NodeCA, newRoot); err != nil {
		t.Errorf("the node CA after the switch: %v", err)
	}
	node, err := IssueNode(s.NodeCA, NodeNames{CommonName: "w1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	// Nodes trust both roots until the finish.
	if _, err := VerifyNode(node.Certificate, node.Key, s.OSCABundle(), now); err != nil {
		t.Error(err)
	}
}

// TestEncryptionKeyNames checks that a new encryption key gets a name of its own, which the
// switch makes the encrypting key's.
func TestEncryptionKeyNames(t *testing.T) {
	s := generate(t)
	if s.Kubernetes.KeyName() != DefaultEncryptionKeyName {
		t.Errorf("a new secrets file's key is %s", s.Kubernetes.KeyName())
	}
	if err := s.BeginRotation(RotateEncryptionKey, now); err != nil {
		t.Fatal(err)
	}
	keys := s.Kubernetes.EncryptionKeys()
	if len(keys) != 2 || keys[0].Name != DefaultEncryptionKeyName || keys[1].Name == DefaultEncryptionKeyName || strings.Contains(keys[1].Name, ":") {
		t.Fatalf("keys while accepting: %v", keys)
	}
	s.Rotation.Applied = true
	if err := s.SwitchRotation(now); err != nil {
		t.Fatal(err)
	}
	if s.Kubernetes.KeyName() != keys[1].Name || string(s.Kubernetes.EncryptionKey) != string(keys[1].Key) {
		t.Errorf("the switch made %s the encrypting key, want %s", s.Kubernetes.KeyName(), keys[1].Name)
	}
	if acc := s.Kubernetes.Accepted.EncryptionKeys; len(acc) != 1 || acc[0].Name != DefaultEncryptionKeyName {
		t.Errorf("accepted after the switch: %v", acc)
	}
}

// TestOneRotationAtATime checks that a running rotation refuses another, naming it.
func TestOneRotationAtATime(t *testing.T) {
	s := generate(t)
	if err := s.BeginRotation(RotateServiceAccountKey, now); err != nil {
		t.Fatal(err)
	}
	for _, kind := range RotationKinds {
		var runs *ErrRotationRuns
		err := s.BeginRotation(kind, now)
		if !errors.As(err, &runs) || !strings.Contains(err.Error(), "service-account-key") {
			t.Errorf("a rotation of the %s while one of the service-account key runs: %v", kind, err)
		}
	}
	if err := generatePtr(t).BeginRotation("everything", now); err == nil {
		t.Error("an unknown kind started")
	}
}

func generatePtr(t *testing.T) *Secrets {
	s := generate(t)
	return &s
}

// TestValidateRefusesInconsistentRotations checks what a secrets file that chalkctl did not
// write that way is refused for.
func TestValidateRefusesInconsistentRotations(t *testing.T) {
	other := generate(t)
	leaf, err := IssueLeaf(other.Kubernetes.CA, Leaf{CommonName: "leaf", Client: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(s *Secrets){
		"accepted values without a rotation": func(s *Secrets) {
			s.Accepted.OSCA = []string{other.OSCA.Certificate}
		},
		"an accepted leaf": func(s *Secrets) {
			s.Kubernetes.Accepted.CA = []string{leaf.Certificate}
			s.Rotation = &Rotation{Kind: RotateKubernetesCA, Phase: PhaseRefresh, Switched: now}
		},
		"an accepted OS CA that is a node CA": func(s *Secrets) {
			s.Accepted.OSCA = []string{other.NodeCA.Certificate}
			s.Rotation = &Rotation{Kind: RotateOSCA, Phase: PhaseRefresh, Switched: now}
		},
		"an accepted certificate followed by a key": func(s *Secrets) {
			s.Accepted.OSCA = []string{other.OSCA.Certificate + other.OSCA.Key}
			s.Rotation = &Rotation{Kind: RotateOSCA, Phase: PhaseRefresh, Switched: now}
		},
		"an accepted CA that is the issuing one": func(s *Secrets) {
			s.Kubernetes.Accepted.EtcdCA = []string{s.Kubernetes.EtcdCA.Certificate}
			s.Rotation = &Rotation{Kind: RotateKubernetesCA, Phase: PhaseRefresh, Switched: now}
		},
		"an unknown kind": func(s *Secrets) {
			s.Rotation = &Rotation{Kind: "everything", Phase: PhaseAccept}
		},
		"an unknown phase": func(s *Secrets) {
			s.Rotation = &Rotation{Kind: RotateOSCA, Phase: "done"}
		},
		"values of another kind accepted": func(s *Secrets) {
			s.Kubernetes.Accepted.EncryptionKeys = []EncryptionKey{{Name: "old", Key: make([]byte, EncryptionKeySize)}}
			s.Rotation = &Rotation{Kind: RotateOSCA, Phase: PhaseFinish}
		},
		"two encryption keys of one name": func(s *Secrets) {
			s.Kubernetes.Accepted.EncryptionKeys = []EncryptionKey{{Name: s.Kubernetes.KeyName(), Key: make([]byte, EncryptionKeySize)}}
			s.Rotation = &Rotation{Kind: RotateEncryptionKey, Phase: PhaseRefresh, Switched: now}
		},
		"an encryption key name with a colon": func(s *Secrets) {
			s.Kubernetes.EncryptionKeyName = "a:b"
		},
		"a short accepted encryption key": func(s *Secrets) {
			s.Kubernetes.Accepted.EncryptionKeys = []EncryptionKey{{Name: "old", Key: []byte("short")}}
			s.Rotation = &Rotation{Kind: RotateEncryptionKey, Phase: PhaseRefresh, Switched: now}
		},
		"a repeated service-account key": func(s *Secrets) {
			pub, _ := ServiceAccountPublicKey(s.Kubernetes.ServiceAccountKey)
			s.Kubernetes.Accepted.ServiceAccountKeys = []string{pub}
			s.Rotation = &Rotation{Kind: RotateServiceAccountKey, Phase: PhaseRefresh, Switched: now}
		},
		"a switch without its time": func(s *Secrets) {
			pub, _ := ServiceAccountPublicKey(other.Kubernetes.ServiceAccountKey)
			s.Kubernetes.Accepted.ServiceAccountKeys = []string{pub}
			s.Rotation = &Rotation{Kind: RotateServiceAccountKey, Phase: PhaseSwitch}
		},
		"new values after the switch": func(s *Secrets) {
			s.Accepted.OSCA = []string{other.OSCA.Certificate}
			s.Rotation = &Rotation{Kind: RotateOSCA, Phase: PhaseSwitch, Switched: now, New: &NewValues{OSCA: &other.OSCA, NodeCA: &other.NodeCA}}
		},
		"an accept phase without new values": func(s *Secrets) {
			s.Accepted.OSCA = []string{other.OSCA.Certificate}
			s.Rotation = &Rotation{Kind: RotateOSCA, Phase: PhaseAccept}
		},
		"a new OS CA that is not accepted": func(s *Secrets) {
			third := generate(t)
			s.Accepted.OSCA = []string{third.OSCA.Certificate}
			s.Rotation = &Rotation{Kind: RotateOSCA, Phase: PhaseAccept, New: &NewValues{OSCA: &other.OSCA, NodeCA: &other.NodeCA}}
		},
		"new values of another kind": func(s *Secrets) {
			s.Accepted.OSCA = []string{other.OSCA.Certificate}
			s.Rotation = &Rotation{Kind: RotateOSCA, Phase: PhaseAccept, New: &NewValues{OSCA: &other.OSCA, NodeCA: &other.NodeCA, ServiceAccountKey: other.Kubernetes.ServiceAccountKey}}
		},
		"a new OS CA in a rotation of the Kubernetes CAs": func(s *Secrets) {
			k := other.Kubernetes
			s.Kubernetes.Accepted.CA, s.Kubernetes.Accepted.FrontProxyCA, s.Kubernetes.Accepted.EtcdCA = []string{k.CA.Certificate}, []string{k.FrontProxyCA.Certificate}, []string{k.EtcdCA.Certificate}
			s.Rotation = &Rotation{Kind: RotateKubernetesCA, Phase: PhaseAccept, New: &NewValues{CA: &k.CA, FrontProxyCA: &k.FrontProxyCA, EtcdCA: &k.EtcdCA, OSCA: &other.OSCA}}
		},
		"two accepted OS CAs": func(s *Secrets) {
			s.Accepted.OSCA = []string{other.OSCA.Certificate, generate(t).OSCA.Certificate}
			s.Rotation = &Rotation{Kind: RotateOSCA, Phase: PhaseRefresh, Switched: now}
		},
		"two accepted Kubernetes CAs": func(s *Secrets) {
			k := other.Kubernetes
			s.Kubernetes.Accepted.CA, s.Kubernetes.Accepted.FrontProxyCA, s.Kubernetes.Accepted.EtcdCA = []string{k.CA.Certificate, generate(t).Kubernetes.CA.Certificate}, []string{k.FrontProxyCA.Certificate}, []string{k.EtcdCA.Certificate}
			s.Rotation = &Rotation{Kind: RotateKubernetesCA, Phase: PhaseRefresh, Switched: now}
		},
		"the Kubernetes CA accepted without the front-proxy and etcd CAs": func(s *Secrets) {
			s.Kubernetes.Accepted.CA = []string{other.Kubernetes.CA.Certificate}
			s.Rotation = &Rotation{Kind: RotateKubernetesCA, Phase: PhaseRefresh, Switched: now}
		},
		"two accepted encryption keys": func(s *Secrets) {
			s.Kubernetes.Accepted.EncryptionKeys = []EncryptionKey{{Name: "a", Key: make([]byte, EncryptionKeySize)}, {Name: "b", Key: make([]byte, EncryptionKeySize)}}
			s.Rotation = &Rotation{Kind: RotateEncryptionKey, Phase: PhaseRefresh, Switched: now}
		},
		"an accepted Kubernetes CA that is no root": func(s *Secrets) {
			k := other.Kubernetes
			s.Kubernetes.Accepted.CA, s.Kubernetes.Accepted.FrontProxyCA, s.Kubernetes.Accepted.EtcdCA = []string{other.NodeCA.Certificate}, []string{k.FrontProxyCA.Certificate}, []string{k.EtcdCA.Certificate}
			s.Rotation = &Rotation{Kind: RotateKubernetesCA, Phase: PhaseRefresh, Switched: now}
		},
		"an encryption key name with a control character": func(s *Secrets) {
			s.Kubernetes.EncryptionKeyName = "a\x01b"
		},
		"an encryption key name with a delete character": func(s *Secrets) {
			s.Kubernetes.EncryptionKeyName = "a\x7fb"
		},
		"a finish that accepts old values": func(s *Secrets) {
			s.Accepted.OSCA = []string{other.OSCA.Certificate}
			s.Rotation = &Rotation{Kind: RotateOSCA, Phase: PhaseFinish, Switched: now}
		},
	} {
		s := generate(t)
		edit(&s)
		err := s.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "PRIVATE KEY") {
			t.Errorf("%s: the error holds a key", name)
		}
	}
}

// TestRotationRedacted checks that nothing a rotation adds to the secrets file appears when they
// are printed.
func TestRotationRedacted(t *testing.T) {
	for _, kind := range []string{RotateServiceAccountKey, RotateEncryptionKey, RotateOSCA} {
		s := generate(t)
		if err := s.BeginRotation(kind, now); err != nil {
			t.Fatal(err)
		}
		var secrets []string
		if n := s.Rotation.New; n != nil {
			secrets = append(secrets, n.ServiceAccountKey)
			if n.OSCA != nil {
				secrets = append(secrets, n.OSCA.Key, n.NodeCA.Key)
			}
		}
		for _, key := range s.Kubernetes.Accepted.EncryptionKeys {
			secrets = append(secrets, base64.StdEncoding.EncodeToString(key.Key), fmt.Sprint(key.Key), string(key.Key))
		}
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			for _, value := range []any{s, s.Kubernetes, s.Kubernetes.Accepted, s.Rotation, *s.Rotation} {
				out := fmt.Sprintf(format, value)
				for _, secret := range secrets {
					if secret != "" && strings.Contains(out, strings.TrimSpace(secret)) {
						t.Errorf("%s: %s of %T holds a secret: %s", kind, format, value, out)
					}
				}
				if strings.Contains(out, "PRIVATE KEY") {
					t.Errorf("%s: %s of %T holds a key: %s", kind, format, value, out)
				}
			}
		}
	}
}

func TestEncryptionKeyFingerprint(t *testing.T) {
	key := EncryptionKey{Name: "a", Key: []byte("0123456789abcdef0123456789abcdef")}
	other := EncryptionKey{Name: "a", Key: []byte("0123456789abcdef0123456789abcdeF")}
	if key.Fingerprint() == other.Fingerprint() || len(key.Fingerprint()) != 64 {
		t.Errorf("fingerprints %s and %s", key.Fingerprint(), other.Fingerprint())
	}
	if strings.Contains(key.Fingerprint(), fmt.Sprintf("%x", key.Key)) {
		t.Error("the fingerprint holds the key")
	}
}
