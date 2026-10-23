---
title: "Troubleshoot Kubernetes"
description: "Find out why a node's Kubernetes does not start, join, serve logs or reach other nodes' pods"
---

# Troubleshoot Kubernetes

These symptoms show in a node's Kubernetes line of [`chalkctl
status`](../reference/cli/chalkctl_status.md) or in kubectl;
[Troubleshooting](../operate/troubleshooting.md) covers the node itself.

## The node has no address

The Kubernetes line says `preparation failed: no ipv4 node address matches ...`, with the
selector and the addresses the node has. The node waited `spec.nodeIP.timeout` of the
[`Cluster`](../reference/kinds.md#cluster) document, 300 seconds by default, for an address that
matches `nodeIP.addresses` or `nodeIP.validSubnets` of its [`Node`](../reference/kinds.md#node)
document, or the cluster's `nodeIP.validSubnets`, and found none, so it runs neither the
kubelet nor static pods. Check the node's `Network` documents and DHCP, and that the subnets list
the network the node is on. Then deliver the corrected documents with
`chalkctl apply-config <node> --secrets=secrets.age`, or reboot once the network is right.

A control plane that lost an address it was pinned to says `pinned address ... is not present`
instead; see [Run a highly available control
plane](ha-control-plane.md#a-control-plane-cannot-rejoin-etcd).

## A control plane waits for bootstrap

`waiting for bootstrap or for the cluster at https://...` is the state of every control plane
before the cluster exists: run `chalkctl bootstrap` on exactly one. After the bootstrap it means
the node finds no API server of its cluster at the endpoint. Check that the endpoint resolves,
that the VIP is held (another control plane's status shows `vip holder`), and that the node
reaches port 6443 there.

## A control plane does not finish joining

The status names the join's step or its last error after `joining the cluster at ...:`. A node
that stays at `etcd member ... catches up` cannot reach the other members on port 2380, or its
etcd is still copying a large database. `etcd has a member <node> already` is a member left over
from an earlier install: remove it, as
[Run a highly available control plane](ha-control-plane.md#a-control-plane-cannot-rejoin-etcd)
describes.

## kubectl logs and exec fail on a node

The API server reaches kubelets with their serving certificates, which the kubelet requests
through a CertificateSigningRequest and chalkd on a control plane approves. chalkd approves a
request only when the Node lists every address and name it asks for, and leaves the others
pending:

```sh
kubectl get csr
```

A pending `kubernetes.io/kubelet-serving` request usually asks for an address the Node does not
report. Check the node's addresses in `kubectl get node <node> -o wide` against its `Network`
and `Node` documents.

## Pods on different nodes cannot reach each other

Pod traffic between nodes travels in flannel's VXLAN, on UDP port 8472.

- A node whose address lies outside `vxlanSourceSubnets` of the
  [`Flannel`](../reference/kinds.md#flannel) document refuses to prepare and says
  `the node's address ... is outside vxlanSourceSubnets`; the other nodes would drop its VXLAN.
  Widen the ranges in the `Flannel` document. chalkctl refuses an inventory whose nodes hold
  different `Flannel` specs, so every node gets the same ranges.
- Small packets pass and large ones hang, as with TLS handshakes or large responses: the VXLAN
  MTU is too big for the network. Set `spec.mtu` of the `Flannel` document to the network's MTU,
  minus 20 when IPv6 is in use.
- A firewall between the nodes drops UDP 8472.

## A Node has no pod range

A worker's state ends in `the Node has no ipv6 pod range; the control plane allocates ranges of the
families the cluster was created with`. The families in `ipFamilies` changed after the cluster was
created, which is not supported. Restore the original families.

## The kubelet certificate was lost with VAR

A worker whose VAR lost its data
falls back to the kubelet certificate in its Kubernetes
share on STATE, which it received at install. When that
one expired too, the kubelet cannot join, and `chalkctl status w1` says:

```text
certificates:
  ...
  kubelet client  expires 2026-09-14  expired; deliver a new one with chalkctl apply-config w1 --kubernetes-share
```

Deliver a new share with a new kubelet certificate:

```sh
chalkctl apply-config w1 --kubernetes-share
```
