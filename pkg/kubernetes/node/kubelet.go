package node

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// writeKubeletFiles installs the kubelet's client certificate cert and writes the cluster's CA,
// the kubelet's flags and its configuration. The kubeconfig, which starts the kubelet, is not
// among them.
func writeKubeletFiles(p Paths, c kubernetes.Cluster, n kubernetes.Node, share kpki.Share, cert pki.CertKey, now time.Time) error {
	if err := installKubeletClient(p, share.CA, cert, n.Name, now); err != nil {
		return err
	}
	if err := os.MkdirAll(p.KubeletDir(), 0o755); err != nil {
		return err
	}
	config, err := json.Marshal(kubeletConfig(p, c))
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{
		"ca.crt":      []byte(share.CABundle()),
		"flags":       []byte("KUBELET_ARGS=" + strings.Join(kubeletFlags(c, n), " ") + "\n"),
		"config.json": config,
	} {
		if err := atomicfile.WriteFile(filepath.Join(p.KubeletDir(), name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// kubeletConfiguration is the kubelet's configuration file, its fields in key order.
type kubeletConfiguration struct {
	APIVersion     string `json:"apiVersion"`
	Authentication struct {
		Anonymous enabled `json:"anonymous"`
		Webhook   enabled `json:"webhook"`
		X509      struct {
			ClientCAFile string `json:"clientCAFile"`
		} `json:"x509"`
	} `json:"authentication"`
	Authorization struct {
		Mode string `json:"mode"`
	} `json:"authorization"`
	CgroupDriver             string   `json:"cgroupDriver"`
	ClusterDNS               []string `json:"clusterDNS"`
	ClusterDomain            string   `json:"clusterDomain"`
	ContainerRuntimeEndpoint string   `json:"containerRuntimeEndpoint"`
	Kind                     string   `json:"kind"`
	ProtectKernelDefaults    bool     `json:"protectKernelDefaults"`
	ReadOnlyPort             int      `json:"readOnlyPort"`
	ResolvConf               string   `json:"resolvConf"`
	RotateCertificates       bool     `json:"rotateCertificates"`
	ServerTLSBootstrap       bool     `json:"serverTLSBootstrap"`
	StaticPodPath            string   `json:"staticPodPath"`
	TLSMinVersion            string   `json:"tlsMinVersion"`
}

type enabled struct {
	Enabled bool `json:"enabled"`
}

// kubeletConfig is the kubelet's configuration for the cluster.
func kubeletConfig(p Paths, c kubernetes.Cluster) kubeletConfiguration {
	config := kubeletConfiguration{
		APIVersion:    "kubelet.config.k8s.io/v1beta1",
		Kind:          "KubeletConfiguration",
		ReadOnlyPort:  0,
		TLSMinVersion: "VersionTLS12",
		// The kubelet refuses to start unless the kernel tunables hold its values, rather than
		// changing them itself.
		ProtectKernelDefaults:    true,
		CgroupDriver:             "systemd",
		ClusterDNS:               []string{c.DNSIPs.Of(c.Primary()).String()},
		ClusterDomain:            c.Domain,
		ContainerRuntimeEndpoint: "unix:///run/containerd/containerd.sock",
		// The client certificate from chalkos is renewed by the kubelet; the serving certificate
		// is requested from the cluster and approved by chalkd on the control plane.
		RotateCertificates: true,
		ServerTLSBootstrap: true,
		StaticPodPath:      p.Manifests(),
		// The stub resolver at 127.0.0.53 is unreachable from pods.
		ResolvConf: "/run/systemd/resolve/resolv.conf",
	}
	config.Authentication.Anonymous.Enabled = false
	config.Authentication.Webhook.Enabled = true
	config.Authentication.X509.ClientCAFile = filepath.Join(p.KubeletDir(), "ca.crt")
	config.Authorization.Mode = "Webhook"
	return config
}

// kubeletFlags are the kubelet's node-specific flags, its name, addresses, labels and taints,
// followed by the cluster's extra kubelet flags in name order. Coming last, the extra flags
// override chalkos's, except the protected ones, which Cluster.Validate refuses. Control-plane
// nodes are tainted unless the cluster allows workloads on them.
func kubeletFlags(c kubernetes.Cluster, n kubernetes.Node) []string {
	flags := []string{"--hostname-override=" + n.Name}
	if len(n.IPs) > 0 {
		flags = append(flags, "--node-ip="+joinAddrs(n.IPs))
	}
	var labels []string
	for k, v := range n.Labels {
		labels = append(labels, k+"="+v)
	}
	slices.Sort(labels)
	if len(labels) > 0 {
		flags = append(flags, "--node-labels="+strings.Join(labels, ","))
	}
	var taints []string
	if c.Kind == kubernetes.KindControlPlane && !c.AllowSchedulingOnControlPlanes {
		taints = append(taints, "node-role.kubernetes.io/control-plane:NoSchedule")
	}
	for _, t := range n.Taints {
		taint := t.Key
		if t.Value != "" {
			taint += "=" + t.Value
		}
		taints = append(taints, taint+":"+t.Effect)
	}
	if len(taints) > 0 {
		flags = append(flags, "--register-with-taints="+strings.Join(taints, ","))
	}
	extra := c.ExtraArgs["kubelet"]
	for _, name := range slices.Sorted(maps.Keys(extra)) {
		flags = append(flags, "--"+name+"="+extra[name])
	}
	return flags
}

// joinAddrs lists the addresses, separated by commas.
func joinAddrs(addrs []netip.Addr) string {
	list := make([]string, len(addrs))
	for i, a := range addrs {
		list[i] = a.String()
	}
	return strings.Join(list, ",")
}

// installKubeletClient puts the certificate into the kubelet's certificate store unless the
// store holds one the kubelet renewed itself that is trusted and expires later.
func installKubeletClient(p Paths, ca pki.CertKey, ck pki.CertKey, node string, now time.Time) error {
	cert, _, err := ck.Parse()
	if err != nil {
		return fmt.Errorf("kubelet client certificate: %w", err)
	}
	switch err := checkRenewedClient(p.KubeletClient(), ca, cert, node, now); {
	case err == nil:
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		log.Printf("kubelet: installing the share's client certificate instead of %s: %v", p.KubeletClient(), err)
	}
	if err := os.MkdirAll(p.KubeletPKI, 0o700); err != nil {
		return err
	}
	// One file, replaced atomically; the kubelet writes renewed certificates next to it.
	name := "kubelet-client-chalkos.pem"
	if err := atomicfile.WriteFile(filepath.Join(p.KubeletPKI, name), []byte(ck.Certificate+ck.Key), 0o600); err != nil {
		return err
	}
	// The kubelet's store follows the link and refuses to replace a file that is not one.
	tmp := p.KubeletClient() + ".chalkos"
	os.Remove(tmp)
	if err := os.Symlink(name, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, p.KubeletClient())
}

// checkRenewedClient returns nil if the kubelet's current client certificate in path should be
// kept: it was issued by ca for node with its own key, is valid now and expires after want.
// Errors never include the file's contents.
func checkRenewedClient(path string, ca pki.CertKey, want *x509.Certificate, node string, now time.Time) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// The kubelet stores the certificate and its key in one file; X509KeyPair also checks that
	// the key belongs to the certificate, whatever encoding the key has.
	pair, err := tls.X509KeyPair(data, data)
	if err != nil {
		return fmt.Errorf("not a certificate with its key: %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	caCert, err := pki.ParseCertificate([]byte(ca.Certificate))
	if err != nil {
		return fmt.Errorf("the cluster CA: %w", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: now}); err != nil {
		return err
	}
	if cert.Subject.CommonName != kpki.NodeUserPrefix+node || !slices.Equal(cert.Subject.Organization, []string{kpki.NodesGroup}) {
		return fmt.Errorf("the certificate is for %q in groups %q, want node %s in %q only", cert.Subject.CommonName, cert.Subject.Organization, node, kpki.NodesGroup)
	}
	if !cert.NotAfter.After(want.NotAfter) {
		return errors.New("it expires no later than the share's certificate")
	}
	return nil
}
