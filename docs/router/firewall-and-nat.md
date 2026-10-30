---
title: "Filter and translate traffic"
description: "Group a router's interfaces into zones, write firewall policies between them and to the router, and masquerade or forward ports with NAT rules"
---

# Filter and translate traffic

This page shows how to filter what a router forwards and accepts, and how to translate addresses.
A router without firewall documents forwards everything and accepts the node API and the traffic
chalkos protects. [The router's firewall](../how-it-works/router/firewall.md) explains the chains
these documents add to.

## Before you begin

The examples extend the router `r1` of [Set up a router](set-up.md), with its WAN on `eth0`, its
LAN on `eth1` and the VLAN interfaces `vlan*`, and its node API on `eth1`.

## Group the interfaces into zones

A zone names the interfaces whose traffic the policies treat alike:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: FirewallZone
metadata: {name: wan}
spec: {interfaces: [eth0]}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: FirewallZone
metadata: {name: lan}
spec: {interfaces: [eth1, "vlan*"]}
```

Give each interface one zone. The rules refuse a name or glob that two zones hold, but they do
not compare a glob with the names it matches: `eth*` in one zone and `eth0` in another pass, and
the policy whose name sorts first then takes `eth0`'s connections. The zone `local` is the router
itself and needs no document.

## Put VRFs into zones

On a router with [VRFs](vrfs.md), a zone can hold a whole VRF in `vrfs`. The links that join the
VRF later belong to the zone too:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: FirewallZone
metadata: {name: tenant}
spec: {vrfs: [tenant]}
```

To split one VRF's links into several zones, name the links in `interfaces` and leave the VRF out
of `vrfs`. A zone needs `interfaces`, `vrfs` or both, and a VRF is in one zone. A link of a VRF
that a zone holds is in no other zone. The output `router-nftables` refuses a zone that names a
link, or a glob that may match one, of a VRF that another zone holds. It also refuses an
interface or glob that matches a VRF's device, such as `ten*` beside the VRF `tenant`:

```text
FirewallZone x: ten* matches tenant, the device of VRF tenant; name the VRF in vrfs
```

The rules refuse an interface of a VRF's own name, such as `tenant`, before delivery, so
`chalkctl validate` catches it too.

Policies and NAT rules name these zones as any other. A policy between the zones of two VRFs
forwards only what the VRFs' tables route, and
[importing routes](vrfs.md#import-routes-from-another-vrf) puts the routes there. A NAT rule's
`in.interface` and `out.interface` name links: the renderer refuses one that matches a VRF's
device and no link, such as `internet` or `inter*`, while `*` translates on every link.

## Let the LAN reach the WAN

Without a policy between two zones, the router forwards everything. Once one policy goes between
zones, the router forwards only what a policy accepts. It drops traffic between
zones without a policy, and from or to an interface in no zone. Let the LAN reach the WAN:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: FirewallPolicy
metadata: {name: lan-wan}
spec: {from: lan, to: wan, default: accept}
```

Replies to the LAN's connections pass, because the firewall accepts established connections.
Nothing from the WAN reaches the LAN, since no policy goes from `wan` to `lan`.

The policy also stops the router forwarding between `eth1` and the VLANs, which share the zone
`lan`. Let them reach each other with a policy from the zone to itself:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: FirewallPolicy
metadata: {name: lan-lan}
spec: {from: lan, to: lan, default: accept}
```

## Allow connections to the router

A policy whose `to` is `local` filters connections to the router itself. Its rules match in
order, and the first that matches decides. `default` takes the rest, and is `drop` unless the
policy says otherwise:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: FirewallPolicy
metadata: {name: wan-local}
spec:
  from: wan
  to: local
  rules:
    - {protocol: tcp, destinationPorts: [8080], source: ["set:monitoring"], action: accept}
    - {protocol: tcp, destinationPorts: ["8443-8444"], source: [198.51.100.0/24, "2001:db8:3::/48"], action: accept}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: AddressSet
metadata: {name: monitoring}
spec: {family: ipv6, prefixes: ["2001:db8:2::/64"]}
```

An `AddressSet` holds ranges of one family. A rule names it as `set:<name>`, alone in its `source`
or `destination`. A port is a number or a range `"<first>-<last>"`. A policy cannot drop what the
firewall accepts before it: chalkd's port on the management interfaces, pings, ICMP errors,
neighbour discovery and established connections.

## Masquerade and forward ports

`NATRule` documents translate addresses, with or without policies. Masquerade the LAN's IPv4 out
of the WAN, and forward the WAN's port 8443 to a server on the LAN:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: NATRule
metadata: {name: masquerade}
spec:
  type: masquerade
  out: {zone: wan}
  match: {source: [192.0.2.0/24]}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: NATRule
metadata: {name: web}
spec:
  type: dnat
  in: {zone: wan}
  match: {protocol: tcp, destinationPorts: [8443]}
  to: {address: 192.0.2.10, port: 443}
```

A rule translates IPv4 unless its `family` is `ipv6`. `snat` translates the source to
`to.address` instead of the interface's address. The router forwards what a `dnat` rule translated
without a policy of its own.

NAT rules leave the connections of a NAT64 alone: they leave from the addresses of its pool, or
an empty pool's own masquerade translates them. The forward chain accepts a NAT64's translated
traffic ahead of the policies, so no policy filters it. A NAT rule may not translate to an
address of a NAT64's pool, nor a `dnat` rule match a destination in a pool or a NAT64 prefix.
[Translate to IPv4 and resolve names](nat64-and-dns.md) sets a NAT64 up.

## Deliver and check the firewall

Validate the files and deliver them as for any config:

```sh
chalkctl validate
chalkctl apply-config r1 --secrets secrets.age
```

The node checks the rendered ruleset with nft before it changes anything, and loads it in one
transaction. Show what it loaded:

```sh
chalkctl node diag r1 --secrets secrets.age nft-ruleset
```

When nft refuses the rendered ruleset at boot, the router loads the fallback ruleset instead,
which accepts chalkd's port on every interface and forwards nothing. The firewall's journal then
reads `nft refused /run/chalkos/outputs/router-nftables/ruleset.nft; loading the fallback ruleset,
which forwards nothing`:

```sh
chalkctl logs r1 --secrets secrets.age --unit=chalkos-firewall.service
```

The next delivery that changes the firewall documents loads the rendered ruleset again.

## A refusal names the document

The rules of the router kinds refuse documents that do not fit together, before chalkctl sends
anything. Each message names the document:

| Message | Fix |
| --- | --- |
| `FirewallPolicy lan-dmz names the zone dmz, which no FirewallZone document declares` | Add the zone, or name one that exists. |
| `the interface eth1 is in more than one FirewallZone, or twice in one; give each interface one zone` | Keep the interface in one zone. |
| `FirewallZone guest names the VRF guest, which no VRF document declares` | Add the `VRF` document, or fix the name in `vrfs`. |
| `the VRF tenant is in more than one FirewallZone, or twice in one; give each VRF one zone` | Keep the VRF in one zone, or name its links in the zones instead. |
| `more than one FirewallPolicy goes from lan to wan; give each pair of zones one policy` | Merge the policies' rules into one. |
| `FirewallPolicy "wan-local" spec.rules: a rule names an AddressSet that the config does not hold` | Add the `AddressSet`, or fix the name after `set:`. |
| `NATRule web has addresses of another family than ipv4` | Set `family: ipv6`, or give IPv4 addresses. |

[Built-ins](../reference/builtins.md#rules-router-images) lists every rule of the router kinds.

## What next

- [The router's firewall](../how-it-works/router/firewall.md) shows the chains these documents
  render.
- [Config kinds](../reference/kinds.md#router-chalkos-dev) lists every field.
