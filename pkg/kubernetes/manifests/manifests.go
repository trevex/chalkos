// Package manifests renders the static pods of a chalkos control-plane node: etcd, the API
// server, the controller-manager and the scheduler, run by the kubelet from the upstream images.
package manifests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

const (
	// PKIDir is where chalkd writes the certificates on the node.
	PKIDir = "/run/chalkos/kubernetes/pki"
	// EtcdDataDir holds etcd's data on VAR.
	EtcdDataDir = "/var/lib/etcd"
	// podPKIDir is where the pods see PKIDir.
	podPKIDir = "/etc/kubernetes/pki"
	// CertificatesAnnotation carries a hash of the certificates a pod mounts, so the kubelet
	// replaces the pod when they are issued again.
	CertificatesAnnotation = "chalkos.dev/certificates"
)

// StaticPods renders the static pods by file name. files are the control plane's certificates
// as pki.ControlPlane issues them.
func StaticPods(c kubernetes.Cluster, n kubernetes.Node, files map[string][]byte) (map[string][]byte, error) {
	if n.IP == nil {
		return nil, errors.New("a control-plane node needs an address (kubernetes.nodeIP)")
	}
	ip := n.IP.String()
	pki := func(file string) string { return path.Join(podPKIDir, file) }

	etcd := pod("etcd", c.Images.Etcd, flags(c, "etcd", map[string]string{
		"name":                        n.Name,
		"data-dir":                    EtcdDataDir,
		"advertise-client-urls":       "https://" + net.JoinHostPort(ip, "2379"),
		"initial-advertise-peer-urls": "https://" + net.JoinHostPort(ip, "2380"),
		"initial-cluster":             n.Name + "=https://" + net.JoinHostPort(ip, "2380"),
		// A member with data ignores the initial cluster, so a restart never starts a new one.
		"initial-cluster-state": "new",
		"listen-client-urls":    "https://127.0.0.1:2379,https://" + net.JoinHostPort(ip, "2379"),
		"listen-peer-urls":      "https://" + net.JoinHostPort(ip, "2380"),
		"listen-metrics-urls":   "http://127.0.0.1:2381",
		"cert-file":             pki(kpki.FileEtcdServer),
		"key-file":              pki(kpki.FileEtcdServerKey),
		"trusted-ca-file":       pki(kpki.FileEtcdCA),
		"client-cert-auth":      "true",
		"peer-cert-file":        pki(kpki.FileEtcdPeer),
		"peer-key-file":         pki(kpki.FileEtcdPeerKey),
		"peer-trusted-ca-file":  pki(kpki.FileEtcdCA),
		"peer-client-cert-auth": "true",
		"snapshot-count":        "10000",
	}), "127.0.0.1", 2381, corev1.URISchemeHTTP, "/livez", "/readyz", "/readyz", "100m", "100Mi")
	mountDir(etcd, "etcd-data", EtcdDataDir, EtcdDataDir, false, corev1.HostPathDirectoryOrCreate)
	mountDir(etcd, "etcd-certs", path.Join(PKIDir, "etcd"), path.Join(podPKIDir, "etcd"), true, corev1.HostPathDirectory)

	apiServer := pod("kube-apiserver", c.Images.KubeAPIServer, flags(c, "kube-apiserver", map[string]string{
		"advertise-address":                  ip,
		"allow-privileged":                   "true",
		"authentication-config":              pki(kpki.FileAuthenticationConfig),
		"authorization-mode":                 "Node,RBAC",
		"client-ca-file":                     pki(kpki.FileCA),
		"enable-admission-plugins":           "NodeRestriction",
		"enable-bootstrap-token-auth":        "false",
		"encryption-provider-config":         pki(kpki.FileEncryptionConfig),
		"etcd-cafile":                        pki(kpki.FileEtcdCA),
		"etcd-certfile":                      pki(kpki.FileAPIServerEtcdClient),
		"etcd-keyfile":                       pki(kpki.FileAPIServerEtcdClientKey),
		"etcd-servers":                       "https://127.0.0.1:2379",
		"kubelet-certificate-authority":      pki(kpki.FileCA),
		"kubelet-client-certificate":         pki(kpki.FileAPIServerKubeletClient),
		"kubelet-client-key":                 pki(kpki.FileAPIServerKubeletKey),
		"kubelet-preferred-address-types":    "InternalIP,ExternalIP,Hostname",
		"profiling":                          "false",
		"proxy-client-cert-file":             pki(kpki.FileFrontProxyClient),
		"proxy-client-key-file":              pki(kpki.FileFrontProxyClientKey),
		"requestheader-allowed-names":        kpki.FrontProxyClientUser,
		"requestheader-client-ca-file":       pki(kpki.FileFrontProxyCA),
		"requestheader-extra-headers-prefix": "X-Remote-Extra-",
		"requestheader-group-headers":        "X-Remote-Group",
		"requestheader-username-headers":     "X-Remote-User",
		"secure-port":                        "6443",
		"service-account-issuer":             "https://kubernetes.default.svc." + c.Domain,
		"service-account-key-file":           pki(kpki.FileServiceAccountPub),
		"service-account-signing-key-file":   pki(kpki.FileServiceAccountKey),
		"service-cluster-ip-range":           c.ServiceCIDR,
		"tls-cert-file":                      pki(kpki.FileAPIServer),
		"tls-min-version":                    "VersionTLS12",
		"tls-private-key-file":               pki(kpki.FileAPIServerKey),
	}), ip, 6443, corev1.URISchemeHTTPS, "/livez", "/readyz", "/livez", "250m", "")
	mountDir(apiServer, "k8s-certs", PKIDir, podPKIDir, true, corev1.HostPathDirectory)

	controllerManager := pod("kube-controller-manager", c.Images.KubeControllerManager, flags(c, "kube-controller-manager", map[string]string{
		"allocate-node-cidrs":              "true",
		"authentication-kubeconfig":        pki(kpki.FileControllerManagerConfig),
		"authorization-kubeconfig":         pki(kpki.FileControllerManagerConfig),
		"bind-address":                     "127.0.0.1",
		"client-ca-file":                   pki(kpki.FileCA),
		"cluster-cidr":                     c.PodCIDR,
		"cluster-name":                     "kubernetes",
		"cluster-signing-cert-file":        pki(kpki.FileCA),
		"cluster-signing-key-file":         pki(kpki.FileCAKey),
		"controllers":                      "*,-bootstrapsigner,-tokencleaner",
		"kubeconfig":                       pki(kpki.FileControllerManagerConfig),
		"leader-elect":                     "true",
		"profiling":                        "false",
		"requestheader-client-ca-file":     pki(kpki.FileFrontProxyCA),
		"root-ca-file":                     pki(kpki.FileCA),
		"service-account-private-key-file": pki(kpki.FileServiceAccountKey),
		"service-cluster-ip-range":         c.ServiceCIDR,
		"use-service-account-credentials":  "true",
	}), "127.0.0.1", 10257, corev1.URISchemeHTTPS, "/healthz", "", "/healthz", "200m", "")
	mountDir(controllerManager, "k8s-certs", PKIDir, podPKIDir, true, corev1.HostPathDirectory)

	schedulerConfig := "/etc/kubernetes/scheduler.kubeconfig"
	scheduler := pod("kube-scheduler", c.Images.KubeScheduler, flags(c, "kube-scheduler", map[string]string{
		"authentication-kubeconfig": schedulerConfig,
		"authorization-kubeconfig":  schedulerConfig,
		"bind-address":              "127.0.0.1",
		"kubeconfig":                schedulerConfig,
		"leader-elect":              "true",
		"profiling":                 "false",
	}), "127.0.0.1", 10259, corev1.URISchemeHTTPS, "/livez", "/readyz", "/livez", "100m", "")
	mountDir(scheduler, "kubeconfig", path.Join(PKIDir, kpki.FileSchedulerConfig), schedulerConfig, true, corev1.HostPathFile)

	out := map[string][]byte{}
	for _, p := range []*corev1.Pod{etcd, apiServer, controllerManager, scheduler} {
		hash, err := certificatesHash(p, files)
		if err != nil {
			return nil, err
		}
		p.Annotations = map[string]string{CertificatesAnnotation: hash}
		data, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return nil, err
		}
		out[p.Name+".json"] = append(data, '\n')
	}
	return out, nil
}

