// Package manifests renders the static pods of a chalkos control-plane node: etcd, the API
// server, the controller-manager and the scheduler, run by the kubelet from the upstream images.
package manifests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

const (
	// podPKIDir is where the pods see the control plane's certificates.
	podPKIDir = "/etc/kubernetes/pki"
	// CertificatesAnnotation carries a hash of the certificates a pod mounts, so the kubelet
	// replaces the pod when they are issued again.
	CertificatesAnnotation = "chalkos.dev/certificates"
)

// StaticPods renders the static pods by file name.
//   - pkiDir is where the control plane's certificates are on the node, kubernetes.PKIDir there.
//   - files are the certificates as pki.ControlPlane issues them.
//   - initialCluster is etcd's --initial-cluster when the node joins an existing cluster; empty
//     starts a new cluster of the node alone.
func StaticPods(c kubernetes.Cluster, n kubernetes.Node, pkiDir string, files map[string][]byte, initialCluster string) (map[string][]byte, error) {
	if len(n.IPs) == 0 {
		return nil, errors.New("the node has no address")
	}
	// etcd and the API server advertise the address of the primary family, and listen on its
	// loopback address for their own node.
	ip := n.IPs[0]
	loopback := c.Loopback()
	pki := func(file string) string { return path.Join(podPKIDir, file) }
	state := "existing"
	if initialCluster == "" {
		initialCluster, state = n.Name+"="+kubernetes.EtcdPeerURL(ip), "new"
	}

	etcd := pod(component{
		name:  "etcd",
		image: c.Images.Etcd,
		command: flags(c, "etcd", map[string]string{
			"name":                        n.Name,
			"data-dir":                    kubernetes.EtcdDataDir,
			"advertise-client-urls":       kubernetes.EtcdClientURL(ip),
			"initial-advertise-peer-urls": kubernetes.EtcdPeerURL(ip),
			"initial-cluster":             initialCluster,
			// A member with data ignores the initial cluster, so a restart never starts a new one.
			"initial-cluster-state": state,
			"listen-client-urls":    c.LocalEtcd() + "," + kubernetes.EtcdClientURL(ip),
			"listen-peer-urls":      kubernetes.EtcdPeerURL(ip),
			"listen-metrics-urls":   "http://" + net.JoinHostPort(loopback.String(), strconv.Itoa(kubernetes.EtcdMetricsPort)),
			"cert-file":             pki(kpki.FileEtcdServer),
			"key-file":              pki(kpki.FileEtcdServerKey),
			"trusted-ca-file":       pki(kpki.FileEtcdCA),
			"client-cert-auth":      "true",
			"peer-cert-file":        pki(kpki.FileEtcdPeer),
			"peer-key-file":         pki(kpki.FileEtcdPeerKey),
			"peer-trusted-ca-file":  pki(kpki.FileEtcdCA),
			"peer-client-cert-auth": "true",
			"snapshot-count":        "10000",
		}),
		probe: probe{host: loopback.String(), port: kubernetes.EtcdMetricsPort, scheme: corev1.URISchemeHTTP,
			live: "/livez", ready: "/readyz", startup: "/readyz"},
		cpu: "100m", memory: "100Mi",
	})
	mount(etcd, kubernetes.EtcdDataDir, corev1.HostPathDirectoryOrCreate,
		corev1.VolumeMount{Name: "etcd-data", MountPath: kubernetes.EtcdDataDir})
	mount(etcd, path.Join(pkiDir, "etcd"), corev1.HostPathDirectory,
		corev1.VolumeMount{Name: "etcd-certs", MountPath: path.Join(podPKIDir, "etcd"), ReadOnly: true})

	apiServer := pod(component{name: "kube-apiserver", image: c.Images.KubeAPIServer, command: flags(c, "kube-apiserver", map[string]string{
		"advertise-address":                  ip.String(),
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
		"etcd-servers":                       c.LocalEtcd(),
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
		"secure-port":                        strconv.Itoa(kubernetes.APIServerPort),
		"service-account-issuer":             "https://kubernetes.default.svc." + c.Domain,
		"service-account-key-file":           pki(kpki.FileServiceAccountPub),
		"service-account-signing-key-file":   pki(kpki.FileServiceAccountKey),
		"service-cluster-ip-range":           joinRanges(c.ServiceCIDRList()),
		"tls-cert-file":                      pki(kpki.FileAPIServer),
		"tls-min-version":                    "VersionTLS12",
		"tls-private-key-file":               pki(kpki.FileAPIServerKey),
	}),
		probe: probe{host: ip.String(), port: kubernetes.APIServerPort, scheme: corev1.URISchemeHTTPS,
			live: "/livez", ready: "/readyz", startup: "/livez"},
		cpu: "250m",
	})
	mount(apiServer, pkiDir, corev1.HostPathDirectory, corev1.VolumeMount{Name: "k8s-certs", MountPath: podPKIDir, ReadOnly: true})

	// The controller-manager and the scheduler serve their health endpoints on IPv4's loopback
	// address in every setup, as kubeadm's do: every node has it.
	controllerManagerFlags := map[string]string{
		"allocate-node-cidrs":              "true",
		"authentication-kubeconfig":        pki(kpki.FileControllerManagerConfig),
		"authorization-kubeconfig":         pki(kpki.FileControllerManagerConfig),
		"bind-address":                     "127.0.0.1",
		"client-ca-file":                   pki(kpki.FileCA),
		"cluster-cidr":                     joinRanges(c.PodCIDRList()),
		"cluster-name":                     "kubernetes",
		"cluster-signing-cert-file":        pki(kpki.FileCASigning),
		"cluster-signing-key-file":         pki(kpki.FileCAKey),
		"controllers":                      "*,-bootstrapsigner,-tokencleaner",
		"kubeconfig":                       pki(kpki.FileControllerManagerConfig),
		"leader-elect":                     "true",
		"profiling":                        "false",
		"requestheader-client-ca-file":     pki(kpki.FileFrontProxyCA),
		"root-ca-file":                     pki(kpki.FileCA),
		"service-account-private-key-file": pki(kpki.FileServiceAccountKey),
		"service-cluster-ip-range":         joinRanges(c.ServiceCIDRList()),
		"use-service-account-credentials":  "true",
	}
	for _, f := range c.Families() {
		controllerManagerFlags["node-cidr-mask-size-"+string(f)] = strconv.Itoa(c.NodeCIDRMaskSizes.Of(f))
	}
	controllerManager := pod(component{
		name: "kube-controller-manager", image: c.Images.KubeControllerManager,
		command: flags(c, "kube-controller-manager", controllerManagerFlags),
		probe:   probe{host: "127.0.0.1", port: 10257, scheme: corev1.URISchemeHTTPS, live: "/healthz", startup: "/healthz"},
		cpu:     "200m",
	})
	mount(controllerManager, pkiDir, corev1.HostPathDirectory, corev1.VolumeMount{Name: "k8s-certs", MountPath: podPKIDir, ReadOnly: true})

	schedulerConfig := "/etc/kubernetes/scheduler.kubeconfig"
	scheduler := pod(component{name: "kube-scheduler", image: c.Images.KubeScheduler, command: flags(c, "kube-scheduler", map[string]string{
		"authentication-kubeconfig": schedulerConfig,
		"authorization-kubeconfig":  schedulerConfig,
		"bind-address":              "127.0.0.1",
		"kubeconfig":                schedulerConfig,
		"leader-elect":              "true",
		"profiling":                 "false",
	}),
		probe: probe{host: "127.0.0.1", port: 10259, scheme: corev1.URISchemeHTTPS, live: "/livez", ready: "/readyz", startup: "/livez"},
		cpu:   "100m",
	})
	mount(scheduler, path.Join(pkiDir, kpki.FileSchedulerConfig), corev1.HostPathFile,
		corev1.VolumeMount{Name: "kubeconfig", MountPath: schedulerConfig, ReadOnly: true})

	out := map[string][]byte{}
	for _, p := range []*corev1.Pod{etcd, apiServer, controllerManager, scheduler} {
		hash, err := certificatesHash(p, pkiDir, files)
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

// joinRanges lists address ranges as Kubernetes's flags take them, separated by commas.
func joinRanges(ranges []netip.Prefix) string {
	list := make([]string, len(ranges))
	for i, r := range ranges {
		list[i] = r.String()
	}
	return strings.Join(list, ",")
}

// flags turns the component's flags, with the cluster's extra flags for it on top, into
// arguments in name order.
func flags(c kubernetes.Cluster, component string, base map[string]string) []string {
	merged := maps.Clone(base)
	maps.Copy(merged, c.ExtraArgs[component])
	args := []string{component}
	for _, k := range slices.Sorted(maps.Keys(merged)) {
		args = append(args, "--"+k+"="+merged[k])
	}
	return args
}

// component is a control-plane component's container.
type component struct {
	name, image string
	command     []string
	probe       probe
	// cpu and memory are what the container requests; an empty memory requests none.
	cpu, memory string
}

// probe is where the kubelet checks a component's health: an HTTP endpoint on the host's network
// with a path each for liveness, readiness and startup. An empty ready path checks no readiness.
type probe struct {
	host                 string
	port                 int32
	scheme               corev1.URIScheme
	live, ready, startup string
}

// get is the kubelet's check of the path, delay seconds after the start, every period seconds,
// failing after failures attempts.
func (pr probe) get(path string, delay, failures, period int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
			Host: pr.host, Path: path, Port: intstr.FromString("probe-port"), Scheme: pr.scheme,
		}},
		InitialDelaySeconds: delay, TimeoutSeconds: 15, FailureThreshold: failures, PeriodSeconds: period,
	}
}

