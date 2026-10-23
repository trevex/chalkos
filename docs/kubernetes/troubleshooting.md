---
title: "Troubleshoot Kubernetes"
description: "Find out why a node's Kubernetes does not start, join, serve logs, reach other pods or get its kubelet certificates"
---

# Troubleshoot Kubernetes

This page is for an operator whose cluster misbehaves on chalkos nodes. Each section starts from a
symptom, names the likely cause and links the page with the fix. Most symptoms show in the node's
Kubernetes line of `chalkctl status`, the others in kubectl.
[Troubleshooting](../operate/troubleshooting.md) covers the node itself: its boot, disks, upgrades
and node certificates.

## Before you begin

You need the secrets file or a reader client file for `chalkctl status` and `chalkctl logs`, and a
kubeconfig for kubectl, as [Bootstrap and get a kubeconfig](bootstrap.md#get-a-kubeconfig)
describes.

Run `chalkctl status` on the node first, and read its Kubernetes line:

```sh
chalkctl status w1
```

The line starts with `kubernetes worker:` or `kubernetes controlplane:`, followed by the node's
state. [The Kubernetes line shows the node's part in the
cluster](../operate/status-and-logs.md#the-kubernetes-line-shows-the-nodes-part-in-the-cluster)
explains the rest of it. Then find the symptom below.

## The node has no address

The Kubernetes line reads:

```text
kubernetes worker: preparation failed: no ipv4 node address matches <selector> (the node has <addresses>), 5m0s after chalkos-node-addresses.target
```

`<selector>` names what the node looked for: `validSubnets` with the subnets, `nodeIP` with a
fixed address, or `the default filter`. `<addresses>` lists the node's addresses of that family,
each with its interface. The line names `ipv6` when the IPv6 address is missing. The node waited
`spec.nodeIP.timeout` of the `Cluster` document, 300 seconds by default and shown as `5m0s`, for an
address that matches `nodeIP.addresses` or `nodeIP.validSubnets` of its
`Node` document, or the cluster's `nodeIP.validSubnets`. It found none, so it runs neither the
kubelet nor static pods.

Check the node's `Network` documents and its DHCP lease, and that the subnets include the network
the node is on. [Network documents configure the
interfaces](../operate/node-config.md#network-documents-configure-the-interfaces) shows the
documents, and [Configure Kubernetes](configure.md) shows `nodeIP`. Deliver the corrected
documents, or reboot the node once the network is right:

```sh
chalkctl apply-config w1 --secrets=secrets.age
```

Two other preparation failures name the address too:

- `flannel needs the node's IPv4 and IPv6 addresses on one interface; ...` appears on a
  dual-stack node whose two addresses sit on different interfaces. Move both onto one interface in
  the `Network` documents.
- `pinned address <address> is not present` appears on a control plane that booted without an
  address it was pinned to when it joined etcd.
  [A control plane lost its pinned address](ha-control-plane.md#a-control-plane-lost-its-pinned-address)
  covers the fix.

## A control plane waits for bootstrap

The Kubernetes line reads:

```text
kubernetes controlplane: waiting for bootstrap or for the cluster at https://10.0.0.11:6443
```

Every control plane reads this before the cluster exists. Run
[`chalkctl bootstrap`](../reference/cli/chalkctl_bootstrap.md) on exactly one of them, as
[Bootstrap and get a kubeconfig](bootstrap.md#bootstrap-the-first-control-plane) describes.

After the bootstrap, the line means the node finds no API server of its cluster at the endpoint.
Check that:

- the endpoint's name resolves from the node;
- the VIP is held, when the endpoint is a VIP: another control plane's line shows `vip holder`;
- the node reaches port 6443 at the endpoint, through any load balancer in between.

[The cluster endpoint reaches the API server and
chalkd](plan.md#the-cluster-endpoint-reaches-the-api-server-and-chalkd) lists what the endpoint
must reach.

## A control plane does not finish joining

A joining control plane names its current step, or its last error, after
`joining the cluster at <endpoint>:`. Two states point to a cause:

- `etcd member <id> catches up` for a long time: the node cannot reach the other members on port
  2380, or its etcd is still copying a large database. Check the network between the control
  planes.
- `etcd has a member cp3 already; remove it with chalkctl etcd remove-member cp3`: a member is
  left over from an earlier install of the node. Run the command the line names. The node tries
  again every 10 seconds and joins.

[A control plane cannot rejoin etcd](ha-control-plane.md#a-control-plane-cannot-rejoin-etcd) lists
every state that holds a control plane back, with its fix.

## kubectl logs and exec fail on a node

`kubectl logs`, `kubectl exec` and `kubectl port-forward` fail for pods on one node, with a TLS
error from that node's kubelet. Other commands work. The API server checks the kubelet's serving
certificate when it connects, and that node has none yet. Its request for one is pending, as the
next section describes.

## Kubelet certificate requests stay pending

Each kubelet requests a serving certificate through a CertificateSigningRequest. chalkd on every
control plane approves a request when the node asks for its own name, for server use only, and for
addresses and names its Node object reports. It leaves every other request pending. List the
requests:

```sh
kubectl get csr
```

A request of signer `kubernetes.io/kubelet-serving` whose condition stays `Pending` was refused.
The chalkd journal of a control plane names the reason once per request:

```sh
chalkctl logs cp1 --unit=chalkd.service
```

The line reads `leaving certificate request <name> pending: <reason>`. The most common reasons:

- `node w1 does not report the address <address>`: the kubelet asks for an address the Node
  lacks. Compare `kubectl get node w1 -o wide` with the node's `Network` and `Node` documents.
- `node w1 has no Node object`: the kubelet has not registered its Node yet.

chalkd looks at the pending requests every 10 seconds and checks each one again, so it approves a
request once the Node reports what the request asks for. chalkd approves no client certificate
requests. The controller-manager approves those when a kubelet renews its own client certificate.
[How do workers join?](../how-it-works/kubernetes/kubernetes.md#how-do-workers-join) explains
both certificates.

## Pods on different nodes cannot reach each other

Pod traffic between nodes travels in flannel's VXLAN, on UDP port 8472. Three causes stop it:

- A node whose address lies outside `vxlanSourceSubnets` of the `Flannel` document refuses to
  prepare, and its line says `the node's address ... is outside vxlanSourceSubnets`. The other
  nodes would drop its VXLAN. Widen the ranges in the `Flannel` document. chalkctl refuses an
  inventory whose nodes hold different `Flannel` specs, so every node gets the same ranges.
- Small packets pass and large ones hang, as with TLS handshakes or large responses. The VXLAN MTU
  is too large for the network. Set `spec.mtu` of the `Flannel` document to the network's MTU,
  minus 20 when IPv6 is in use, because IPv6 VXLAN takes 70 bytes of each packet and IPv4 VXLAN 50.
- A firewall between the nodes drops UDP port 8472.

[Flannel sets the pod network's MTU and VXLAN
sources](configure.md#flannel-sets-the-pod-networks-mtu-and-vxlan-sources) shows the document.

## A Node has no pod range

A worker's line ends in:

```text
the Node has no ipv6 pod range; the control plane allocates ranges of the families the cluster was created with
```

The controller-manager allocates pod ranges of the families the control planes run with. This
worker's `Cluster` document names a family the cluster was not created with, which happens when a
new worker is installed after `ipFamilies` changed in the inventory. Running nodes refuse that
change, but an install has no running config to compare with. Restore the original `ipFamilies` and
reinstall the worker, as [Reinstall a node on its
machine](../operate/add-remove-nodes.md#reinstall-a-node-on-its-machine) describes. [IP families
and ranges are fixed at creation](plan.md#ip-families-and-ranges-are-fixed-at-creation) explains
why.

## The kubelet certificate was lost with VAR

A worker's kubelet renews its client certificate itself and keeps it on VAR. A worker whose VAR
lost its data falls back to the kubelet certificate in its Kubernetes share on STATE, which it
received at install. When that one expired too, the kubelet cannot join, and the certificates
section of `chalkctl status w1` says:

```text
certificates:
  ...
  kubelet client  expires 2026-09-14  expired; deliver a new one with chalkctl apply-config w1 --kubernetes-share
```

Deliver a new share with a new kubelet certificate:

```sh
chalkctl apply-config w1 --kubernetes-share --secrets=secrets.age
```

chalkctl issues the certificate from the Kubernetes CA in the secrets file.
[Certificates](../how-it-works/node/certificates.md) lists every certificate and its lifetime.

## What next

- [Troubleshooting](../operate/troubleshooting.md) covers the node: boot, disks, upgrades and
  node certificates.
- [Status, logs and the console](../operate/status-and-logs.md) explains each line of
  `chalkctl status` and the units to read.
- [Run a highly available control plane](ha-control-plane.md) covers etcd's members and how to
  bring a control plane back.
- [Kubernetes on chalkos](../how-it-works/kubernetes/kubernetes.md) explains bootstrap, join and
  the certificates.
