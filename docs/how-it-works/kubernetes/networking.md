---
title: "Kubernetes networking"
description: "How a Kubernetes node picks its addresses, how the VIP and the pod network work, and which ports the control plane opens"
---

# Kubernetes networking

A Kubernetes cluster on chalkos runs on the node's host network, which
[Networking](../node/networking.md) describes. On top of it, each node picks one address per IP
family of the cluster, flannel carries the pods' traffic between nodes, and kube-proxy and CoreDNS
give pods their services and names. The `Flannel` document of a cluster with flannel sets the MTU
flannel assumes and the ranges its VXLAN must come from:

```yaml
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Flannel
metadata: {name: default}
spec:
  # The network between the nodes carries 1450 bytes, so the VXLAN devices and the pods get 1400.
  mtu: 1450
```

## Each node picks its Kubernetes addresses before the kubelet starts

`chalkos-kubernetes.service` picks the node's addresses before the kubelet starts, and writes them
below `/run/chalkos/kubernetes`. A node without a matching address stays in the state
`preparation failed: <reason>`.
[Which addresses does a node use for Kubernetes?](../node/networking.md#which-addresses-does-a-node-use-for-kubernetes)
explains how a node picks them, and why a control plane's addresses are pinned.

## The address families of a cluster are fixed once it exists

The `Cluster` document's `ipFamilies` decide whether the cluster runs on IPv4, IPv6 or both. Its
pod ranges, service ranges and DNS addresses belong to those families. All four are fixed once the
cluster exists, as
[How Kubernetes runs on a node](overview.md#the-ranges-of-a-cluster-are-fixed-once-it-exists)
explains. A worker whose Node lacks a pod range of one of the cluster's families reports
`joined: <problem>` in its status.
[How does dual stack work?](../node/networking.md#how-does-dual-stack-work) describes a cluster of
both families.

## The VIP keeps the cluster endpoint reachable

A VIP is an address of the API server that one healthy control plane holds at a time, so the
cluster endpoint stays reachable while a control plane reboots or fails. `vip.addresses` of the
`Cluster` document sets it, and a node's `Node` document names the interface the node holds it on.
[How does the VIP move between control planes?](../node/networking.md#how-does-the-vip-move-between-control-planes)
describes the election.

## Flannel carries the pod network

chalkd applies the pod network as an add-on: flannel, unless
[`chalkos.cni.provider`](../../reference/options.md#chalkoscniprovider) is `none`. flannel carries
pod traffic between nodes in VXLAN. A cluster has at most one `Flannel` document, on images with
flannel, and every node holds the same one; chalkctl refuses an inventory whose nodes hold
different `Flannel` specs.
[How do pods reach each other across nodes?](../node/networking.md#how-do-pods-reach-each-other-across-nodes)
describes the VXLAN devices and how the firewall accepts VXLAN.

## kube-proxy and CoreDNS give pods services and names

chalkd applies kube-proxy as a DaemonSet in nftables mode, the mode the node's firewall and flannel
use too. It applies CoreDNS at the primary family's address of the `Cluster` document's `dnsIPs`.
The kubelet configuration of every node names the cluster's DNS address and domain, so pods
resolve through CoreDNS.

## The control plane ports open only on control planes

`chalkos-controlplane.service` opens the API server's and etcd's ports in the firewall on a node
whose `Node` document has the type `controlplane`, and closes them on a worker. It does this at
boot and whenever the `Node` document changes.
[Ports and firewall](../../reference/ports-and-firewall.md) lists the ports.

## Limits

- The address families, pod ranges, service ranges and DNS addresses of a cluster are fixed once it
  exists. A change needs a new cluster.

## Related pages

- [Networking](../node/networking.md): the host network a Kubernetes node runs on.
- [How Kubernetes runs on a node](overview.md) and [The control plane](control-plane.md).
- [Configure Kubernetes](../../kubernetes/configure.md#flannel-sets-the-pod-networks-mtu-and-vxlan-sources)
  for the `Flannel` document.
- [Config kinds](../../reference/kinds.md#flannel) for every field of `Cluster` and `Flannel`.
