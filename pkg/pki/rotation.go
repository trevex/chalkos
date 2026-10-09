package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// What chalkctl rotate rotates.
const (
	RotateOSCA              = "os-ca"
	RotateKubernetesCA      = "kubernetes-ca"
	RotateServiceAccountKey = "service-account-key"
	RotateEncryptionKey     = "encryption-key"
)

// RotationKinds lists what can be rotated, in the order chalkctl names them.
var RotationKinds = []string{RotateOSCA, RotateKubernetesCA, RotateServiceAccountKey, RotateEncryptionKey}

// RotationName names what a kind of rotation rotates in prose.
func RotationName(kind string) string {
	switch kind {
	case RotateOSCA:
		return "OS CA"
	case RotateKubernetesCA:
		return "Kubernetes CAs"
	case RotateServiceAccountKey:
		return "service-account key"
	case RotateEncryptionKey:
		return "encryption key"
	}
	return kind
}

// The phases of a rotation, in order. Accept makes every node trust the new value besides the
// old one; Switch issues or signs with the new value; Refresh issues again what the old value
// issued; Finish removes the old value.
const (
	PhaseAccept  = "accept"
	PhaseSwitch  = "switch"
	PhaseRefresh = "refresh"
	PhaseFinish  = "finish"
)

var phases = []string{PhaseAccept, PhaseSwitch, PhaseRefresh, PhaseFinish}

// DefaultEncryptionKeyName names the encryption key of a secrets file that names none: the name
// every key had before keys rotated.
const DefaultEncryptionKeyName = "chalkos"

// Accepted holds the OS CAs nodes and chalkctl still trust besides the one that issues, by
// certificate.
type Accepted struct {
	OSCA []string `json:"osCA,omitempty"`
}

// KubernetesAccepted holds what the Kubernetes components still trust besides what issues or
// signs: CA certificates, service-account public keys (PEM) and encryption keys, which decrypt
// but no longer encrypt.
type KubernetesAccepted struct {
	CA                 []string        `json:"ca,omitempty"`
	FrontProxyCA       []string        `json:"frontProxyCA,omitempty"`
	EtcdCA             []string        `json:"etcdCA,omitempty"`
	ServiceAccountKeys []string        `json:"serviceAccountKeys,omitempty"`
	EncryptionKeys     []EncryptionKey `json:"encryptionKeys,omitempty"`
}

// String counts what is accepted and never prints an encryption key.
func (a KubernetesAccepted) String() string {
	return fmt.Sprintf("pki.KubernetesAccepted{ca: %d, frontProxyCA: %d, etcdCA: %d, serviceAccountKeys: %d, encryptionKeys: %v}",
		len(a.CA), len(a.FrontProxyCA), len(a.EtcdCA), len(a.ServiceAccountKeys), a.EncryptionKeys)
}

// GoString redacts like String.
func (a KubernetesAccepted) GoString() string { return a.String() }

// EncryptionKey is a key the API server decrypts with, under the name its ciphertexts carry.
type EncryptionKey struct {
	Name string `json:"name"`
	Key  []byte `json:"key"`
}

// String names the key and never prints it.
func (k EncryptionKey) String() string {
	return fmt.Sprintf("pki.EncryptionKey{name: %s, redacted}", k.Name)
}

// GoString redacts like String.
func (k EncryptionKey) GoString() string { return k.String() }

// Fingerprint identifies the key without revealing it: a hash of the key, apart from the hashes
// of certificates.
func (k EncryptionKey) Fingerprint() string {
	sum := sha256.Sum256(append([]byte("chalkos encryption key\x00"), k.Key...))
	return hex.EncodeToString(sum[:])
}

