---
title: "Configure Kubernetes"
description: "Set the cluster's settings, each node's labels, taints and flags, the pod network and your own add-ons"
---

# Configure Kubernetes

This page is for an operator who sets how Kubernetes runs on the cluster's nodes. Four kinds of
documents hold the settings: `Cluster` for what every node shares, `Node` for one node, `Flannel`
for the pod network and `Addon` for objects the control planes apply. At the end the nodes run
with your settings, delivered with the rest of their config. The page builds on the homelab, whose
`cluster/cluster.yaml` sets only the endpoint:

```yaml title="cluster/cluster.yaml"
--8<-- "examples/homelab/cluster/cluster.yaml"
```

Every other field takes its default. [Config kinds](../reference/kinds.md#cluster) lists every
field of the four kinds.

## Before you begin

You need the workstation that [Set up your workstation](../operate/workstation.md) describes, and
the decisions of [Plan a cluster](plan.md). Changes reach a running node with
`chalkctl apply-config`, as [Change a node's config](../operate/change-config.md) describes, which
needs the secrets file.

## The Cluster document holds what every node shares

Put the `Cluster` document into the files every node gets. chalkctl refuses an inventory whose
nodes hold different `Cluster` or `Flannel` specs, and a node whose config holds no `Cluster`
document. Its fields fall into groups:

| Fields | Set | Default |
| --- | --- | --- |
| `endpoint` | The URL of the API server, the cluster endpoint | required |
| `domain` | The cluster's DNS domain | `cluster.local` |
| `ipFamilies`, `podCIDRs`, `serviceCIDRs`, `dnsIPs` | The address families, their pod and service ranges and the cluster DNS address of each; fixed once the cluster exists | IPv4, `10.244.0.0/16`, `10.96.0.0/12`, the 10th address of the service range |
| `nodeCIDRMaskSizes` | The prefix length of each node's part of the pod range | 24 for IPv4, 64 for IPv6 |
| `nodeIP` | The subnets nodes without a fixed address pick theirs from, and how long a node waits for it at boot | any global unicast address, 300 seconds |
| `vip` | The VIPs the control planes hold | none |
| `allowSchedulingOnControlPlanes` | Whether workloads may run on control planes; fixed once the cluster exists | `false` |
| `extraArgs` | Extra flags of etcd, the API server, the controller-manager and the scheduler | none |

[Plan a cluster](plan.md) covers the endpoint, the VIP and the address families. The sections
below cover scheduling and the extra flags.

## The Node document gives a node its type, labels and taints

Each node's config holds one `Node` document, usually in the node's own file. The homelab's `w1`
is a worker for storage: its label attracts the storage system's replicas, and its taint keeps
other workloads off.

```yaml title="nodes/w1.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Node
metadata: {name: default}
spec:
  type: worker
  labels: {node.kubernetes.io/storage: ssd}
  taints: [{key: dedicated, value: storage, effect: NoSchedule}]
```

`type` is `controlplane` or `worker`, and it is fixed once the node holds a Kubernetes share. A
taint's `effect` is `NoSchedule`, `PreferNoSchedule` or `NoExecute`, and its `value` may be left
out.

The kubelet sets the labels on its own node, so the labels it may set limit yours. In the
`kubernetes.io` and `k8s.io` namespaces it sets only labels under `kubelet.kubernetes.io/` and
`node.kubernetes.io/`, and a short list of well-known labels. `chalkctl validate` refuses any other,
such as a `node-role.kubernetes.io/` label:

```text
node w1: invalid Kubernetes settings:
- Node: label "node-role.kubernetes.io/storage": the kubelet sets labels in the kubernetes.io and k8s.io namespaces on its own node only under kubelet.kubernetes.io/ and node.kubernetes.io/ or from the list kubernetes.io/hostname, topology.kubernetes.io/zone, topology.kubernetes.io/region, failure-domain.beta.kubernetes.io/zone, failure-domain.beta.kubernetes.io/region, beta.kubernetes.io/instance-type, node.kubernetes.io/instance-type, kubernetes.io/os, kubernetes.io/arch, beta.kubernetes.io/os, beta.kubernetes.io/arch
```

The kubelet applies the labels and taints when it registers its node, the first time the node
joins, and chalkd never changes the Kubernetes node after that. So `labels` and `taints` are fixed
once the node holds a Kubernetes share, and a node refuses a delivery that changes them:

```text
the config is refused: Node "default" spec.labels: the config changes the node's labels; the value applies when the node registers with the cluster, so changing it on a running node changes nothing; change the Node object with kubectl label instead, or reinstall the node
```

Change the labels and taints of a registered node with kubectl, and keep its `Node` document as it
is:

```sh
kubectl label node w1 node.kubernetes.io/storage=nvme --overwrite
kubectl taint node w1 dedicated=storage:NoSchedule-
```

A reinstall registers the node again with the labels and taints of its `Node` document.

The `Node` document also holds the node's own address settings, `nodeIP`, and on a control plane
the interface it holds the VIP on, `vip.interface`.
[Networking](../how-it-works/node/networking.md#which-addresses-does-a-node-use-for-kubernetes)
explains both.

## Run workloads on control planes

A control plane registers with the taint `node-role.kubernetes.io/control-plane:NoSchedule`, so
only pods that tolerate it run there. Set `allowSchedulingOnControlPlanes` before the first node
installs to leave the taint off the control planes:

```yaml title="cluster/cluster.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: default}
spec:
  endpoint: https://10.0.0.11:6443
  allowSchedulingOnControlPlanes: true
```

The taint is part of the control plane's registration, so `allowSchedulingOnControlPlanes` is fixed
once a node holds a Kubernetes share. Every node gets the same `Cluster` document, so every
running node refuses a delivery that changes it:

```text
the config is refused: Cluster "default" spec.allowSchedulingOnControlPlanes: the config changes allowSchedulingOnControlPlanes; the value applies when the node registers with the cluster, so changing it on a running node changes nothing; add or remove the node-role.kubernetes.io/control-plane:NoSchedule taint with kubectl taint instead, or reinstall the node
```

On a running cluster, keep the setting and remove or add the taint with kubectl:

```sh
kubectl taint node cp1 node-role.kubernetes.io/control-plane:NoSchedule-
kubectl taint node cp1 node-role.kubernetes.io/control-plane:NoSchedule
```

## Add flags to the control plane's components

`extraArgs` of the `Cluster` document adds flags to etcd, the API server, the controller-manager
and the scheduler, each a map of flags named without their leading dashes:

```yaml title="cluster/cluster.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: default}
spec:
  endpoint: https://10.0.0.11:6443
  extraArgs:
    # NodePort services take ports from a smaller range.
    kube-apiserver: {service-node-port-range: 30000-31000}
    # etcd may grow to 8 GiB.
    etcd: {quota-backend-bytes: "8589934592"}
```

The components take the flags on every control plane. A flag chalkos also sets takes your value.
A flag's name holds letters, digits, `_`, `.` and `-`, and starts with a letter or digit.
[The control plane](../how-it-works/kubernetes/control-plane.md#the-flags-follow-kubeadm-with-a-few-access-choices)
explains the flags chalkos sets.

The API server's flags that decide who may do what are refused, also when spelt with `_` for `-`:
`anonymous-auth`, `authentication-config`, `authorization-mode` and
`enable-bootstrap-token-auth`. `chalkctl validate` names the flag:

```text
node cp1: the Kubernetes cluster the documents give: Cluster "default": extraArgs: kube-apiserver --authentication-config is set by chalkos and cannot be overridden
```

The API server refuses to start with some flags next to flags chalkos sets, so `chalkctl validate`
refuses them too. [Conflicting API server flags](../reference/kubernetes.md#conflicting-api-server-flags)
lists them.

The error names the flag, the flag it conflicts with and what to do instead:

```text
node cp1: the Kubernetes cluster the documents give: Cluster "default": extraArgs: kube-apiserver --admission-control conflicts with --enable-admission-plugins: chalkos sets --enable-admission-plugins, and the API server refuses this flag next to it; give the plugins in enable-admission-plugins and disable-admission-plugins
```

From the node, the control plane's pods mount the certificates and kubeconfigs chalkd writes, and
etcd's pod its data directory. A flag that names a file of your own, such as an audit policy or a
webhook's configuration, finds no file to read.

## Add flags to a node's kubelet

`kubeletExtraArgs` of a node's `Node` document adds flags to that node's kubelet:

```yaml title="nodes/w1.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Node
metadata: {name: default}
spec:
  type: worker
  labels: {node.kubernetes.io/storage: ssd}
  taints: [{key: dedicated, value: storage, effect: NoSchedule}]
  kubeletExtraArgs: {max-pods: "200"}
```

The kubelet's configuration is the same on every node and comes from the cluster. Its flags come
last on the kubelet's command line, so they override that configuration. chalkos refuses the flags
it sets itself. They give the kubelet its configuration, credentials, certificates and static pods.
They also set the node's name, addresses, labels and taints, and who may use the kubelet's API.
[Protected kubelet flags](../reference/kubernetes.md#protected-kubelet-flags) lists them.

A refused flag names its replacement where there is one:

```text
node w1: the Kubernetes cluster the documents give: Node "default": kubeletExtraArgs: --register-with-taints is set by chalkos and cannot be overridden; give the node's labels and taints in the Node document's labels and taints
```

A kubelet flag's name and value hold no whitespace and no backslash, because the flags reach the
kubelet through an environment file that systemd splits at whitespace. A flag that needs a list
separated by spaces is refused:

```text
node w1: the Kubernetes cluster the documents give: Node "default": kubeletExtraArgs: --eviction-hard="memory.available<500Mi nodefs.available<10%": the kubelet's flags cannot hold whitespace or backslashes
```

## The API server authenticates with certificates and tokens

The API server accepts client certificates of the cluster's Kubernetes CA and service-account
tokens. Anonymous requests reach only `/livez`, `/readyz` and `/healthz`. chalkos writes the API
server's authentication configuration and protects the `authentication-config` flag that names it.

chalkos has no way to add an OIDC issuer or another authenticator today. The API server refuses
the `oidc-*` flags next to an authentication configuration, so `chalkctl validate` refuses them in
`extraArgs.kube-apiserver`:

```text
node cp1: the Kubernetes cluster the documents give: Cluster "default": extraArgs: kube-apiserver --oidc-issuer-url conflicts with --authentication-config: chalkos configures authentication through --authentication-config, and the API server refuses oidc-* flags next to it; chalkos offers no way to add an authenticator today, so grant narrower access with RBAC
```

For access narrower than `cluster-admin`, bind users and groups to roles with RBAC.
[Bootstrap and get a kubeconfig](bootstrap.md#give-someone-access-to-the-kubernetes-api) describes
the kubeconfigs chalkctl issues.

## Flannel sets the pod network's MTU and VXLAN sources

On images that run flannel, a `Flannel` document in the files every node gets sets two things:

```yaml title="cluster/cluster.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Flannel
metadata: {name: default}
spec:
  # Nodes accept the pod network's VXLAN traffic only from the nodes' own network.
  vxlanSourceSubnets: [10.0.0.0/24]
```

- `vxlanSourceSubnets` lists the ranges flannel's VXLAN traffic must come from, at least one of
  each family. Without it, nodes accept VXLAN from any source. Set it to the ranges your nodes live
  in.
- `mtu` is the MTU of the network between the nodes. Flannel's VXLAN devices and the pods get 50
  less. Without it, flannel takes the MTU of the interface that holds the node's address.

`chalkctl validate` refuses a node whose address lies outside the VXLAN sources, since the other
nodes would drop its traffic:

```text
node w1: invalid Kubernetes settings:
- Node: nodeIP.addresses: the node's address 10.0.0.21 is outside vxlanSourceSubnets 10.1.0.0/24, from which alone the other nodes take VXLAN
```

It also refuses a source range that overlaps the pod or service ranges, and an MTU that leaves pods
less than IPv4's minimum of 576, or IPv6's 1280 when the cluster uses IPv6.

## Addon documents apply your own objects

An `Addon` document holds Kubernetes objects that the control planes apply after chalkos's own
add-ons. This one creates a namespace and lets the group `team-a` read what runs in it:

```yaml title="cluster/apps.yaml"
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: apps}
spec:
  objects:
    - apiVersion: v1
      kind: Namespace
      metadata: {name: apps}
    - apiVersion: rbac.authorization.k8s.io/v1
      kind: RoleBinding
      metadata: {name: team-a-view, namespace: apps}
      roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: view}
      subjects: [{apiGroup: rbac.authorization.k8s.io, kind: Group, name: team-a}]
```

A node's config may hold several `Addon` documents, each with its own name. The control planes
apply the objects with server-side apply, each document's objects in their order and the documents
in the order of their names. They apply them at bootstrap, at every boot and after every delivery
that changes them.

- chalkctl delivers `Addon` documents to control planes alone, so the file may sit among the files
  every node gets. A control plane keeps them on STATE, readable by root alone, so the objects may
  include Secrets.
- An object you remove from an `Addon` document stays in the cluster. Delete it with kubectl.
- chalkd applies the objects as field manager `chalkd` and takes over fields that others changed,
  so a change made with kubectl to such an object is undone at the next apply.

## Pods pull private images with the nodes' credentials

A `Registry` document gives every node the credentials of a private registry. The kubelet then
pulls that registry's images for every pod on the node without `imagePullSecrets`.
[Registries and mirrors](../operate/registries.md#give-the-nodes-the-registrys-credentials)
describes the document. Keep credentials that only some workloads may use in their namespaces' pull
secrets instead.

## Deliver the change

Check the config of every node, then deliver it to each node whose files changed:

```sh
chalkctl validate
chalkctl apply-config cp1 --secrets=secrets.age
chalkctl apply-config w1 --secrets=secrets.age
```

A changed `Cluster`, `Flannel` or `Node` document restarts `chalkos-kubernetes.service` and
`kubelet.service` on the node, and chalkd on a control plane renders the control plane's pods again.
A changed `Node` document also restarts `chalkos-controlplane.service`, which opens the control
plane's ports by the node's type.
A changed `Addon` document restarts nothing: the control planes apply its objects again.
[React to changes](../build/react-to-changes.md#built-into-chalkos) lists these effects.

A node that holds a Kubernetes share refuses a config that changes `ipFamilies`, `podCIDRs`,
`serviceCIDRs` or `dnsIPs`, or the node's `type`, and names what the change takes.

## Check that it worked

`chalkctl status` shows each control plane's Kubernetes line. `control plane current` means its API
server and etcd run with the certificates the node holds and the API server answers ready:

```text
kubernetes controlplane: bootstrapped, node ready: True, control plane current
```

kubectl shows what reached Kubernetes. The labels and taints of a node, and the objects of the
`Addon` above:

```sh
kubectl --kubeconfig kubeconfig get node w1 --show-labels
kubectl --kubeconfig kubeconfig describe node w1
kubectl --kubeconfig kubeconfig get rolebinding -n apps
```

A component that does not start with an extra flag shows in its control plane's status and in the
kubelet's journal, `chalkctl logs cp1 --unit=kubelet.service`.
[Troubleshoot Kubernetes](troubleshooting.md) covers further symptoms.

## What next

- [Run a highly available control plane](ha-control-plane.md).
- [Upgrade Kubernetes](upgrade-kubernetes.md) to a new release.
- [Change a node's config](../operate/change-config.md) covers previews, refusals and the
  services a change restarts.
- [How Kubernetes runs on a node](../how-it-works/kubernetes/overview.md) explains what runs on each
  node, and [The control plane](../how-it-works/kubernetes/control-plane.md) what the control plane
  applies.
