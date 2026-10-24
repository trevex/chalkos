// Package addons renders the Kubernetes objects chalkos applies to every cluster before the
// cluster's own manifests: RBAC for chalkos's identities, kube-proxy, flannel when it is the pod
// network, and CoreDNS.
package addons

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	rbacv1ac "k8s.io/client-go/applyconfigurations/rbac/v1"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

// Render returns the objects in the order the control plane applies them: RBAC, kube-proxy, the
// pod network, the cluster DNS.
func Render(c kubernetes.Cluster) ([]map[string]any, error) {
	families := c.Families()
	configs := rbac()
	proxy, err := kubeProxy(c, families)
	if err != nil {
		return nil, err
	}
	configs = append(configs, proxy...)
	if c.Flannel != nil {
		f, err := flannel(c, families)
		if err != nil {
			return nil, err
		}
		configs = append(configs, f...)
	}
	configs = append(configs, coreDNS(c, families[0])...)
	objects := make([]map[string]any, len(configs))
	for i, config := range configs {
		if objects[i], err = runtime.DefaultUnstructuredConverter.ToUnstructured(config); err != nil {
			return nil, err
		}
	}
	return objects, nil
}

// compact is v as JSON on one line, as a ConfigMap carries a configuration file.
func compact(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

func clusterRoleBinding(name, role string, subject *rbacv1ac.SubjectApplyConfiguration) *rbacv1ac.ClusterRoleBindingApplyConfiguration {
	return rbacv1ac.ClusterRoleBinding(name).
		WithRoleRef(rbacv1ac.RoleRef().WithAPIGroup("rbac.authorization.k8s.io").WithKind("ClusterRole").WithName(role)).
		WithSubjects(subject)
}

func subject(kind, name string) *rbacv1ac.SubjectApplyConfiguration {
	return rbacv1ac.Subject().WithAPIGroup("rbac.authorization.k8s.io").WithKind(kind).WithName(name)
}

func serviceAccountSubject(name, namespace string) *rbacv1ac.SubjectApplyConfiguration {
	return rbacv1ac.Subject().WithKind("ServiceAccount").WithName(name).WithNamespace(namespace)
}

func rule(group string, resources []string, verbs ...string) *rbacv1ac.PolicyRuleApplyConfiguration {
	return rbacv1ac.PolicyRule().WithAPIGroups(group).WithResources(resources...).WithVerbs(verbs...)
}

func fieldRef(name, path string) *corev1ac.EnvVarApplyConfiguration {
	return corev1ac.EnvVar().WithName(name).
		WithValueFrom(corev1ac.EnvVarSource().WithFieldRef(corev1ac.ObjectFieldSelector().WithFieldPath(path)))
}

func mount(name, path string) *corev1ac.VolumeMountApplyConfiguration {
	return corev1ac.VolumeMount().WithName(name).WithMountPath(path)
}

func hostPath(name, path string) *corev1ac.VolumeApplyConfiguration {
	return corev1ac.Volume().WithName(name).WithHostPath(corev1ac.HostPathVolumeSource().WithPath(path))
}

func xtablesLock() *corev1ac.VolumeApplyConfiguration {
	return corev1ac.Volume().WithName("xtables-lock").
		WithHostPath(corev1ac.HostPathVolumeSource().WithPath("/run/xtables.lock").WithType(corev1.HostPathFileOrCreate))
}

func configMapVolume(name, configMap string) *corev1ac.VolumeApplyConfiguration {
	return corev1ac.Volume().WithName(name).WithConfigMap(corev1ac.ConfigMapVolumeSource().WithName(configMap))
}

func httpProbe(path, port string) *corev1ac.ProbeApplyConfiguration {
	return corev1ac.Probe().WithHTTPGet(corev1ac.HTTPGetAction().WithPath(path).WithPort(intstr.FromString(port)))
}

func port(name string, number int32, protocol corev1.Protocol) *corev1ac.ContainerPortApplyConfiguration {
	return corev1ac.ContainerPort().WithName(name).WithContainerPort(number).WithProtocol(protocol)
}

func rbac() []any {
	return []any{
		// Admin kubeconfigs from chalkctl kubeconfig carry this group, which RBAC can revoke,
		// unlike system:masters.
		clusterRoleBinding("chalkos:cluster-admins", "cluster-admin", subject("Group", "chalkos:cluster-admins")),
		// Kubelets renew their client certificates; the controller-manager approves the requests
		// of nodes renewing their own.
		clusterRoleBinding("chalkos:selfnodeclient", "system:certificates.k8s.io:certificatesigningrequests:selfnodeclient",
			subject("Group", "system:nodes")),
		// The API server reaches kubelets for logs, exec and port forwarding.
		clusterRoleBinding("chalkos:apiserver-kubelet", "system:kubelet-api-admin", subject("User", "chalkos:kube-apiserver-kubelet-client")),
	}
}

// kubeProxyConfiguration is kube-proxy's configuration file, its fields in key order.
type kubeProxyConfiguration struct {
	APIVersion       string `json:"apiVersion"`
	BindAddress      string `json:"bindAddress"`
	ClientConnection struct {
		Kubeconfig string `json:"kubeconfig"`
	} `json:"clientConnection"`
	ClusterCIDR       string   `json:"clusterCIDR"`
	Kind              string   `json:"kind"`
	Mode              string   `json:"mode"`
	NodePortAddresses []string `json:"nodePortAddresses"`
}

// The kubeconfig of a pod's service account, its fields in key order.
type (
	serviceAccountKubeconfig struct {
		APIVersion     string      `json:"apiVersion"`
		Clusters       []saCluster `json:"clusters"`
		Contexts       []saContext `json:"contexts"`
		CurrentContext string      `json:"current-context"`
		Kind           string      `json:"kind"`
		Users          []saUser    `json:"users"`
	}
	saCluster struct {
		Cluster struct {
			CertificateAuthority string `json:"certificate-authority"`
			Server               string `json:"server"`
		} `json:"cluster"`
		Name string `json:"name"`
	}
	saContext struct {
		Context struct {
			Cluster   string `json:"cluster"`
			Namespace string `json:"namespace"`
			User      string `json:"user"`
		} `json:"context"`
		Name string `json:"name"`
	}
	saUser struct {
		Name string `json:"name"`
		User struct {
			TokenFile string `json:"tokenFile"`
		} `json:"user"`
	}
)

func kubeProxy(c kubernetes.Cluster, families []nodeip.Family) ([]any, error) {
	cidrs := make([]string, len(families))
	for i, f := range families {
		cidrs[i] = c.PodCIDRs.Of(f).String()
	}
	// kube-proxy serves every family the node has addresses of; it binds as kubeadm's does, by
	// the primary family.
	bind := "0.0.0.0"
	if families[0] == nodeip.IPv6 {
		bind = "::"
	}
	proxyConfig := kubeProxyConfiguration{
		APIVersion: "kubeproxy.config.k8s.io/v1alpha1",
		Kind:       "KubeProxyConfiguration",
		// The node's firewall and flannel run on nftables too.
		Mode:        "nftables",
		ClusterCIDR: strings.Join(cidrs, ","),
		BindAddress: bind,
		// NodePorts answer on the node's own addresses, one per family, as the kubelet registered
		// them.
		NodePortAddresses: []string{"primary"},
	}
	proxyConfig.ClientConnection.Kubeconfig = "/var/lib/kube-proxy/kubeconfig.conf"
	config, err := compact(proxyConfig)
	if err != nil {
		return nil, err
	}
	// Pods reach the API server at the cluster endpoint: the service address needs kube-proxy.
	cluster := saCluster{Name: "default"}
	cluster.Cluster.CertificateAuthority = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	cluster.Cluster.Server = c.Endpoint
	context := saContext{Name: "default"}
	context.Context.Cluster = "default"
	context.Context.Namespace = "default"
	context.Context.User = "default"
	user := saUser{Name: "default"}
	user.User.TokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	kubeconfig, err := compact(serviceAccountKubeconfig{
		APIVersion:     "v1",
		Kind:           "Config",
		Clusters:       []saCluster{cluster},
		Contexts:       []saContext{context},
		CurrentContext: "default",
		Users:          []saUser{user},
	})
	if err != nil {
		return nil, err
	}
	labels := map[string]string{"k8s-app": "kube-proxy"}
	return []any{
		corev1ac.ServiceAccount("kube-proxy", "kube-system"),
		clusterRoleBinding("chalkos:node-proxier", "system:node-proxier", serviceAccountSubject("kube-proxy", "kube-system")),
		corev1ac.ConfigMap("kube-proxy", "kube-system").
			WithLabels(map[string]string{"app": "kube-proxy"}).
			WithData(map[string]string{"kubeconfig.conf": kubeconfig, "config.conf": config}),
		appsv1ac.DaemonSet("kube-proxy", "kube-system").
			WithLabels(labels).
			WithSpec(appsv1ac.DaemonSetSpec().
				WithSelector(metav1ac.LabelSelector().WithMatchLabels(labels)).
				WithUpdateStrategy(appsv1ac.DaemonSetUpdateStrategy().WithType(appsv1.RollingUpdateDaemonSetStrategyType)).
				WithTemplate(corev1ac.PodTemplateSpec().
					WithLabels(labels).
					WithSpec(corev1ac.PodSpec().
						WithPriorityClassName("system-node-critical").
						WithHostNetwork(true).
						WithServiceAccountName("kube-proxy").
						WithNodeSelector(map[string]string{"kubernetes.io/os": "linux"}).
						WithTolerations(corev1ac.Toleration().WithOperator(corev1.TolerationOpExists)).
						WithContainers(corev1ac.Container().
							WithName("kube-proxy").
							WithImage(c.Images.KubeProxy).
							WithCommand(
								"/usr/local/bin/kube-proxy",
								"--config=/var/lib/kube-proxy/config.conf",
								"--hostname-override=$(NODE_NAME)",
							).
							WithEnv(fieldRef("NODE_NAME", "spec.nodeName")).
							WithSecurityContext(corev1ac.SecurityContext().WithPrivileged(true)).
							WithVolumeMounts(
								mount("kube-proxy", "/var/lib/kube-proxy"),
								mount("xtables-lock", "/run/xtables.lock"),
								mount("lib-modules", "/lib/modules").WithReadOnly(true),
							)).
						WithVolumes(
							configMapVolume("kube-proxy", "kube-proxy"),
							xtablesLock(),
							// NixOS keeps the running kernel's modules here, not in /lib/modules.
							hostPath("lib-modules", "/run/booted-system/kernel-modules/lib/modules"),
						)))),
	}, nil
}

// flanneld uses exactly the node's addresses, one per family, as the kubelet registered them and
// reports them as the pod's addresses: the firewall accepts VXLAN to those alone. With them as
// public addresses flanneld finds the interface holding them.
const flanneld = `set -- --ip-masq --kube-subnet-mgr --healthz-port=8081
# Split on commas alone, without expanding file names.
set -f
IFS=,
for ip in $POD_IPS; do
  case $ip in
    *:*) set -- "$@" "--public-ipv6=$ip" ;;
    *) set -- "$@" "--public-ip=$ip" ;;
  esac
done
exec /opt/bin/flanneld "$@"
`

// netConf is flannel's network configuration, its fields in key order.
type netConf struct {
	Backend struct {
		MTU  int    `json:"MTU,omitempty"`
		Type string `json:"Type"`
	} `json:"Backend"`
	// EnableIPv4 is set to false alone, for clusters without IPv4.
	EnableIPv4     *bool  `json:"EnableIPv4,omitempty"`
	EnableIPv6     bool   `json:"EnableIPv6,omitempty"`
	EnableNFTables bool   `json:"EnableNFTables"`
	IPv6Network    string `json:"IPv6Network,omitempty"`
	Network        string `json:"Network,omitempty"`
}

// The CNI network configuration list flannel installs on the node, its fields in key order.
type (
	conflist struct {
		CNIVersion string      `json:"cniVersion"`
		Name       string      `json:"name"`
		Plugins    []cniPlugin `json:"plugins"`
	}
	cniPlugin struct {
		Backend      string           `json:"backend,omitempty"`
		Capabilities *cniCapabilities `json:"capabilities,omitempty"`
		Delegate     *cniDelegate     `json:"delegate,omitempty"`
		Type         string           `json:"type"`
	}
	cniCapabilities struct {
		PortMappings bool `json:"portMappings"`
	}
	cniDelegate struct {
		HairpinMode      bool `json:"hairpinMode"`
		IsDefaultGateway bool `json:"isDefaultGateway"`
	}
)

func flannel(c kubernetes.Cluster, families []nodeip.Family) ([]any, error) {
	// VXLAN in every family of the cluster (flannel.1 and flannel-v6.1, both on UDP 8472), with
	// the rules flannel needs in nftables tables of its own.
	var nc netConf
	nc.Backend.Type = "vxlan"
	nc.Backend.MTU = c.Flannel.MTU
	nc.EnableNFTables = true
	if slices.Contains(families, nodeip.IPv4) {
		nc.Network = c.PodCIDRs.IPv4.String()
	} else {
		nc.EnableIPv4 = new(false)
	}
	if slices.Contains(families, nodeip.IPv6) {
		nc.EnableIPv6 = true
		nc.IPv6Network = c.PodCIDRs.IPv6.String()
	}
	net, err := compact(nc)
	if err != nil {
		return nil, err
	}
	cni, err := compact(conflist{CNIVersion: "0.3.1", Name: "cbr0", Plugins: []cniPlugin{
		{Type: "flannel", Delegate: &cniDelegate{HairpinMode: true, IsDefaultGateway: true}},
		{Type: "portmap", Capabilities: &cniCapabilities{PortMappings: true}, Backend: "nftables"},
	}})
	if err != nil {
		return nil, err
	}
	labels := map[string]string{"tier": "node", "app": "flannel", "k8s-app": "flannel"}
	flannelLabel := map[string]string{"k8s-app": "flannel"}
	return []any{
		corev1ac.Namespace("kube-flannel").WithLabels(map[string]string{
			"k8s-app":                            "flannel",
			"pod-security.kubernetes.io/enforce": "privileged",
		}),
		rbacv1ac.ClusterRole("flannel").
			WithLabels(flannelLabel).
			WithRules(
				rule("", []string{"pods"}, "get"),
				rule("", []string{"nodes"}, "get", "list", "watch"),
				rule("", []string{"nodes/status"}, "patch"),
			),
		rbacv1ac.ClusterRoleBinding("flannel").
			WithLabels(flannelLabel).
			WithRoleRef(rbacv1ac.RoleRef().WithAPIGroup("rbac.authorization.k8s.io").WithKind("ClusterRole").WithName("flannel")).
			WithSubjects(serviceAccountSubject("flannel", "kube-flannel")),
		corev1ac.ServiceAccount("flannel", "kube-flannel").WithLabels(flannelLabel),
		corev1ac.ConfigMap("kube-flannel-cfg", "kube-flannel").
			WithLabels(labels).
			WithData(map[string]string{"cni-conf.json": cni, "net-conf.json": net}),
		appsv1ac.DaemonSet("kube-flannel-ds", "kube-flannel").
			WithLabels(labels).
			WithSpec(appsv1ac.DaemonSetSpec().
				WithSelector(metav1ac.LabelSelector().WithMatchLabels(map[string]string{"app": "flannel"})).
				WithTemplate(corev1ac.PodTemplateSpec().
					WithLabels(labels).
					WithSpec(corev1ac.PodSpec().
						WithHostNetwork(true).
						WithPriorityClassName("system-node-critical").
						WithServiceAccountName("flannel").
						WithNodeSelector(map[string]string{"kubernetes.io/os": "linux"}).
						WithTolerations(corev1ac.Toleration().WithOperator(corev1.TolerationOpExists).WithEffect(corev1.TaintEffectNoSchedule)).
						// The flannel CNI plugin comes with the image (containerd's bin_dirs), so
						// only the network configuration is installed on the host.
						WithInitContainers(corev1ac.Container().
							WithName("install-cni").
							WithImage(c.Images.Flannel).
							WithCommand("/opt/bin/install-conf").
							WithArgs("/etc/kube-flannel/cni-conf.json", "/etc/cni/net.d/10-flannel.conflist").
							WithVolumeMounts(
								mount("cni", "/etc/cni/net.d"),
								mount("flannel-cfg", "/etc/kube-flannel/"),
							)).
						WithContainers(corev1ac.Container().
							WithName("kube-flannel").
							WithImage(c.Images.Flannel).
							WithCommand("/bin/sh", "-c", flanneld).
							WithPorts(port("healthz", 8081, corev1.ProtocolTCP)).
							WithLivenessProbe(httpProbe("/healthz", "healthz").WithInitialDelaySeconds(10).WithPeriodSeconds(30)).
							WithReadinessProbe(httpProbe("/readyz", "healthz").WithInitialDelaySeconds(5).WithPeriodSeconds(10)).
							WithResources(corev1ac.ResourceRequirements().WithRequests(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("50Mi"),
							})).
							WithSecurityContext(corev1ac.SecurityContext().
								WithPrivileged(false).
								WithCapabilities(corev1ac.Capabilities().WithAdd("NET_ADMIN", "NET_RAW"))).
							WithEnv(
								fieldRef("POD_NAME", "metadata.name"),
								fieldRef("POD_NAMESPACE", "metadata.namespace"),
								// The node's addresses, the primary family's first, separated by commas.
								fieldRef("POD_IPS", "status.podIPs"),
								corev1ac.EnvVar().WithName("EVENT_QUEUE_DEPTH").WithValue("5000"),
								corev1ac.EnvVar().WithName("CONT_WHEN_CACHE_NOT_READY").WithValue("false"),
							).
							WithVolumeMounts(
								mount("run", "/run/flannel"),
								mount("flannel-cfg", "/etc/kube-flannel/"),
								mount("xtables-lock", "/run/xtables.lock"),
							)).
						WithVolumes(
							hostPath("run", "/run/flannel"),
							// containerd reads CNI configurations from VAR; /etc is read-only.
							hostPath("cni", "/var/lib/cni/net.d"),
							configMapVolume("flannel-cfg", "kube-flannel-cfg"),
							xtablesLock(),
						)))),
	}, nil
}