// Rotation records a rotation that runs: what rotates, the phase it reached, and the new values'
// keys until the switch makes them the ones that issue.
type Rotation struct {
	Kind  string `json:"kind"`
	Phase string `json:"phase"`
	// Applied is set once every node confirmed the phase.
	Applied bool `json:"applied,omitempty"`
	// Switched is when every node applied the switch: tokens signed with the old key until then
	// are renewed within an hour of it.
	Switched time.Time `json:"switched,omitzero"`
	// New holds the new CAs with their keys and the new service-account key during the accept
	// phase; their certificates and public keys are among the accepted values meanwhile. A new
	// encryption key is accepted with its key and needs nothing here.
	New *NewValues `json:"new,omitempty"`
}

// String names the rotation and its phase.
func (r Rotation) String() string {
	return fmt.Sprintf("pki.Rotation{kind: %s, phase: %s, applied: %v}", r.Kind, r.Phase, r.Applied)
}

// GoString redacts like String.
func (r Rotation) GoString() string { return r.String() }

// NewValues are the new values of a rotation in its accept phase.
type NewValues struct {
	OSCA              *CertKey `json:"osCA,omitempty"`
	NodeCA            *CertKey `json:"nodeCA,omitempty"`
	CA                *CertKey `json:"ca,omitempty"`
	FrontProxyCA      *CertKey `json:"frontProxyCA,omitempty"`
	EtcdCA            *CertKey `json:"etcdCA,omitempty"`
	ServiceAccountKey string   `json:"serviceAccountKey,omitempty"`
}

// String never prints a key.
func (n NewValues) String() string { return "pki.NewValues{redacted}" }

// GoString redacts like String.
func (n NewValues) GoString() string { return n.String() }

// onlyOf reports whether the new values hold nothing but values of kind.
func (n NewValues) onlyOf(kind string) bool {
	of := map[string]bool{
		RotateOSCA:              n.OSCA != nil || n.NodeCA != nil,
		RotateKubernetesCA:      n.CA != nil || n.FrontProxyCA != nil || n.EtcdCA != nil,
		RotateServiceAccountKey: n.ServiceAccountKey != "",
	}
	for k, set := range of {
		if set && k != kind {
			return false
		}
	}
	return true
}

// OSCABundle is the OS CAs nodes and chalkctl trust: the one that issues, then those accepted.
func (s Secrets) OSCABundle() string {
	return Bundle(append([]string{s.OSCA.Certificate}, s.Accepted.OSCA...)...)
}

// CABundle is the Kubernetes CAs trusted, the one that issues first; FrontProxyCABundle and
// EtcdCABundle are those of the front-proxy and etcd CAs.
func (k KubernetesSecrets) CABundle() string {
	return Bundle(append([]string{k.CA.Certificate}, k.Accepted.CA...)...)
}

func (k KubernetesSecrets) FrontProxyCABundle() string {
	return Bundle(append([]string{k.FrontProxyCA.Certificate}, k.Accepted.FrontProxyCA...)...)
}

func (k KubernetesSecrets) EtcdCABundle() string {
	return Bundle(append([]string{k.EtcdCA.Certificate}, k.Accepted.EtcdCA...)...)
}

// KeyName is the name of the encryption key that encrypts.
func (k KubernetesSecrets) KeyName() string {
	if k.EncryptionKeyName == "" {
		return DefaultEncryptionKeyName
	}
	return k.EncryptionKeyName
}

// EncryptionKeys are the keys the API server decrypts with, the one that encrypts first.
func (k KubernetesSecrets) EncryptionKeys() []EncryptionKey {
	return append([]EncryptionKey{{Name: k.KeyName(), Key: k.EncryptionKey}}, k.Accepted.EncryptionKeys...)
}

// ServiceAccountPublicKey returns the PEM public key of a PEM private service-account key.
func ServiceAccountPublicKey(private string) (string, error) {
	key, err := ParseECKey(private)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// ParsePublicKey decodes a PEM ECDSA P-256 public key, as service accounts' are.
func ParsePublicKey(data string) (*ecdsa.PublicKey, error) {
	block, rest := pem.Decode([]byte(data))
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("not one PEM public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("the public key is not ECDSA P-256")
	}
	return key, nil
}

// PublicKeyFingerprint is the SHA-256 of a PEM public key's DER form.
func PublicKeyFingerprint(data string) (string, error) {
	key, err := ParsePublicKey(data)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", err
	}
	return Fingerprint(der), nil
}

