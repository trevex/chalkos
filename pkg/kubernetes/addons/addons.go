// Package addons renders the Kubernetes objects chalkos applies to every cluster before the
// cluster's own manifests: RBAC for chalkos's identities, kube-proxy, flannel when it is the pod
// network, and CoreDNS; and the admission policy that shuts out revoked nodes, which chalkd
// applies from the revoked nodes as they are listed.
package addons

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
)

type object = map[string]any

// Render returns the objects in the order the control plane applies them: RBAC, kube-proxy, the
// pod network, the cluster DNS.
func Render(c kubernetes.Cluster) ([]map[string]any, error) {
	families, err := c.Families()
	if err != nil {
		return nil, err
	}
	out := rbac()
	proxy, err := kubeProxy(c, families)
	if err != nil {
		return nil, err
	}
	out = append(out, proxy...)
	if c.Flannel != nil {
		f, err := flannel(c, families)
		if err != nil {
			return nil, err
		}
		out = append(out, f...)
	}
	return append(out, coreDNS(c, families[0])...), nil
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

func clusterRoleBinding(name, role string, subject object) object {
	return object{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   object{"name": name},
		"roleRef":    object{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": role},
		"subjects":   []any{subject},
	}
}

func subject(kind, name string) object {
	return object{"apiGroup": "rbac.authorization.k8s.io", "kind": kind, "name": name}
}

func serviceAccount(name, namespace string, labels object) object {
	meta := object{"name": name, "namespace": namespace}
	if labels != nil {
		meta["labels"] = labels
	}
	return object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": meta}
}

func rbac() []object {
	return []object{
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

// RevokedNodes renders the admission policy that denies every write by a revoked node, and its
// binding: the node's kubelet can no longer update its Node or its pods, renew its client
// certificate through a CSR or ask for a serving certificate. The policy names the nodes of the
// list inline, so the API server evaluates it without parameters: with parameters it would deny
// every request it matches, whoever made it, until its informer had read them. chalkd applies it
// again whenever the list changes. It fails closed, which a map literal cannot make fail.
func RevokedNodes(l revoked.List) []map[string]any {
	return []object{
		{
			"apiVersion": "admissionregistration.k8s.io/v1",
			"kind":       "ValidatingAdmissionPolicy",
			"metadata":   object{"name": revoked.Name},
			"spec": object{
				"failurePolicy": "Fail",
				// Subresources too: the kubelet writes nodes/status and pods/status.
				"matchConstraints": object{"resourceRules": []any{object{
					"apiGroups":   []any{"*"},
					"apiVersions": []any{"*"},
					"resources":   []any{"*/*"},
					"operations":  []any{"CREATE", "UPDATE", "DELETE", "CONNECT"},
				}}},
				// Requests by anyone but a node end here.
				"matchConditions": []any{object{"name": "node", "expression": `request.userInfo.username.startsWith("system:node:")`}},
				"variables": []any{
					object{"name": "revoked", "expression": revokedLiteral(l)},
					object{"name": "node", "expression": "request.userInfo.username.substring(12)"},
				},
				"validations": []any{object{
					"expression":        "!(variables.node in variables.revoked)",
					"messageExpression": `"node " + variables.node + " is revoked " + variables.revoked[variables.node]`,
					"message":           "the node is revoked",
					"reason":            "Forbidden",
				}},
			},
		},
		{
			"apiVersion": "admissionregistration.k8s.io/v1",
			"kind":       "ValidatingAdmissionPolicyBinding",
			"metadata":   object{"name": revoked.Name},
			"spec": object{
				"policyName":        revoked.Name,
				"validationActions": []any{"Deny"},
			},
		},
	}
}

// MaxRevokedLength bounds the length of the policy's map of revoked nodes, in code points. The
// API server compiles no CEL expression longer than 100,000 code points; the bound leaves room
// for nodes revoked through other control planes at the same time, each of which checks the list
// as it read it.
const MaxRevokedLength = 90_000

// RevokedLength is the length, in code points, of the map of revoked nodes the policy names for
// the list, which MaxRevokedLength bounds.
func RevokedLength(l revoked.List) int {
	return utf8.RuneCountInString(revokedLiteral(l))
}

// revokedLiteral is a CEL map from each revoked node's name to when it was revoked, in name
// order. strconv.Quote is safe here: ConfigMap keys are limited to [-._a-zA-Z0-9] and the times
// are rendered again, so no string holds what Go and CEL quote differently. The one mismatch,
// Go's \xNN for bytes of invalid UTF-8, which CEL reads as a code point, cannot occur.
func revokedLiteral(l revoked.List) string {
	var entries []string
	for _, name := range slices.Sorted(maps.Keys(l)) {
		when := "at an unknown time"
		if at := l[name]; !at.IsZero() {
			when = "since " + at.UTC().Format(revoked.TimeFormat)
		}
		entries = append(entries, strconv.Quote(name)+": "+strconv.Quote(when))
	}
	return "{" + strings.Join(entries, ", ") + "}"
}

func kubeProxy(c kubernetes.Cluster, families []nodeip.Family) ([]object, error) {
	cidrs := make([]string, len(families))
	for i, f := range families {
		cidrs[i] = c.PodCIDRs.Of(f)
	}
	// kube-proxy serves every family the node has addresses of; it binds as kubeadm's does, by
	// the primary family.
	bind := "0.0.0.0"
	if families[0] == nodeip.IPv6 {
		bind = "::"
	}
	config, err := compact(object{
		"apiVersion": "kubeproxy.config.k8s.io/v1alpha1",
		"kind":       "KubeProxyConfiguration",
		// The node's firewall and flannel run on nftables too.
		"mode":        "nftables",
		"clusterCIDR": strings.Join(cidrs, ","),
		"bindAddress": bind,
		// NodePorts answer on the node's own addresses, one per family, as the kubelet registered
		// them.
		"nodePortAddresses": []any{"primary"},
		"clientConnection":  object{"kubeconfig": "/var/lib/kube-proxy/kubeconfig.conf"},
	})
	if err != nil {
		return nil, err
	}
	// Pods reach the API server at the cluster endpoint: the service address needs kube-proxy.
	kubeconfig, err := compact(object{
		"apiVersion": "v1",
		"kind":       "Config",
		"clusters": []any{object{"name": "default", "cluster": object{
			"certificate-authority": "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
			"server":                c.Endpoint,
		}}},
		"contexts":        []any{object{"name": "default", "context": object{"cluster": "default", "namespace": "default", "user": "default"}}},
		"current-context": "default",
		"users":           []any{object{"name": "default", "user": object{"tokenFile": "/var/run/secrets/kubernetes.io/serviceaccount/token"}}},
	})
	if err != nil {
		return nil, err
	}
	return []object{
		serviceAccount("kube-proxy", "kube-system", nil),
		clusterRoleBinding("chalkos:node-proxier", "system:node-proxier",
			object{"kind": "ServiceAccount", "name": "kube-proxy", "namespace": "kube-system"}),
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   object{"name": "kube-proxy", "namespace": "kube-system", "labels": object{"app": "kube-proxy"}},
			"data":       object{"kubeconfig.conf": kubeconfig, "config.conf": config},
		},
		{
			"apiVersion": "apps/v1",
			"kind":       "DaemonSet",
			"metadata":   object{"name": "kube-proxy", "namespace": "kube-system", "labels": object{"k8s-app": "kube-proxy"}},
			"spec": object{
				"selector":       object{"matchLabels": object{"k8s-app": "kube-proxy"}},
				"updateStrategy": object{"type": "RollingUpdate"},
				"template": object{
					"metadata": object{"labels": object{"k8s-app": "kube-proxy"}},
					"spec": object{
						"priorityClassName":  "system-node-critical",
						"hostNetwork":        true,
						"serviceAccountName": "kube-proxy",
						"nodeSelector":       object{"kubernetes.io/os": "linux"},
						"tolerations":        []any{object{"operator": "Exists"}},
						"containers": []any{object{
							"name":  "kube-proxy",
							"image": c.Images.KubeProxy,
							"command": []any{
								"/usr/local/bin/kube-proxy",
								"--config=/var/lib/kube-proxy/config.conf",
								"--hostname-override=$(NODE_NAME)",
							},
							"env":             []any{object{"name": "NODE_NAME", "valueFrom": object{"fieldRef": object{"fieldPath": "spec.nodeName"}}}},
							"securityContext": object{"privileged": true},
							"volumeMounts": []any{
								object{"name": "kube-proxy", "mountPath": "/var/lib/kube-proxy"},
								object{"name": "xtables-lock", "mountPath": "/run/xtables.lock"},
								object{"name": "lib-modules", "mountPath": "/lib/modules", "readOnly": true},
							},
						}},
						"volumes": []any{
							object{"name": "kube-proxy", "configMap": object{"name": "kube-proxy"}},
							object{"name": "xtables-lock", "hostPath": object{"path": "/run/xtables.lock", "type": "FileOrCreate"}},
							// NixOS keeps the running kernel's modules here, not in /lib/modules.
							object{"name": "lib-modules", "hostPath": object{"path": "/run/booted-system/kernel-modules/lib/modules"}},
						},
					},
				},
			},
		},
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

func flannel(c kubernetes.Cluster, families []nodeip.Family) ([]object, error) {
	inUse := func(f nodeip.Family) bool {
		for _, g := range families {
			if g == f {
				return true
			}
		}
		return false
	}
	// VXLAN in every family of the cluster (flannel.1 and flannel-v6.1, both on UDP 8472), with
	// the rules flannel needs in nftables tables of its own.
	backend := object{"Type": "vxlan"}
	if c.Flannel.MTU > 0 {
		backend["MTU"] = c.Flannel.MTU
	}
	netConf := object{"EnableNFTables": true, "Backend": backend}
	if inUse(nodeip.IPv4) {
		netConf["Network"] = c.PodCIDRs.IPv4
	} else {
		netConf["EnableIPv4"] = false
	}
	if inUse(nodeip.IPv6) {
		netConf["EnableIPv6"] = true
		netConf["IPv6Network"] = c.PodCIDRs.IPv6
	}
	net, err := compact(netConf)
	if err != nil {
		return nil, err
	}
	cni, err := compact(object{
		"name":       "cbr0",
		"cniVersion": "0.3.1",
		"plugins": []any{
			object{"type": "flannel", "delegate": object{"hairpinMode": true, "isDefaultGateway": true}},
			object{"type": "portmap", "capabilities": object{"portMappings": true}, "backend": "nftables"},
		},
	})
	if err != nil {
		return nil, err
	}
	labels := func() object { return object{"tier": "node", "app": "flannel", "k8s-app": "flannel"} }
	flannelLabel := func() object { return object{"k8s-app": "flannel"} }
	return []object{
		{
			"apiVersion": "v1",
			"kind":       "Namespace",
			"metadata": object{"name": "kube-flannel", "labels": object{
				"k8s-app":                            "flannel",
				"pod-security.kubernetes.io/enforce": "privileged",
			}},
		},
		{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRole",
			"metadata":   object{"name": "flannel", "labels": flannelLabel()},
			"rules": []any{
				object{"apiGroups": []any{""}, "resources": []any{"pods"}, "verbs": []any{"get"}},
				object{"apiGroups": []any{""}, "resources": []any{"nodes"}, "verbs": []any{"get", "list", "watch"}},
				object{"apiGroups": []any{""}, "resources": []any{"nodes/status"}, "verbs": []any{"patch"}},
			},
		},
		{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRoleBinding",
			"metadata":   object{"name": "flannel", "labels": flannelLabel()},
			"roleRef":    object{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "flannel"},
			"subjects":   []any{object{"kind": "ServiceAccount", "name": "flannel", "namespace": "kube-flannel"}},
		},
		serviceAccount("flannel", "kube-flannel", flannelLabel()),
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   object{"name": "kube-flannel-cfg", "namespace": "kube-flannel", "labels": labels()},
			"data":       object{"cni-conf.json": cni, "net-conf.json": net},
		},
		{
			"apiVersion": "apps/v1",
			"kind":       "DaemonSet",
			"metadata":   object{"name": "kube-flannel-ds", "namespace": "kube-flannel", "labels": labels()},
			"spec": object{
				"selector": object{"matchLabels": object{"app": "flannel"}},
				"template": object{
					"metadata": object{"labels": labels()},
					"spec": object{
						"hostNetwork":        true,
						"priorityClassName":  "system-node-critical",
						"serviceAccountName": "flannel",
						"nodeSelector":       object{"kubernetes.io/os": "linux"},
						"tolerations":        []any{object{"operator": "Exists", "effect": "NoSchedule"}},
						// The flannel CNI plugin comes with the image (containerd's bin_dirs), so
						// only the network configuration is installed on the host.
						"initContainers": []any{object{
							"name":    "install-cni",
							"image":   c.Images.Flannel,
							"command": []any{"/opt/bin/install-conf"},
							"args":    []any{"/etc/kube-flannel/cni-conf.json", "/etc/cni/net.d/10-flannel.conflist"},
							"volumeMounts": []any{
								object{"name": "cni", "mountPath": "/etc/cni/net.d"},
								object{"name": "flannel-cfg", "mountPath": "/etc/kube-flannel/"},
							},
						}},
						"containers": []any{object{
							"name":           "kube-flannel",
							"image":          c.Images.Flannel,
							"command":        []any{"/bin/sh", "-c", flanneld},
							"ports":          []any{object{"name": "healthz", "containerPort": 8081, "protocol": "TCP"}},
							"livenessProbe":  object{"httpGet": object{"path": "/healthz", "port": "healthz"}, "initialDelaySeconds": 10, "periodSeconds": 30},
							"readinessProbe": object{"httpGet": object{"path": "/readyz", "port": "healthz"}, "initialDelaySeconds": 5, "periodSeconds": 10},
							"resources":      object{"requests": object{"cpu": "100m", "memory": "50Mi"}},
							"securityContext": object{
								"privileged":   false,
								"capabilities": object{"add": []any{"NET_ADMIN", "NET_RAW"}},
							},
							"env": []any{
								object{"name": "POD_NAME", "valueFrom": object{"fieldRef": object{"fieldPath": "metadata.name"}}},
								object{"name": "POD_NAMESPACE", "valueFrom": object{"fieldRef": object{"fieldPath": "metadata.namespace"}}},
								// The node's addresses, the primary family's first, separated by commas.
								object{"name": "POD_IPS", "valueFrom": object{"fieldRef": object{"fieldPath": "status.podIPs"}}},
								object{"name": "EVENT_QUEUE_DEPTH", "value": "5000"},
								object{"name": "CONT_WHEN_CACHE_NOT_READY", "value": "false"},
							},
							"volumeMounts": []any{
								object{"name": "run", "mountPath": "/run/flannel"},
								object{"name": "flannel-cfg", "mountPath": "/etc/kube-flannel/"},
								object{"name": "xtables-lock", "mountPath": "/run/xtables.lock"},
							},
						}},
						"volumes": []any{
							object{"name": "run", "hostPath": object{"path": "/run/flannel"}},
							// containerd reads CNI configurations from VAR; /etc is read-only.
							object{"name": "cni", "hostPath": object{"path": "/var/lib/cni/net.d"}},
							object{"name": "flannel-cfg", "configMap": object{"name": "kube-flannel-cfg"}},
							object{"name": "xtables-lock", "hostPath": object{"path": "/run/xtables.lock", "type": "FileOrCreate"}},
						},
					},
				},
			},
		},
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

func coreDNS(c kubernetes.Cluster, primary nodeip.Family) []object {
	labels := func() object { return object{"k8s-app": "kube-dns"} }
	family := map[nodeip.Family]string{nodeip.IPv4: "IPv4", nodeip.IPv6: "IPv6"}[primary]
	serviceLabels := labels()
	serviceLabels["kubernetes.io/cluster-service"] = "true"
	serviceLabels["kubernetes.io/name"] = "CoreDNS"
	return []object{
		serviceAccount("coredns", "kube-system", nil),
		{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRole",
			"metadata":   object{"name": "system:coredns"},
			"rules": []any{
				object{"apiGroups": []any{""}, "resources": []any{"endpoints", "services", "pods", "namespaces"}, "verbs": []any{"list", "watch"}},
				object{"apiGroups": []any{"discovery.k8s.io"}, "resources": []any{"endpointslices"}, "verbs": []any{"list", "watch"}},
			},
		},
		{
			"apiVersion": "rbac.authorization.k8s.io/v1",
			"kind":       "ClusterRoleBinding",
			"metadata":   object{"name": "system:coredns"},
			"roleRef":    object{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "system:coredns"},
			"subjects":   []any{object{"kind": "ServiceAccount", "name": "coredns", "namespace": "kube-system"}},
		},
		{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   object{"name": "coredns", "namespace": "kube-system"},
			"data":       object{"Corefile": fmt.Sprintf(corefile, c.Domain)},
		},
		{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   object{"name": "coredns", "namespace": "kube-system", "labels": labels()},
			"spec": object{
				"replicas": 2,
				"strategy": object{"type": "RollingUpdate", "rollingUpdate": object{"maxUnavailable": 1}},
				"selector": object{"matchLabels": labels()},
				"template": object{
					"metadata": object{"labels": labels()},
					"spec": object{
						"priorityClassName":  "system-cluster-critical",
						"serviceAccountName": "coredns",
						"affinity": object{"podAntiAffinity": object{"preferredDuringSchedulingIgnoredDuringExecution": []any{object{
							"weight": 100,
							"podAffinityTerm": object{
								"labelSelector": object{"matchExpressions": []any{object{"key": "k8s-app", "operator": "In", "values": []any{"kube-dns"}}}},
								"topologyKey":   "kubernetes.io/hostname",
							},
						}}}},
						"tolerations": []any{
							object{"key": "CriticalAddonsOnly", "operator": "Exists"},
							object{"key": "node-role.kubernetes.io/control-plane", "effect": "NoSchedule"},
						},
						"nodeSelector": object{"kubernetes.io/os": "linux"},
						"containers": []any{object{
							"name":  "coredns",
							"image": c.Images.CoreDNS,
							"resources": object{
								"limits":   object{"memory": "170Mi"},
								"requests": object{"cpu": "100m", "memory": "70Mi"},
							},
							"args":         []any{"-conf", "/etc/coredns/Corefile"},
							"volumeMounts": []any{object{"name": "config-volume", "mountPath": "/etc/coredns", "readOnly": true}},
							"ports": []any{
								object{"containerPort": 53, "name": "dns", "protocol": "UDP"},
								object{"containerPort": 53, "name": "dns-tcp", "protocol": "TCP"},
								object{"containerPort": 9153, "name": "metrics", "protocol": "TCP"},
								object{"containerPort": 8080, "name": "liveness-probe", "protocol": "TCP"},
								object{"containerPort": 8181, "name": "readiness-probe", "protocol": "TCP"},
							},
							"livenessProbe": object{
								"httpGet":             object{"path": "/health", "port": "liveness-probe", "scheme": "HTTP"},
								"initialDelaySeconds": 60,
								"timeoutSeconds":      5,
								"successThreshold":    1,
								"failureThreshold":    5,
							},
							"readinessProbe": object{"httpGet": object{"path": "/ready", "port": "readiness-probe", "scheme": "HTTP"}},
							"securityContext": object{
								"allowPrivilegeEscalation": false,
								"capabilities":             object{"add": []any{"NET_BIND_SERVICE"}, "drop": []any{"ALL"}},
								"readOnlyRootFilesystem":   true,
							},
						}},
						"dnsPolicy": "Default",
						"volumes": []any{object{
							"name":      "config-volume",
							"configMap": object{"name": "coredns", "items": []any{object{"key": "Corefile", "path": "Corefile"}}},
						}},
					},
				},
			},
		},
		{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata":   object{"name": "kube-dns", "namespace": "kube-system", "labels": serviceLabels},
			// One address of the primary family, which every pod has, as kubeadm's DNS service
			// gets: the kubelet hands pods one name server. It still answers A and AAAA queries.
			"spec": object{
				"clusterIP":      c.DNSIPs.Of(primary),
				"ipFamilyPolicy": "SingleStack",
				"ipFamilies":     []any{family},
				"selector":       labels(),
				"ports": []any{
					object{"name": "dns", "port": 53, "protocol": "UDP", "targetPort": 53},
					object{"name": "dns-tcp", "port": 53, "protocol": "TCP", "targetPort": 53},
					object{"name": "metrics", "port": 9153, "protocol": "TCP", "targetPort": 9153},
				},
			},
		},
	}
}