// corefile answers for the cluster domain and forwards other names to the nodes' resolvers.
const corefile = `.:53 {
    errors
    health {
       lameduck 5s
    }
    ready
    kubernetes %[1]s in-addr.arpa ip6.arpa {
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
       ttl 30
    }
    prometheus :9153
    forward . /etc/resolv.conf {
       max_concurrent 1000
    }
    cache 30 {
       disable success %[1]s
       disable denial %[1]s
    }
    loop
    reload
    loadbalance
}
`

func coreDNS(c kubernetes.Cluster, primary nodeip.Family) []any {
	labels := map[string]string{"k8s-app": "kube-dns"}
	family := map[nodeip.Family]corev1.IPFamily{nodeip.IPv4: corev1.IPv4Protocol, nodeip.IPv6: corev1.IPv6Protocol}[primary]
	serviceLabels := map[string]string{
		"k8s-app":                       "kube-dns",
		"kubernetes.io/cluster-service": "true",
		"kubernetes.io/name":            "CoreDNS",
	}
	servicePort := func(name string, number int32, protocol corev1.Protocol) *corev1ac.ServicePortApplyConfiguration {
		return corev1ac.ServicePort().WithName(name).WithPort(number).WithProtocol(protocol).WithTargetPort(intstr.FromInt32(number))
	}
	return []any{
		corev1ac.ServiceAccount("coredns", "kube-system"),
		rbacv1ac.ClusterRole("system:coredns").WithRules(
			rule("", []string{"endpoints", "services", "pods", "namespaces"}, "list", "watch"),
			rule("discovery.k8s.io", []string{"endpointslices"}, "list", "watch"),
		),
		rbacv1ac.ClusterRoleBinding("system:coredns").
			WithRoleRef(rbacv1ac.RoleRef().WithAPIGroup("rbac.authorization.k8s.io").WithKind("ClusterRole").WithName("system:coredns")).
			WithSubjects(serviceAccountSubject("coredns", "kube-system")),
		corev1ac.ConfigMap("coredns", "kube-system").
			WithData(map[string]string{"Corefile": fmt.Sprintf(corefile, c.Domain)}),
		appsv1ac.Deployment("coredns", "kube-system").
			WithLabels(labels).
			WithSpec(appsv1ac.DeploymentSpec().
				WithReplicas(2).
				WithStrategy(appsv1ac.DeploymentStrategy().
					WithType(appsv1.RollingUpdateDeploymentStrategyType).
					WithRollingUpdate(appsv1ac.RollingUpdateDeployment().WithMaxUnavailable(intstr.FromInt32(1)))).
				WithSelector(metav1ac.LabelSelector().WithMatchLabels(labels)).
				WithTemplate(corev1ac.PodTemplateSpec().
					WithLabels(labels).
					WithSpec(corev1ac.PodSpec().
						WithPriorityClassName("system-cluster-critical").
						WithServiceAccountName("coredns").
						WithAffinity(corev1ac.Affinity().WithPodAntiAffinity(corev1ac.PodAntiAffinity().
							WithPreferredDuringSchedulingIgnoredDuringExecution(corev1ac.WeightedPodAffinityTerm().
								WithWeight(100).
								WithPodAffinityTerm(corev1ac.PodAffinityTerm().
									WithLabelSelector(metav1ac.LabelSelector().WithMatchExpressions(metav1ac.LabelSelectorRequirement().
										WithKey("k8s-app").WithOperator(metav1.LabelSelectorOpIn).WithValues("kube-dns"))).
									WithTopologyKey("kubernetes.io/hostname"))))).
						WithTolerations(
							corev1ac.Toleration().WithKey("CriticalAddonsOnly").WithOperator(corev1.TolerationOpExists),
							corev1ac.Toleration().WithKey("node-role.kubernetes.io/control-plane").WithEffect(corev1.TaintEffectNoSchedule),
						).
						WithNodeSelector(map[string]string{"kubernetes.io/os": "linux"}).
						WithContainers(corev1ac.Container().
							WithName("coredns").
							WithImage(c.Images.CoreDNS).
							WithResources(corev1ac.ResourceRequirements().
								WithLimits(corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("170Mi")}).
								WithRequests(corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("70Mi"),
								})).
							WithArgs("-conf", "/etc/coredns/Corefile").
							WithVolumeMounts(mount("config-volume", "/etc/coredns").WithReadOnly(true)).
							WithPorts(
								port("dns", 53, corev1.ProtocolUDP),
								port("dns-tcp", 53, corev1.ProtocolTCP),
								port("metrics", 9153, corev1.ProtocolTCP),
								port("liveness-probe", 8080, corev1.ProtocolTCP),
								port("readiness-probe", 8181, corev1.ProtocolTCP),
							).
							WithLivenessProbe(corev1ac.Probe().
								WithHTTPGet(corev1ac.HTTPGetAction().WithPath("/health").WithPort(intstr.FromString("liveness-probe")).WithScheme(corev1.URISchemeHTTP)).
								WithInitialDelaySeconds(60).
								WithTimeoutSeconds(5).
								WithSuccessThreshold(1).
								WithFailureThreshold(5)).
							WithReadinessProbe(corev1ac.Probe().
								WithHTTPGet(corev1ac.HTTPGetAction().WithPath("/ready").WithPort(intstr.FromString("readiness-probe")).WithScheme(corev1.URISchemeHTTP))).
							WithSecurityContext(corev1ac.SecurityContext().
								WithAllowPrivilegeEscalation(false).
								WithCapabilities(corev1ac.Capabilities().WithAdd("NET_BIND_SERVICE").WithDrop("ALL")).
								WithReadOnlyRootFilesystem(true))).
						WithDNSPolicy(corev1.DNSDefault).
						WithVolumes(corev1ac.Volume().
							WithName("config-volume").
							WithConfigMap(corev1ac.ConfigMapVolumeSource().
								WithName("coredns").
								WithItems(corev1ac.KeyToPath().WithKey("Corefile").WithPath("Corefile"))))))),
		corev1ac.Service("kube-dns", "kube-system").
			WithLabels(serviceLabels).
			// One address of the primary family, which every pod has, as kubeadm's DNS service
			// gets: the kubelet hands pods one name server. It still answers A and AAAA queries.
			WithSpec(corev1ac.ServiceSpec().
				WithClusterIP(c.DNSIPs.Of(primary).String()).
				WithIPFamilyPolicy(corev1.IPFamilyPolicySingleStack).
				WithIPFamilies(family).
				WithSelector(labels).
				WithPorts(
					servicePort("dns", 53, corev1.ProtocolUDP),
					servicePort("dns-tcp", 53, corev1.ProtocolTCP),
					servicePort("metrics", 9153, corev1.ProtocolTCP),
				)),
	}
}