// ServiceAccountPublicKeys are the public keys the API server accepts tokens of, the signing
// key's first.
func (k KubernetesSecrets) ServiceAccountPublicKeys() ([]string, error) {
	pub, err := ServiceAccountPublicKey(k.ServiceAccountKey)
	if err != nil {
		return nil, err
	}
	return append([]string{pub}, k.Accepted.ServiceAccountKeys...), nil
}

// validateAccepted checks the accepted values: certificates of CAs, P-256 public keys, and
// encryption keys of the right size under names of their own that the API server can tell apart.
func (s Secrets) validateAccepted() error {
	for i, c := range s.Accepted.OSCA {
		if err := validateRoot(c); err != nil {
			return fmt.Errorf("accepted.osCA[%d]: %w", i, err)
		}
	}
	return s.Kubernetes.ValidateAccepted()
}

// ValidateAccepted checks what the Kubernetes components accept besides what issues or signs:
// certificates of CAs, P-256 public keys, and encryption keys of the right size under names of
// their own that the API server can tell apart. A worker's share holds CA certificates alone and
// no service-account or encryption key, which ValidateAccepted does not ask for.
func (k KubernetesSecrets) ValidateAccepted() error {
	for _, list := range []struct {
		name  string
		certs []string
	}{{"kubernetes.accepted.ca", k.Accepted.CA}, {"kubernetes.accepted.frontProxyCA", k.Accepted.FrontProxyCA}, {"kubernetes.accepted.etcdCA", k.Accepted.EtcdCA}} {
		for i, c := range list.certs {
			if err := validateRoot(c); err != nil {
				return fmt.Errorf("%s[%d]: %w", list.name, i, err)
			}
		}
	}
	if k.ServiceAccountKey == "" && len(k.EncryptionKey) == 0 {
		if len(k.Accepted.ServiceAccountKeys) > 0 || len(k.Accepted.EncryptionKeys) > 0 {
			return errors.New("kubernetes.accepted holds keys without the keys they are accepted besides")
		}
		return nil
	}
	signing, err := ServiceAccountPublicKey(k.ServiceAccountKey)
	if err != nil {
		return fmt.Errorf("kubernetes.serviceAccountKey: %w", err)
	}
	seen := map[string]bool{}
	for i, pub := range append([]string{signing}, k.Accepted.ServiceAccountKeys...) {
		fp, err := PublicKeyFingerprint(pub)
		if err != nil {
			return fmt.Errorf("kubernetes.accepted.serviceAccountKeys[%d]: %w", i-1, err)
		}
		if seen[fp] {
			return errors.New("kubernetes.accepted.serviceAccountKeys repeats a key")
		}
		seen[fp] = true
	}
	names := map[string]bool{}
	for i, key := range k.EncryptionKeys() {
		if err := validKeyName(key.Name); err != nil {
			return fmt.Errorf("the encryption key %d: %w", i, err)
		}
		if names[key.Name] {
			return fmt.Errorf("two encryption keys are named %s; the API server tells them apart by name", key.Name)
		}
		names[key.Name] = true
		if len(key.Key) != EncryptionKeySize {
			return fmt.Errorf("the encryption key %s must be %d bytes", key.Name, EncryptionKeySize)
		}
	}
	return nil
}