// flags turns the component's flags, with the cluster's extra flags for it on top, into
// arguments in name order.
func flags(c kubernetes.Cluster, component string, base map[string]string) []string {
	merged := map[string]string{}
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range c.ExtraArgs[component] {
		merged[k] = v
	}
	names := make([]string, 0, len(merged))
	for k := range merged {
		names = append(names, k)
	}
	sort.Strings(names)
	args := []string{component}
	for _, k := range names {
		args = append(args, "--"+k+"="+merged[k])
	}
	return args
}

func pod(name, image string, command []string, probeHost string, port int32, scheme corev1.URIScheme, live, ready, startup, cpu, memory string) *corev1.Pod {
	probe := func(path string, delay, failures, period int32) *corev1.Probe {
		return &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
				Host: probeHost, Path: path, Port: intstr.FromString("probe-port"), Scheme: scheme,
			}},
			InitialDelaySeconds: delay, TimeoutSeconds: 15, FailureThreshold: failures, PeriodSeconds: period,
		}
	}
	requests := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}
	if memory != "" {
		requests[corev1.ResourceMemory] = resource.MustParse(memory)
	}
	container := corev1.Container{
		Name:            name,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         command,
		Ports:           []corev1.ContainerPort{{Name: "probe-port", ContainerPort: port, Protocol: corev1.ProtocolTCP}},
		Resources:       corev1.ResourceRequirements{Requests: requests},
		LivenessProbe:   probe(live, 10, 8, 10),
		// Four minutes, as kubeadm allows: a node pulls the images on first start.
		StartupProbe: probe(startup, 10, 24, 10),
	}
	if ready != "" {
		container.ReadinessProbe = probe(ready, 0, 3, 1)
	}
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kube-system",
			Labels:    map[string]string{"component": name, "tier": "control-plane"},
		},
		Spec: corev1.PodSpec{
			Containers:        []corev1.Container{container},
			HostNetwork:       true,
			PriorityClassName: "system-node-critical",
			SecurityContext:   &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		},
	}
}

func mountDir(p *corev1.Pod, name, hostPath, mountPath string, readOnly bool, kind corev1.HostPathType) {
	p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{
		Name:         name,
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: hostPath, Type: &kind}},
	})
	c := &p.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: name, MountPath: mountPath, ReadOnly: readOnly})
}

// certificatesHash hashes the files below PKIDir that the pod mounts, in name order.
func certificatesHash(p *corev1.Pod, files map[string][]byte) (string, error) {
	var names []string
	for _, v := range p.Spec.Volumes {
		rel, ok := strings.CutPrefix(v.HostPath.Path, PKIDir)
		if !ok {
			continue
		}
		rel = strings.TrimPrefix(rel, "/")
		for name := range files {
			if rel == "" || name == rel || strings.HasPrefix(name, rel+"/") {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("%s mounts no certificates", p.Name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(files[name]))
		h.Write(files[name])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
