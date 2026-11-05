---
title: "How a router works"
description: "What the router layer adds to a node: the router kinds, forwarding set by a Router document, the firewall, policy routing, BGP, NAT64, the resolver, the boot order and the management guard"
---

# How a router works

The router layer is what a variant adds with
[`router = true`](../../reference/mkimages.md#variantsrouter). A node of such an image forwards
IPv4 and IPv6 packets between its interfaces and accepts the kinds of
[`router.chalkos.dev`](../../reference/kinds.md#router-chalkos-dev). Its interfaces, addresses and
static routes stay networkd's `Network` and `NetDev` documents, as on every node.

## The router layer adds kinds, outputs and a firewall

A router image carries the base of every image and the router layer on top of it. An image
carries one layer at most, so a router image runs no Kubernetes. The router layer adds these
parts:

| Part | What it is |
| --- | --- |
| Kinds | `Router`, `FirewallZone`, `FirewallPolicy`, `AddressSet`, `NATRule`, `BGP`, `BGPPeer`, `PrefixList`, `CommunityList`, `RoutingPolicy`, `NAT64`, `Resolver`, `VRF`, `PolicyRoute`, `NextHop`, `DHCPSubnet` and `DHCPRelay`, which only router images accept. |
| Outputs | `router-sysctl`, the kernel settings of the `Router` document; `router-nftables`, the firewall of the firewall kinds, the chain `policy-routing` of the `PolicyRoute` documents, the ports of the `BGPPeer`, `Resolver`, `DHCPSubnet` and `DHCPRelay` documents and the lines around the NAT64s' links; `frr`, FRR's config of the BGP kinds; `jool`, an instance of Jool and its link per `NAT64`; `networkd`, which renders the `VRF` documents, the kernel's rules and next hops' routes of the `PolicyRoute` and `NextHop` documents and a unit per NAT64's link beside networkd's own; `unbound`, Unbound's config of each `Resolver`; `kea`, the config of Kea's DHCPv4 and DHCPv6 servers of each VRF whose links `DHCPSubnet` documents serve; and `dhcp-relay`, the config of dnsmasq's relay of each VRF whose links `DHCPRelay` documents relay. |
| Effects | `router-sysctl` restarts `systemd-sysctl.service`, `router-nftables` reloads `chalkos-firewall.service`, `frr` reloads `frr.service`, `vrf` reloads `chalkos-vrf.service`, which removes the VRFs networkd keeps, `jool` reloads `chalkos-nat64.service`, `unbound` reloads `unbound.service` and `resolvers` reloads `chalkos-resolvers.service`, which runs the VRFs' resolvers, when `unbound` or `networkd` changes. `unbound-start` restarts `unbound.service` when the default VRF's `Resolver` is added or removed. `dhcp` reloads `chalkos-dhcp.service`, which runs the DHCP servers, when `kea` or `networkd` changes. `dhcp-relay` reloads `chalkos-dhcp-relays.service`, which runs the relays, when `dhcp-relay` or `networkd` changes. |
| Firewall | The nftables table `inet chalkos` in place of NixOS's firewall. |
| FRR | zebra, bgpd, bfdd and staticd, and vtysh, which chalkd asks for the router's BGP sessions and paths. An image whose variant sets `chalkos.router.bgp.enable` to `false` carries no FRR and no other part of BGP: no kinds `BGP`, `BGPPeer`, `PrefixList`, `CommunityList` or `RoutingPolicy`, output `frr`, effect `frr` or diagnostics `bgp-summary`, `bgp-neighbor`, `bgp-route` and `bfd-peers`. chalkd then reports no BGP, and the firewall opens no BGP port. |
| Jool | The kernel module of the NAT64 instances, which run in network namespaces of their own, and `jool`, with which `chalkos-nat64.service` loads them and chalkd, through `nsenter`, reads their sessions and pools. An image whose variant sets `chalkos.router.nat64.enable` to `false` carries no Jool and no other part of NAT64: no kind `NAT64`, output `jool`, effect `jool`, `chalkos-nat64.service` or diagnostic `nat64-sessions`. |
| Unbound | The resolvers: `unbound.service` for the default VRF and `unbound@<vrf>.service` for each other VRF, and `unbound-control`, which reloads them and tells chalkd whether the default VRF's runs. An image whose variant sets `chalkos.router.resolver.enable` to `false` carries no Unbound and no other part of the resolver: no kind `Resolver`, output `unbound`, effects `unbound`, `unbound-start` and `resolvers` or `chalkos-resolvers.service`. |
| Kea | The DHCP servers: `kea-dhcp4.service` and `kea-dhcp6.service` for the default VRF and `kea-dhcp4@<vrf>.service` and `kea-dhcp6@<vrf>.service` for each other VRF, which `chalkos-dhcp.service` starts once their links exist, and whose control sockets tell chalkd their leases; and `chalkos-dhcp6-routes.service`, which routes the prefixes the DHCPv6 servers delegate. An image whose variant sets `chalkos.router.dhcp.enable` to `false` carries no Kea and no other part of DHCP: no kind `DHCPSubnet`, output `kea`, effect `dhcp` or diagnostic `dhcp-leases`. |
| dnsmasq | The DHCP relays: `dnsmasq-relay.service` for the default VRF and `dnsmasq-relay@<vrf>.service` for each other VRF, which `chalkos-dhcp-relays.service` starts and restarts. An image whose variant sets `chalkos.router.dhcpRelay.enable` to `false` carries no dnsmasq and no kind `DHCPRelay`, output or effect `dhcp-relay`. |
| Diagnostics | `traceroute`, the routers on the path to an address; `bgp-summary`, `bgp-neighbor`, `bgp-route` and `bfd-peers`, FRR's views of BGP and BFD; `nat64-sessions`, a NAT64 instance's sessions, read in its network namespace; and `dhcp-leases`, the DHCP servers' leases. |

[The router's firewall](firewall.md) describes the table, [BGP on a router](bgp.md) FRR's config,
[VRFs on a router](vrfs.md) the VRFs, [Policy routing on a router](policy-routing.md) the marks
and the kernel's rules, [NAT64 and DNS64 on a router](nat64-and-dns.md) Jool's instances
and Unbound's config, and [DHCP on a router](dhcp.md) Kea's servers and dnsmasq's relays.
[Built-ins](../../reference/builtins.md#outputs-router-images) lists the outputs, effects, rules
and diagnostics.

## The Router document sets the kernel's forwarding

A node has one `Router` document at most. A node without one takes the kind's defaults: it
forwards both families, filters reverse paths loosely, spreads flows over equal-cost routes by
their addresses, protocol and ports, and answers the node API on every interface. A LAN router's
`Router`:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: Router
metadata: {name: router}
spec:
  rpFilter: strict
  ecmpHash: l3
  conntrackMax: 262144
  management: {interfaces: [lan0]}
```

The output `router-sysctl` renders the kernel's part of it into
`/run/sysctl.d/90-chalkos-router.conf`:

```text
# The kernel settings of the node's Router, rendered by chalkos-render router-sysctl.
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1
net.ipv4.fib_multipath_hash_policy = 0
net.ipv6.fib_multipath_hash_policy = 0
net.ipv4.conf.all.rp_filter = 0
net.ipv4.conf.default.rp_filter = 0
net.ipv4.conf.*.rp_filter = 0
net.ipv4.tcp_l3mdev_accept = 0
net.ipv4.udp_l3mdev_accept = 0
net.ipv4.raw_l3mdev_accept = 0
net.netfilter.nf_conntrack_max = 262144
```

systemd-sysctl reads the file after the image's `/etc/sysctl.d/60-nixos.conf`, so these values
win over the image's own. The kernel's IPv4 reverse-path filter stays off, because the firewall
filters reverse paths in both families as `rpFilter` says. The glob `*` covers the interfaces
that exist when systemd-sysctl runs, and systemd's udev rules apply the file to each interface
that appears later. The image loads `nf_conntrack` at boot, so `nf_conntrack_max` exists when
systemd-sysctl runs. `rpFilter` and `management` go to the firewall.

The three l3mdev settings keep the default VRF's sockets, such as chalkd's, from answering on a
VRF's link; [VRFs on a router](vrfs.md#management-stays-in-the-default-vrf) explains why.

## A router boots with its firewall in place

`chalkos-config.service` renders every output at boot, before the network starts. The flowchart
shows what follows:

```mermaid
flowchart LR
    config["chalkos-config renders the outputs"] --> sysctl["systemd-sysctl applies router-sysctl"]
    config --> firewall["chalkos-firewall loads router-nftables"]
    firewall --> pre["network-pre.target"]
    pre --> networkd["networkd configures the interfaces"]
    config --> vrf["chalkos-vrf removes the VRFs no unit declares"]
    vrf --> networkd
    config --> frr["frr.service fills in the passwords and starts FRR"]
    firewall --> frr
    networkd --> frr
    firewall --> nat64["chalkos-nat64 runs the NAT64s in their namespaces and routes to them"]
    networkd --> nat64
    nat64 --> frr
    nat64 --> unbound["unbound.service starts the default VRF's resolver"]
    nat64 --> resolvers["chalkos-resolvers starts the VRFs' resolvers"]
    networkd --> resolvers
```

`chalkos-nat64` runs after networkd, because a NAT64's link joins a VRF that networkd creates.
It waits up to 30 seconds for each such VRF's device.

`chalkos-firewall.service` loads the rendered ruleset. When the node has none, it loads a fallback
ruleset that the image carries. That happens in maintenance mode, before the first install, and
when the node's config did not render at boot. The fallback accepts chalkd's port on every
interface and the traffic chalkos protects, and forwards nothing.

When nft refuses the rendered ruleset at boot, the unit loads the fallback and then fails. The
router stays reachable through chalkd and forwards nothing. The next delivery that changes the
ruleset restarts the failed unit, which loads the new ruleset.

## A delivery reloads the firewall in one transaction

A delivery renders and checks the outputs before it changes anything. The check of
`router-nftables` runs `nft --check` on the rendered ruleset in a user and network namespace of
its own, so the kernel refuses a ruleset it would not load before the node stores the config.
Once the config is stored, chalkd reloads `chalkos-firewall.service` and then restarts
`systemd-sysctl.service`, so forwarding never changes before the policies of the same delivery.
The reload loads the ruleset in one nftables transaction. The transaction replaces the table
`inet chalkos` and leaves every other table alone. A ruleset that nft refuses at that point
leaves the loaded one in place and fails the delivery. chalkd still restarts
`systemd-sysctl.service`, so the delivery's kernel settings apply with the old table.
A delivery that changes FRR's config reloads `frr.service` in the same call as the firewall, and
after it. [BGP on a router](bgp.md#a-delivery-reloads-frr) describes that reload. The same call
reloads `chalkos-nat64.service` after the firewall and networkd and before FRR, so a NAT64
prefix's route exists before FRR announces it. `unbound.service` and `chalkos-resolvers.service`
reload after `chalkos-nat64.service`.
[NAT64 and DNS64 on a router](nat64-and-dns.md#a-delivery-reloads-unbound-and-keeps-its-cache)
describes both.

## The management guard keeps the node API reachable

`Router.spec.management.interfaces` names the interfaces on which the firewall accepts chalkd's
port. A change rule guards them against a delivery that locks the operator out. A delivery that
keeps `spec.management.interfaces` must keep a `Network` document for each management interface
that a running `Network` matches. A delivery that removes the last `Network` of `lan0`, while
`lan0` is a management interface, is refused:

```text
the config is refused: Router/router: spec.management.interfaces: the config leaves the management interfaces lan0 without a Network document that matches them, so the router would no longer configure them; keep a Network that matches each, or change spec.management.interfaces in the same delivery
```

The rule reads the `Name=` of each `[Match]` section and splits it into names at spaces and tabs. It
compares names and globs, a prefix followed by `*`, by their prefixes:

- A name matches the same name, and a glob matches the names and globs its prefix starts.
- A `Network` glob such as `lan*`, or `lan0*`, covers the management interface `lan0`.
- The management interface `*` needs one `Network` with a `Name=` to remain.

A delivery that renames the `Network` and keeps its `[Match]` passes. So does one that moves the
match into another document, or changes `spec.management.interfaces` too. A router whose node API
answers on every interface, the default, has nothing to guard. The rule reads the running config,
so `chalkctl apply-config` and the node run it, and `chalkctl validate` does not.

The guard cannot tell which interface chalkctl reaches the router through. A delivery with
`apply-config --try` covers that case: the router rolls the config back at the trial's deadline
unless chalkctl confirms it over a new connection, which the new ruleset must accept.

## Limits

- An image carries the router layer or the Kubernetes layer, not both.
- The management guard compares the names and prefixes of `Name=` with the management
  interfaces. It does not ask networkd which interfaces a `Network` matches, so it catches a
  removed `Network` and proves nothing more. A `Network` glob that covers a name passes it whether
  or not networkd configures that interface with it, and a `[Match]` on anything but `Name=`
  counts for none.
- No policy filters the router's own outgoing traffic.

## Related pages

- [The router's firewall](firewall.md): the table, its chains and what each default does.
- [BGP on a router](bgp.md): FRR's config, the passwords and the state chalkd reads.
- [VRFs on a router](vrfs.md): the VRFs' units, chalkos-vrf and FRR's instances.
- [Policy routing on a router](policy-routing.md): the marks, the chain and networkd's rules.
- [NAT64 and DNS64 on a router](nat64-and-dns.md): Jool's instances and Unbound's config.
- [Run a router](../../router/index.md): set up a router and its firewall.
- [Config kinds](../../reference/kinds.md#router-chalkos-dev) for every field of the router kinds.
- [Ports and firewall](../../reference/ports-and-firewall.md#router-images).