// validKeyName checks a name the API server prefixes ciphertexts with, k8s:enc:secretbox:v1:<name>:,
// and that chalkctl prints: no colon, space or control character.
func validKeyName(name string) error {
	if name == "" || strings.ContainsFunc(name, func(r rune) bool { return r == ':' || unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("the name %q is empty or holds a colon, space or control character", name)
	}
	return nil
}

// validateRoot checks one certificate of an OS CA without its key: a self-signed CA that no node
// CA is. A node CA trusted as a root would make what control planes issue verify as the OS CA's
// own client certificates.
func validateRoot(certificate string) error {
	if err := validateCACertificate(certificate); err != nil {
		return err
	}
	cert, err := ParseCertificate([]byte(certificate))
	if err != nil {
		return err
	}
	if IsNodeCA(cert) || cert.CheckSignatureFrom(cert) != nil {
		return errors.New("not a self-signed root CA")
	}
	return nil
}

// validateCACertificate checks one CA certificate without its key.
func validateCACertificate(certificate string) error {
	certs, err := ParseBundle(certificate)
	if err != nil {
		return err
	}
	if len(certs) != 1 {
		return errors.New("not one certificate")
	}
	cert := certs[0]
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return errors.New("not a CA certificate")
	}
	return nil
}

// cas lists every CA of the secrets file, also those accepted and new, for RequireDistinctCAs.
func (s Secrets) cas() []NamedCA {
	cas := append([]NamedCA{{"osCA", s.OSCA}, {"nodeCA", s.NodeCA}}, s.Kubernetes.cas()...)
	add := func(name string, certs []string) {
		for i, c := range certs {
			cas = append(cas, NamedCA{fmt.Sprintf("%s[%d]", name, i), CertKey{Certificate: c}})
		}
	}
	add("accepted.osCA", s.Accepted.OSCA)
	add("kubernetes.accepted.ca", s.Kubernetes.Accepted.CA)
	add("kubernetes.accepted.frontProxyCA", s.Kubernetes.Accepted.FrontProxyCA)
	add("kubernetes.accepted.etcdCA", s.Kubernetes.Accepted.EtcdCA)
	if r := s.Rotation; r != nil && r.New != nil && r.New.NodeCA != nil {
		cas = append(cas, NamedCA{"rotation.new.nodeCA", *r.New.NodeCA})
	}
	return cas
}

