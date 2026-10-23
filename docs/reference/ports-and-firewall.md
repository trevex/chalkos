---
title: "Ports and firewall"
description: "Every port a chalkos node listens on, who needs to reach it, and how the firewall treats it"
---

# Ports and firewall

This page lists the ports a chalkos node accepts connections on, the ports its services listen
on locally, the nftables tables on a node and the connections a node makes. The firewall rules
come from `modules/node/chalkd.nix`, `modules/node/kubernetes.nix`,
`modules/node/controlplane-ports.nix` and `modules/node/vxlan-rule.nix`; the listeners from
`cmd/chalkd`, `pkg/kubernetes/manifests/manifests.go`, `pkg/kubernetes/addons` and
`modules/node/kubernetes.nix`.
[Networking](../how-it-works/node/networking.md) explains the network design and
[Security model](../how-it-works/node/security.md) what each listener trusts.

## Open ports

The firewall accepts new connections on these ports alone. Every node has the first; the others
depend on whether the image has Kubernetes, on the node's type (`Node.spec.type`:
`controlplane` or `worker`) and on the CNI.

| Port | Protocol | Service | Nodes | Connects to it |
| --- | --- | --- | --- | --- |
| 50000 | TCP | chalkd's node API, mutual TLS | every node, the installer too | chalkctl and chalklab; nodes renewing their node certificate, at a control plane through the cluster endpoint's host |
| 6443 | TCP | Kubernetes API server | control planes | kubectl, every kubelet and kube-proxy at the cluster endpoint; pods, flannel among them, through the `kubernetes` service |
| 2379 | TCP | etcd's client port | control planes | the other control planes |
| 2380 | TCP | etcd's peer port | control planes | the other control planes |
| 10250 | TCP | kubelet | control planes and workers | the API server |
| 30000–32767 | TCP | NodePort services | control planes and workers | clients of NodePort services |
| 8472 | UDP | flannel's VXLAN | control planes and workers, with flannel | the other nodes |

Ports 2379 and 2380 accept only certificates of the cluster's etcd CA, but a control plane's
firewall opens them to every source, not only to the other control planes' addresses.

The firewall opens the NodePort range for TCP only.

## VXLAN rule