// pod is the static pod that runs the component.
func pod(c component) *corev1.Pod {
	requests := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(c.cpu)}
	if c.memory != "" {
		requests[corev1.ResourceMemory] = resource.MustParse(c.memory)
	}
	container := corev1.Container{
		Name:            c.name,
		Image:           c.image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         c.command,
		Ports:           []corev1.ContainerPort{{Name: "probe-port", ContainerPort: c.probe.port, Protocol: corev1.ProtocolTCP}},
		Resources:       corev1.ResourceRequirements{Requests: requests},
		LivenessProbe:   c.probe.get(c.probe.live, 10, 8, 10),
		// Four minutes, as kubeadm allows: a node pulls the images on first start.
		StartupProbe: c.probe.get(c.probe.startup, 10, 24, 10),
	}
	if c.probe.ready != "" {
		container.ReadinessProbe = c.probe.get(c.probe.ready, 0, 3, 1)
	}
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.name,
			Namespace: "kube-system",
			Labels:    map[string]string{"component": c.name, "tier": "control-plane"},
		},
		Spec: corev1.PodSpec{
			Containers:        []corev1.Container{container},
			HostNetwork:       true,
			PriorityClassName: "system-node-critical",
			SecurityContext:   &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		},
	}
}

// mount adds the host path of the kind to the pod as the volume m names, and mounts it into the
// pod's container as m says.
func mount(p *corev1.Pod, hostPath string, kind corev1.HostPathType, m corev1.VolumeMount) {
	p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{
		Name:         m.Name,
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: hostPath, Type: &kind}},
	})
	c := &p.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, m)
}

// certificatesHash hashes the files below pkiDir that the pod mounts, in name order.
func certificatesHash(p *corev1.Pod, pkiDir string, files map[string][]byte) (string, error) {
	var names []string
	for _, v := range p.Spec.Volumes {
		rel, ok := strings.CutPrefix(v.HostPath.Path, pkiDir)
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