// validateRotation checks that a running rotation is one chalkctl recorded: a known kind and
// phase, the new values in the accept phase only, each one accepted meanwhile, and old values
// accepted for no other kind.
func (s Secrets) validateRotation() error {
	r := s.Rotation
	if r == nil {
		if s.accepting() != "" {
			return fmt.Errorf("the secrets file accepts old values of the %s, but no rotation runs", RotationName(s.accepting()))
		}
		return nil
	}
	if !slices.Contains(RotationKinds, r.Kind) {
		return fmt.Errorf("rotation: unknown kind %q", r.Kind)
	}
	if !slices.Contains(phases, r.Phase) {
		return fmt.Errorf("rotation: unknown phase %q", r.Phase)
	}
	if other := s.acceptingOtherThan(r.Kind); other != "" {
		return fmt.Errorf("the secrets file accepts old values of the %s, but the rotation is of the %s", RotationName(other), RotationName(r.Kind))
	}
	switch {
	case r.Kind == RotateEncryptionKey && r.New != nil:
		return errors.New("rotation: a new encryption key is accepted, not held apart")
	case r.Kind != RotateEncryptionKey && (r.Phase == PhaseAccept) != (r.New != nil):
		return errors.New("rotation: new values belong to the accept phase only")
	case r.New != nil && !r.New.onlyOf(r.Kind):
		return fmt.Errorf("rotation: the new values hold values of another kind than the %s", RotationName(r.Kind))
	}

	if (r.Phase == PhaseSwitch && r.Applied || r.Phase == PhaseRefresh) && r.Switched.IsZero() {
		return errors.New("rotation: the switch has no time")
	}
	accepted := s.acceptedOf(r.Kind)
	if r.Phase == PhaseFinish {
		if accepted > 0 {
			return errors.New("rotation: the finish accepts old values")
		}
		return nil
	}
	// chalkctl accepts exactly one value besides each issuing one: the new value before the
	// switch, the old one after it.
	for _, n := range s.acceptedCounts(r.Kind) {
		if n != 1 {
			return fmt.Errorf("rotation: the %s phase accepts %d values besides one that issues; a rotation accepts exactly one", r.Phase, n)
		}
	}
	if r.Phase != PhaseAccept {
		return nil
	}
	n := r.New
	switch r.Kind {
	case RotateOSCA:
		if n.OSCA == nil || n.NodeCA == nil {
			return errors.New("rotation: the new OS CA and node CA are missing")
		}
		if err := ValidateCA(*n.OSCA); err != nil {
			return fmt.Errorf("rotation.new.osCA: %w", err)
		}
		if err := ValidateNodeCA(*n.NodeCA, n.OSCA.Certificate); err != nil {
			return fmt.Errorf("rotation.new.nodeCA: %w", err)
		}
		return requireAccepted("rotation.new.osCA", n.OSCA.Certificate, s.Accepted.OSCA)
	case RotateKubernetesCA:
		for _, ca := range []struct {
			name     string
			ca       *CertKey
			accepted []string
		}{{"ca", n.CA, s.Kubernetes.Accepted.CA}, {"frontProxyCA", n.FrontProxyCA, s.Kubernetes.Accepted.FrontProxyCA}, {"etcdCA", n.EtcdCA, s.Kubernetes.Accepted.EtcdCA}} {
			if ca.ca == nil {
				return fmt.Errorf("rotation.new.%s is missing", ca.name)
			}
			if err := ValidateCA(*ca.ca); err != nil {
				return fmt.Errorf("rotation.new.%s: %w", ca.name, err)
			}
			if err := requireAccepted("rotation.new."+ca.name, ca.ca.Certificate, ca.accepted); err != nil {
				return err
			}
		}
	case RotateServiceAccountKey:
		pub, err := ServiceAccountPublicKey(n.ServiceAccountKey)
		if err != nil {
			return fmt.Errorf("rotation.new.serviceAccountKey: %w", err)
		}
		return requireAccepted("rotation.new.serviceAccountKey", pub, s.Kubernetes.Accepted.ServiceAccountKeys)
	}
	return nil
}

// requireAccepted checks that a new value is among the accepted ones while it is accepted.
func requireAccepted(name, value string, accepted []string) error {
	if !slices.ContainsFunc(accepted, func(a string) bool { return strings.TrimSpace(a) == strings.TrimSpace(value) }) {
		return fmt.Errorf("%s is not accepted", name)
	}
	return nil
}

// acceptedOf counts the values accepted besides the issuing one for a kind.
func (s Secrets) acceptedOf(kind string) int {
	total := 0
	for _, n := range s.acceptedCounts(kind) {
		total += n
	}
	return total
}

// acceptedCounts counts the values accepted besides each issuing one of a kind: one count per CA
// or key that rotates.
func (s Secrets) acceptedCounts(kind string) []int {
	a := s.Kubernetes.Accepted
	switch kind {
	case RotateOSCA:
		return []int{len(s.Accepted.OSCA)}
	case RotateKubernetesCA:
		return []int{len(a.CA), len(a.FrontProxyCA), len(a.EtcdCA)}
	case RotateServiceAccountKey:
		return []int{len(a.ServiceAccountKeys)}
	case RotateEncryptionKey:
		return []int{len(a.EncryptionKeys)}
	}
	return nil
}

// accepting names a kind with accepted values, "" when there is none.
func (s Secrets) accepting() string {
	return s.acceptingOtherThan("")
}

func (s Secrets) acceptingOtherThan(kind string) string {
	for _, k := range RotationKinds {
		if k != kind && s.acceptedOf(k) > 0 {
			return k
		}
	}
	return ""
}

// ErrRotationRuns means a rotation runs already; one rotation runs at a time.
type ErrRotationRuns struct{ Kind, Phase string }

func (e *ErrRotationRuns) Error() string {
	return fmt.Sprintf("a rotation of the %s runs, in its %s phase; continue it with chalkctl rotate %s --resume or --finish", RotationName(e.Kind), e.Phase, e.Kind)
}