flannel's VXLAN carries pod traffic unauthenticated, so a node accepts UDP 8472 only when it is
sent to one of the addresses the node picked for Kubernetes, and only from the ranges of the
[`Flannel`](kinds.md#flannel) document's `spec.vxlanSourceSubnets` when that list is set.
The rule has three parts:

| Part | What it does |
| --- | --- |
| Table `inet chalkos-vxlan`, chain `input` | Runs before the firewall and marks UDP 8472 sent to the node's address of each family with the bit `0x01000000`: on the interface holding that address, or on any interface but the pod network's and kube-proxy's when the address is on a loopback or dummy interface. With source ranges set, only packets from them get the mark, and a family without a range gets none. |
| Firewall's `input-allow` chain | Accepts UDP 8472 that carries the mark. |
| Table `inet chalkos-vxlan`, chain `clear` | Runs after the firewall and clears the mark, so no later rule sees it. |

With source ranges set, a `forward` chain in the same table also drops VXLAN that pods send
through the node to those ranges: the node masquerades pod traffic behind its own address,
which is in the ranges, so other nodes would take it for this node's. The chain takes the pod
range of each family in `ipFamilies` from the node's `Cluster` document and the source ranges
from its `Flannel` document on every run, and skips ranges of a family the image does not use.
While those ranges are unknown, because the node's config holds no `Cluster` document or
chalkcfg cannot read it or the `Flannel` document, or when they are not valid ranges, the chain
drops all UDP 8472 the node forwards.

`chalkos-vxlan-rule` fills the table from `/run/chalkos/kubernetes/vxlan`, which chalkd writes
once it picked the node's addresses at boot. Without that file, or with one it cannot parse, the
table holds no marking rule and the node refuses all VXLAN.

## Listeners the firewall keeps closed

These services listen on a loopback address, or on every address with the firewall dropping
connections from other machines. They are reachable from the node itself.

| Port | Protocol | Service | Address | Nodes |
| --- | --- | --- | --- | --- |
| 53 | UDP and TCP | systemd-resolved's stub resolver, which the node's own name lookups use | `127.0.0.53` and `127.0.0.54` | every node |
| 323 | UDP | chrony's command port, for chronyc | `127.0.0.1` and `::1` | every node |
| 2379 | TCP | etcd's client port, for the local API server | the loopback address of the primary family | control planes |
| 2381 | TCP | etcd's metrics and health, plain HTTP | the loopback address of the primary family | control planes |
| 8081 | TCP | flanneld's health | every address | nodes with flannel |
| 10248 | TCP | kubelet's health, the kubelet's default | `127.0.0.1` | control planes and workers |
| 10249 | TCP | kube-proxy's metrics, kube-proxy's default | `127.0.0.1` | control planes and workers |
| 10256 | TCP | kube-proxy's health, kube-proxy's default | every address | control planes and workers |
| 10257 | TCP | kube-controller-manager | `127.0.0.1` | control planes |
| 10259 | TCP | kube-scheduler | `127.0.0.1` | control planes |

CoreDNS answers on port 53 inside the pod network, at the cluster's DNS service addresses; it
opens no port on the node.

## What a node does not listen on

A node runs no SSH server, so port 22 is closed, and there are no logins on the console.
[Troubleshooting](../operate/troubleshooting.md) shows how to debug a node through
`kubectl debug node/<node>` and the node API. chrony keeps the clock and serves no NTP clients.

## Firewall tables

The NixOS firewall runs on nftables, as kube-proxy and flannel do. chalkos sets
`networking.nftables.flushRuleset = false`, so loading or reloading the firewall replaces its own
table and leaves the others in place.

| Table | Owner | Contents |
| --- | --- | --- |
| `inet nixos-fw` | NixOS firewall | The input policy: drop, except for the loopback interface, established and related connections, the open ports above, ICMP echo requests, ICMPv6 other than redirects and node information queries, DHCPv6 replies to link-local addresses, and the control plane's ports and VXLAN carrying their marks. A reverse-path check in prerouting drops packets from a source address the node has no route back to. |
| `inet chalkos-controlplane` | chalkos | On a control plane, the mark of the control plane's ports and its clearing. |
| `inet chalkos-vxlan` | chalkos | The VXLAN mark, its clearing and the forward drop. |
| `ip kube-proxy`, `ip6 kube-proxy` | kube-proxy | Service and NodePort translation, in kube-proxy's nftables mode. |
| flannel's tables | flanneld | Masquerading of pod traffic, with `EnableNFTables`. |
| the portmap plugin's table | CNI portmap plugin | Host ports of pods, with its nftables backend. |

The `input-allow` chain, as NixOS renders it for a node with Kubernetes and flannel:

```text
chain input-allow {
   tcp dport { 10250, 50000, 30000-32767 } accept
   meta l4proto . th dport @temp-ports accept
   icmp type echo-request  accept comment "allow ping"
   icmpv6 type != { nd-redirect, 139 } accept comment "Accept all ICMPv6 messages except redirects and node information queries (type 139).  See RFC 4890, section 4.4."
   ip6 daddr fe80::/64 udp dport 546 accept comment "DHCPv6 client"
   tcp dport { 6443, 2379, 2380 } meta mark & 0x02000000 == 0x02000000 accept
   udp dport 8472 meta mark & 0x01000000 == 0x01000000 accept
}
```

The chain also accepts 6443, 2379 and 2380 when they carry the mark `0x02000000`, which the
table `inet chalkos-controlplane` sets on a node whose `Node.spec.type` is `controlplane`.
`chalkos-controlplane.service` loads that table, or deletes it on a worker, at boot and whenever
the node's `Node` document changes. The table clears the bit on every packet the node receives
before it marks, and again after the firewall's chain, so a network plugin of the cluster's own
must not use it.

## Connections a node makes

A network firewall in front of the nodes needs to let these through, besides the open ports
between the nodes.

| Destination | Port | Protocol | Purpose |
| --- | --- | --- | --- |
| Container registries, or the endpoints their [`Mirror`](kinds.md#mirror) documents name | 443, or the endpoint's port | TCP | Images of the control plane's static pods, kube-proxy, flannel, CoreDNS and workloads. An endpoint may use plain HTTP when a [`Registry`](kinds.md#registry) of its host sets `spec.insecure`. |
| The registry of an install's or upgrade's image, when the inventory names the image in a registry, or the endpoints of its [`Mirror`](kinds.md#mirror) | 443, or the port of the image reference or the endpoint | TCP | chalkd pulling the image, on the installer during `chalkctl install` and on the node during `chalkctl upgrade`. Plain HTTP only when the [`Registry`](kinds.md#registry) of the host or endpoint sets `spec.insecure`. |
| Time servers of the [`TimeSync`](kinds.md#timesync) document's `spec.servers` | 4460 | TCP | NTS key exchange, for servers with `nts`, the default. |
| Time servers | 123, or the server's `port` | UDP | NTP. |
| DNS resolvers of the node's network | 53 | UDP and TCP | Names of registries and time servers. |
| DHCP servers, when the node's network units use DHCP | 67 | UDP | Addresses. |
| The cluster endpoint | 6443 | TCP | The kubelet and kube-proxy of every Kubernetes node. |
| The cluster endpoint's host | 50000 | TCP | Renewal of a worker's node certificate at a control plane, once two thirds of its lifetime have passed; control planes sign their own. |
| The other control planes | 2379, 2380 | TCP | etcd membership and replication. |
| The other Kubernetes nodes | 10250 | TCP | The API server reaching kubelets, from control planes. |
| The other Kubernetes nodes | 8472 | UDP | flannel's VXLAN. |

A VIP needs no port of its own: chalkd adds the address to an interface of the
control plane that holds it and announces it to the neighbours with ARP or IPv6 neighbour
advertisements.
