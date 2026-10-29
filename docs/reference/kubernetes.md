---
title: "Kubernetes"
description: "The components of each node type, the flags chalkos refuses, the add-ons it applies and the Kubernetes status states"
---

# Kubernetes

This page is for operators of a cluster on the shipped variant who read a node's units and pods,
a refusal of `chalkctl validate` or the Kubernetes line of `chalkctl status`. It lists what runs
on each type of node, the flags chalkos refuses in `extraArgs` and `kubeletExtraArgs`, the
objects the control planes apply and the states of the Kubernetes line.
[How Kubernetes runs on a node](../how-it-works/kubernetes/overview.md) explains the mechanism.

## Components by node type

`spec.type` of a node's `Node` document sets its type.

| Type | Runs |
| --- | --- |
| `controlplane` | containerd, the kubelet, and etcd, the API server, the controller-manager and the scheduler as static pods |
| `worker` | containerd and the kubelet |
| none, on an image without Kubernetes | No Kubernetes at all; the image leaves it out |

## Protected API server flags

`extraArgs.kube-apiserver` of the `Cluster` document refuses these flags, also spelt with `_` for
`-`. They decide who may do what.

| Flag | chalkos sets |
| --- | --- |
| `anonymous-auth` | Anonymous requests reach only `/livez`, `/readyz` and `/healthz`, through the authentication configuration |
| `authentication-config` | The API server's authentication configuration, which chalkos writes |
| `authorization-mode` | `Node,RBAC` |
| `enable-bootstrap-token-auth` | `false` |

## Protected kubelet flags

`kubeletExtraArgs` of a `Node` document refuses these flags, also spelt with `_` for `-`. They set
where the kubelet finds its configuration, credentials, credential provider and static pods, the
node's name, addresses, labels and taints, and who may use the kubelet's API. A refused
`node-labels` or `register-with-taints` names the `Node` document's `labels` and `taints` instead.

- `anonymous-auth`
- `authentication-token-webhook`
- `authorization-mode`
- `bootstrap-kubeconfig`
- `cert-dir`
- `client-ca-file`
- `config`
- `config-dir`
- `hostname-override`
- `image-credential-provider-bin-dir`
- `image-credential-provider-config`
- `kubeconfig`
- `node-ip`
- `node-labels`
- `pod-manifest-path`
- `read-only-port`
- `register-with-taints`
- `rotate-certificates`
- `tls-cert-file`
- `tls-private-key-file`

A kubelet flag's name and value also hold no whitespace and no backslash, because the flags reach
the kubelet through an environment file that systemd splits at whitespace.

## Conflicting API server flags

The API server refuses to start with these flags next to flags chalkos sets, so
`extraArgs.kube-apiserver` refuses them too.

| Flags | Conflict with |
| --- | --- |
| `oidc-issuer-url`, `oidc-client-id`, `oidc-ca-file`, `oidc-username-claim`, `oidc-username-prefix`, `oidc-groups-claim`, `oidc-groups-prefix`, `oidc-signing-algs`, `oidc-required-claim` | `authentication-config` |
| `authorization-config`, `authorization-policy-file`, `authorization-webhook-config-file` | `authorization-mode=Node,RBAC` |
| `admission-control` | `enable-admission-plugins` |
| `service-account-signing-endpoint` | `service-account-signing-key-file` and `service-account-key-file` |

## Add-ons

A bootstrapped control plane applies these objects in this order, with server-side apply as field
manager `chalkd`. chalkd renders the first four from the node's cluster.

| Order | Objects |
| --- | --- |
| 1 | RBAC: `chalkos:cluster-admins` binds the group `chalkos:cluster-admins` of admin kubeconfigs to `cluster-admin`; `chalkos:selfnodeclient` lets kubelets renew their own client certificates; `chalkos:apiserver-kubelet` gives the API server access to the kubelets |
| 2 | kube-proxy, as a DaemonSet in nftables mode |
| 3 | The pod network: flannel, unless `chalkos.cni.provider` is `none` |
| 4 | CoreDNS, at the primary family's address of `dnsIPs` |
| 5 | The objects of the `Addon` documents: each document's `spec.objects` in their order, and the documents in the order of their names |

The admission policy `chalkos-revoked-nodes` is not among these objects. A control plane applies it
whenever the list of revoked nodes changes.

## Kubernetes status states

The Kubernetes line of `chalkctl status` names the node's kind and one of these states.

| State | Meaning |
| --- | --- |
| `no share` | The node holds no Kubernetes share yet; it gets one at install or with `chalkctl apply-config --kubernetes-share` |
| `preparing` | The preparation of the node's addresses, certificates and kubelet flags runs |
| `waiting for bootstrap or for the cluster at <endpoint>` | A control plane that found no cluster to join yet |
| `joining the cluster at <endpoint>: <step>` | A join that runs, with its step or its last error |
| `bootstrapped` | A control plane that is an etcd voter, by bootstrap or join |
| `bootstrapped: <problem>` | A control plane whose address differs from its etcd peer URL, or whose etcd member belongs to another cluster than the other control planes' members |
| `joined` | A worker |
| `joined: <problem>` | A worker whose Node lacks a pod range of one of the cluster's families |
| `preparation failed: <reason>` | The preparation stopped, for example because no address matched |
| `left etcd; reinstall the node to join the cluster again` | A control plane after `chalkctl etcd leave` |
| `etcd data missing: restore etcd or reinstall the node` | A bootstrapped control plane whose etcd data is gone, as after VAR was lost |

After the state, the line shows whether the node's Node is Ready. A control plane adds its VIP
role, `vip holder` or `vip standby`, and `control plane current` when its API server and etcd
serve the current certificates and the API server answers ready.

Sources: `pkg/kubernetes/cluster.go`, `pkg/kubernetes/manifests/manifests.go`,
`pkg/kubernetes/addons/addons.go`, `pkg/chalkd/layers/kubernetes/status.go`,
`pkg/chalkd/layers/kubernetes/join.go` and `modules/node/kubernetes`.