// BeginRotation starts rotating kind: it creates the new value and accepts it besides the one
// that issues, in the accept phase.
func (s *Secrets) BeginRotation(kind string, now time.Time) error {
	if r := s.Rotation; r != nil {
		return &ErrRotationRuns{r.Kind, r.Phase}
	}
	k := &s.Kubernetes
	n := &NewValues{}
	switch kind {
	case RotateOSCA:
		root, err := NewOSCA(now)
		if err != nil {
			return err
		}
		nodeCA, err := NewNodeCA(root, now)
		if err != nil {
			return err
		}
		n.OSCA, n.NodeCA = &root, &nodeCA
		s.Accepted.OSCA = append(s.Accepted.OSCA, root.Certificate)
	case RotateKubernetesCA:
		for _, ca := range []struct {
			name     string
			new      **CertKey
			accepted *[]string
		}{{"chalkos Kubernetes CA", &n.CA, &k.Accepted.CA}, {"chalkos front-proxy CA", &n.FrontProxyCA, &k.Accepted.FrontProxyCA}, {"chalkos etcd CA", &n.EtcdCA, &k.Accepted.EtcdCA}} {
			created, err := NewCA(ca.name, now)
			if err != nil {
				return err
			}
			*ca.new = &created
			*ca.accepted = append(*ca.accepted, created.Certificate)
		}
	case RotateServiceAccountKey:
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		if n.ServiceAccountKey, err = EncodeKey(key); err != nil {
			return err
		}
		pub, err := ServiceAccountPublicKey(n.ServiceAccountKey)
		if err != nil {
			return err
		}
		k.Accepted.ServiceAccountKeys = append(k.Accepted.ServiceAccountKeys, pub)
	case RotateEncryptionKey:
		key := make([]byte, EncryptionKeySize)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		// A name of its own for every key: ciphertexts carry it, and one under a name used before
		// would be tried with the wrong key.
		name := "chalkos-" + now.UTC().Format("20060102t150405z")
		for i := 2; slices.ContainsFunc(k.EncryptionKeys(), func(e EncryptionKey) bool { return e.Name == name }); i++ {
			name = fmt.Sprintf("chalkos-%s-%d", now.UTC().Format("20060102t150405z"), i)
		}
		k.Accepted.EncryptionKeys = append(k.Accepted.EncryptionKeys, EncryptionKey{Name: name, Key: key})
		n = nil
	default:
		return fmt.Errorf("unknown rotation %q; rotate one of %s", kind, strings.Join(RotationKinds, ", "))
	}
	s.Rotation = &Rotation{Kind: kind, Phase: PhaseAccept, New: n}
	return s.Validate()
}

// requirePhase refuses the next phase until every node applied phase.
func (s *Secrets) requirePhase(phase string) error {
	r := s.Rotation
	if r == nil {
		return errors.New("no rotation runs")
	}
	if r.Phase != phase || !r.Applied {
		state := "not applied on every node yet"
		if r.Applied {
			state = "applied"
		}
		return fmt.Errorf("the rotation of the %s is in its %s phase, %s; the next phase follows the %s phase applied on every node", RotationName(r.Kind), r.Phase, state, phase)
	}
	return nil
}

// RecordApplied records that every node applied the phase the rotation is in, at now. The switch
// keeps the time: tokens signed with the old service-account key are renewed within an hour of
// it, and some control planes signed with the old key until every node applied it.
func (s *Secrets) RecordApplied(now time.Time) error {
	r := s.Rotation
	if r == nil {
		return errors.New("no rotation runs")
	}
	r.Applied = true
	if r.Phase == PhaseSwitch {
		r.Switched = now.UTC()
	}
	return s.Validate()
}

