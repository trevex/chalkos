---
title: "Ports and firewall"
description: "Every port a chalkos node listens on, who needs to reach it, and how the firewall treats it"
---

# Ports and firewall

This page lists the ports a chalkos node accepts connections on, the ports its services listen
on locally, the nftables tables on a node and the connections a node makes. The firewall rules
come from `modules/node/chalkd.nix`, `modules/node/kubernetes.nix` and
`modules/node/vxlan-rule.nix`; the listeners from `cmd/chalkd`,
`pkg/kubernetes/manifests/manifests.go` and the cluster's Kubernetes modules.
[Networking](../concepts/networking.md) explains the network design and
[Security model](../concepts/security.md) what each listener trusts.

## Open ports

The firewall accepts new connections on these ports alone. Every node has the first; the others
depend on the [role](glossary.md#role)'s Kubernetes kind (`controlplane`, `worker` or none)
and on the CNI.

| Port | Protocol | Service | Nodes | Connects to it |
| --- | --- | --- | --- | --- |
| 50000 | TCP | [chalkd](glossary.md#chalkd)'s node API, mutual TLS | every node, the [installer](glossary.md#installer) too | chalkctl and chalklab; nodes renewing their [node certificate](glossary.md#node-certificate), at a control plane through the cluster endpoint's host |
| 6443 | TCP | Kubernetes API server | control planes | kubectl, every kubelet and kube-proxy at the cluster endpoint; pods, flannel among them, through the `kubernetes` service |
| 2379 | TCP | etcd's client port | control planes | the other control planes |
| 2380 | TCP | etcd's peer port | control planes | the other control planes |
| 10250 | TCP | kubelet | control planes and workers | the API server |
| 30000–32767 | TCP | NodePort services | control planes and workers | clients of NodePort services |
| 8472 | UDP | flannel's VXLAN | control planes and workers, with flannel | the other nodes |

Ports 2379 and 2380 accept only certificates of the cluster's etcd CA, but the firewall opens them
to every source, not only to the other control planes' addresses.

The firewall opens the NodePort range for TCP only.

## VXLAN rule

flannel's VXLAN carries pod traffic unauthenticated, so a node accepts UDP 8472 only when it is
sent to one of the addresses the node picked for Kubernetes, and only from
[`chalkos.cluster.kubernetes.vxlanSourceSubnets`](options.md#chalkosclusterkubernetesvxlansourcesubnets)
when that list is set. The rule has three parts:

| Part | What it does |
| --- | --- |
| Table `inet chalkos-vxlan`, chain `input` | Runs before the firewall and marks UDP 8472 sent to the node's address of each family with the bit `0x01000000`: on the interface holding that address, or on any interface but the pod network's and kube-proxy's when the address is on a loopback or dummy interface. With source ranges set, only packets from them get the mark, and a family without a range gets none. |
| Firewall's `input-allow` chain | Accepts UDP 8472 that carries the mark. |
| Table `inet chalkos-vxlan`, chain `clear` | Runs after the firewall and clears the mark, so no later rule sees it. |

With source ranges set, a `forward` chain in the same table also drops VXLAN that pods send
through the node to those ranges: the node masquerades pod traffic behind its own address,
which is in the ranges, so other nodes would take it for this node's.

`chalkos-vxlan-rule` fills the table from `/run/chalkos/kubernetes/vxlan`, which chalkd writes
once it picked the node's addresses at boot. Without that file, or with one it cannot parse, the
table holds no marking rule and the node refuses all VXLAN.

## Listeners the firewall keeps closed

These services listen on a loopback address, or on every address with the firewall dropping
connections from other machines. They are reachable from the node itself.

| Port | Protocol | Service | Address | Nodes |
| --- | --- | --- | --- | --- |
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
[Troubleshooting](../guides/troubleshooting.md) shows how to debug a node through
`kubectl debug node/<node>` and the node API. chrony keeps the clock and serves no NTP clients.

## Firewall tables

The NixOS firewall runs on nftables, as kube-proxy and flannel do. chalkos sets
`networking.nftables.flushRuleset = false`, so loading or reloading the firewall replaces its own
table and leaves the others in place.

| Table | Owner | Contents |
| --- | --- | --- |
| `inet nixos-fw` | NixOS firewall | The input policy: drop, except for the loopback interface, established and related connections, the open ports above, ICMP echo requests, ICMPv6 other than redirects and node information queries, DHCPv6 replies to link-local addresses, and VXLAN carrying the mark. A reverse-path check in prerouting drops packets from a source address the node has no route back to. |
| `inet chalkos-vxlan` | chalkos | The VXLAN mark, its clearing and, with source ranges, the forward drop. |
| `ip kube-proxy`, `ip6 kube-proxy` | kube-proxy | Service and NodePort translation, in kube-proxy's nftables mode. |
| flannel's tables | flanneld | Masquerading of pod traffic, with `EnableNFTables`. |
| the portmap plugin's table | CNI portmap plugin | Host ports of pods, with its nftables backend. |

The worker's `input-allow` chain, as NixOS renders it for a node with flannel:

```text
chain input-allow {
   tcp dport { 10250, 50000, 30000-32767 } accept
   meta l4proto . th dport @temp-ports accept
   icmp type echo-request  accept comment "allow ping"
   icmpv6 type != { nd-redirect, 139 } accept comment "Accept all ICMPv6 messages except redirects and node information queries (type 139).  See RFC 4890, section 4.4."
   ip6 daddr fe80::/64 udp dport 546 accept comment "DHCPv6 client"
   udp dport 8472 meta mark & 0x01000000 == 0x01000000 accept
}
```

A control plane's chain also accepts 6443, 2379 and 2380.

## Connections a node makes

A network firewall in front of the nodes needs to let these through, besides the open ports
between the nodes.

| Destination | Port | Protocol | Purpose |
| --- | --- | --- | --- |
| Container registries, or the mirrors in [`chalkos.cluster.registries.mirrors`](options.md#chalkosclusterregistriesmirrors) | 443, or the mirror's port | TCP | Images of the control plane's static pods, kube-proxy, flannel, CoreDNS and workloads. A mirror may use plain HTTP with [`chalkos.cluster.registries.allowPlainHTTP`](options.md#chalkosclusterregistriesallowplainhttp). |
| Time servers of [`chalkos.time.servers`](options.md#chalkostimeservers) | 4460 | TCP | NTS key exchange, for servers with [`nts`](options.md#chalkostimeserversnts), the default. |
| Time servers | 123, or the server's [`port`](options.md#chalkostimeserversport) | UDP | NTP. |
| DNS resolvers of the node's network | 53 | UDP and TCP | Names of registries and time servers. |
| DHCP servers, when the node's network units use DHCP | 67 | UDP | Addresses. |
| The cluster endpoint | 6443 | TCP | The kubelet and kube-proxy of every Kubernetes node. |
| The cluster endpoint's host | 50000 | TCP | Renewal of the node certificate at a control plane, once two thirds of its lifetime have passed. |
| The other control planes | 2379, 2380 | TCP | etcd membership and replication. |
| The other Kubernetes nodes | 10250 | TCP | The API server reaching kubelets, from control planes. |
| The other Kubernetes nodes | 8472 | UDP | flannel's VXLAN. |

A [VIP](glossary.md#vip) needs no port of its own: chalkd adds the address to an interface of the
control plane that holds it and announces it to the neighbours with ARP or IPv6 neighbour
advertisements.