// ClientCA is the OS CA client files are issued from: the new one once every node accepts it, so
// a client file issued during the rotation keeps working after its finish, else the issuing one.
func (s Secrets) ClientCA() CertKey {
	if r := s.Rotation; r != nil && r.Kind == RotateOSCA && r.Phase == PhaseAccept && r.Applied && r.New != nil && r.New.OSCA != nil {
		return *r.New.OSCA
	}
	return s.OSCA
}

// KubeconfigCA is the Kubernetes CA kubeconfigs' client certificates are issued from, chosen as
// ClientCA chooses the OS CA.
func (s Secrets) KubeconfigCA() CertKey {
	if r := s.Rotation; r != nil && r.Kind == RotateKubernetesCA && r.Phase == PhaseAccept && r.Applied && r.New != nil && r.New.CA != nil {
		return *r.New.CA
	}
	return s.Kubernetes.CA
}

// SwitchRotation makes the new value the one that issues or signs and accepts the old one in its
// place.
func (s *Secrets) SwitchRotation() error {
	if err := s.requirePhase(PhaseAccept); err != nil {
		return err
	}
	r := s.Rotation
	k := &s.Kubernetes
	switch r.Kind {
	case RotateOSCA:
		s.Accepted.OSCA = []string{s.OSCA.Certificate}
		s.OSCA, s.NodeCA = *r.New.OSCA, *r.New.NodeCA
	case RotateKubernetesCA:
		k.Accepted.CA, k.Accepted.FrontProxyCA, k.Accepted.EtcdCA = []string{k.CA.Certificate}, []string{k.FrontProxyCA.Certificate}, []string{k.EtcdCA.Certificate}
		k.CA, k.FrontProxyCA, k.EtcdCA = *r.New.CA, *r.New.FrontProxyCA, *r.New.EtcdCA
	case RotateServiceAccountKey:
		old, err := ServiceAccountPublicKey(k.ServiceAccountKey)
		if err != nil {
			return err
		}
		k.ServiceAccountKey, k.Accepted.ServiceAccountKeys = r.New.ServiceAccountKey, []string{old}
	case RotateEncryptionKey:
		if len(k.Accepted.EncryptionKeys) != 1 {
			return errors.New("the rotation of the encryption key accepts no single new key")
		}
		next := k.Accepted.EncryptionKeys[0]
		k.Accepted.EncryptionKeys = []EncryptionKey{{Name: k.KeyName(), Key: k.EncryptionKey}}
		k.EncryptionKey, k.EncryptionKeyName = next.Key, next.Name
	}
	s.Rotation = &Rotation{Kind: r.Kind, Phase: PhaseSwitch}
	return s.Validate()
}

// RefreshRotation moves a rotation whose switch every node applied to the refresh phase.
func (s *Secrets) RefreshRotation() error {
	if err := s.requirePhase(PhaseSwitch); err != nil {
		return err
	}
	s.Rotation.Phase, s.Rotation.Applied = PhaseRefresh, false
	return s.Validate()
}

// FinishRotation stops accepting the old value of a rotation whose refresh every node applied.
func (s *Secrets) FinishRotation() error {
	if err := s.requirePhase(PhaseRefresh); err != nil {
		return err
	}
	k := &s.Kubernetes
	switch s.Rotation.Kind {
	case RotateOSCA:
		s.Accepted.OSCA = nil
	case RotateKubernetesCA:
		k.Accepted.CA, k.Accepted.FrontProxyCA, k.Accepted.EtcdCA = nil, nil, nil
	case RotateServiceAccountKey:
		k.Accepted.ServiceAccountKeys = nil
	case RotateEncryptionKey:
		k.Accepted.EncryptionKeys = nil
	}
	s.Rotation.Phase, s.Rotation.Applied = PhaseFinish, false
	return s.Validate()
}

// EndRotation forgets a rotation whose finish every node applied.
func (s *Secrets) EndRotation() error {
	if err := s.requirePhase(PhaseFinish); err != nil {
		return err
	}
	s.Rotation = nil
	return s.Validate()
}
