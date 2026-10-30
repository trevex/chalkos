---
title: "Separate routing with VRFs"
description: "Put a router's links into VRFs, run a BGP instance in each and import routes between them, while the node API stays in the default VRF"
---

# Separate routing with VRFs

A VRF gives a set of links a routing table of their own. The router routes a packet that arrives
on a VRF's link by that table alone, so two VRFs may use the same addresses. This page puts links
into VRFs, runs BGP in each, imports routes from one VRF into another and checks the result.
[VRFs on a router](../how-it-works/router/vrfs.md) explains how chalkos renders them.

## Before you begin

You need a router set up as [Set up a router](set-up.md) describes, and the secrets file. The
examples configure the edge `r1`. Its management link `eth0` stays out of every VRF. Its uplink
`eth1` goes into the VRF `internet`, and `eth2` and the `swp` links to a tenant's leaves go into
the VRF `tenant`. Each link has a `Network` document that names it in `[Match] Name=`:

```yaml
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-mgmt}
spec:
  match: {name: eth0}
  network: {dhcp: "yes"}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-internet}
spec:
  match: {name: eth1}
  network: {address: [203.0.113.1/24], ipv6AcceptRA: false}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 30-tenant}
spec:
  match: {name: "eth2 swp1 swp2"}
  network: {ipv6AcceptRA: false}
```

A VRF reaches a link through the `Network` document that networkd picks for it, so a link without
one cannot join a VRF. Name links by their interface names, without other `[Match]` keys.

## Keep the node API in the default VRF

The default VRF holds every link that no VRF holds. chalkd, chrony, the node's own name
resolution, image pulls and every other service of the node run there and in no other VRF. The
router sets `net.ipv4.tcp_l3mdev_accept`, `udp_l3mdev_accept` and `raw_l3mdev_accept` to 0, so a
socket of the default VRF takes no connection or datagram from a VRF's link, whatever the firewall
lets in.

Keep the link you reach the router through out of every VRF. The `Router` document of `r1` names
it as the node API's interface:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: Router
metadata: {name: router}
spec:
  management: {interfaces: [eth0]}
```

The rules refuse a VRF that holds an interface of `Router.spec.management.interfaces`, and match
globs as globs on both sides, so a VRF's `eth*` holds the management interface `eth0`:

```text
chalkctl: r1: the config is refused: the management interface eth0 is in a VRF, and chalkd answers in the default VRF alone; keep the node API's interfaces out of every VRF
```

A router without management interfaces answers the node API on every interface. The rules then
want at least one link that a `Network` document names and no VRF holds. They cannot know which
link chalkctl reaches the router through, so deliver every change of VRFs as a trial. The router
rolls the trial back when chalkctl cannot reach it over a new connection:

```sh
chalkctl apply-config r1 --try 2m --secrets secrets.age
```

## Declare the VRFs

A `VRF` document names the VRF, which is also the name of its kernel device, and gives its routing
table and its links:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: VRF
metadata: {name: internet}
spec: {table: 100, interfaces: [eth1]}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: VRF
metadata: {name: tenant}
spec: {table: 200, interfaces: [eth2, "swp*"]}
```

A name has 15 characters at most, and `default` names the default VRF. A table belongs to one VRF
and is none of 253, 254 and 255, the kernel's tables of the default VRF. An interface is a name or
a glob of a prefix and `*`, such as `swp*`. A glob stands for the links that the `Network`
documents name, and an interface or glob belongs to one VRF.

The router adds `VRF=` to the `Network` that networkd picks for each of a VRF's links. All links
that one `Network` may match belong to the same VRF, or to none. `30-tenant` matches `eth2`,
`swp1` and `swp2`, which `tenant` holds, so it gets `VRF=tenant`. A `Network` of `Name=swp*` while
`tenant` holds `swp1` alone is refused, because it may match a link outside the VRF.

A `Network` may set `network.vrf` itself, for a VRF of a `NetDev` of `kind: vrf` that you write
yourself. It may not name another VRF than the `VRF` document that holds its links.

## Run BGP in a VRF

A router has one `BGP` document per VRF. `spec.vrf` names the VRF, and a document without it
serves the default VRF:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: BGP
metadata: {name: bgp}
spec:
  asn: 64512
  routerID: 192.0.2.1
---
apiVersion: router.chalkos.dev/v1alpha1
kind: BGP
metadata: {name: internet}
spec:
  vrf: internet
  asn: 64512
  routerID: 192.0.2.1
  networks: [{prefix: 198.51.100.0/24}]
---
apiVersion: router.chalkos.dev/v1alpha1
kind: BGP
metadata: {name: tenant}
spec:
  vrf: tenant
  asn: 64512
  routerID: 192.0.2.1
  bfd: {receiveMs: 100, transmitMs: 100}
```

Each instance has its own AS number, router ID, multipath, BFD timers, graceful restart and
networks. It announces a network while its VRF's table holds a route to it.

A `BGPPeer` names its VRF the same way:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: BGPPeer
metadata: {name: transit}
spec:
  vrf: internet
  address: 203.0.113.254
  remoteAS: 64496
  families: {ipv4: {}}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: BGPPeer
metadata: {name: leaves}
spec:
  vrf: tenant
  interfaces: ["swp*"]
  remoteAS: external
  bfd: true
  extendedNextHop: true
  families: {ipv4: {}, ipv6: {}}
```

A peer's VRF needs a `BGP` document. A peer's `interfaces` stand for links of its VRF, so `swp*`
in `tenant` runs a session on `swp1` and one on `swp2`. In the default VRF, a glob matches the
links that no VRF holds. A peer's `interface` is a link of its own VRF, and a link that no
`Network` names is in the default VRF. The firewall accepts the BGP and BFD ports of a VRF's peer
on the links of that VRF.

## Import routes from another VRF

`spec.import` of a `BGP` document takes the routes of other VRFs' BGP instances, per family:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: BGP
metadata: {name: bgp}
spec:
  asn: 64512
  routerID: 192.0.2.1
  import:
    ipv6: {vrfs: [tenant], policy: from-tenant}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: BGP
metadata: {name: tenant}
spec:
  vrf: tenant
  asn: 64512
  routerID: 192.0.2.1
  bfd: {receiveMs: 100, transmitMs: 100}
  import:
    ipv4: {vrfs: [internet]}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: RoutingPolicy
metadata: {name: from-tenant}
spec:
  terms: [{match: {prefixList: tenant}, then: {action: accept}}]
---
apiVersion: router.chalkos.dev/v1alpha1
kind: PrefixList
metadata: {name: tenant}
spec:
  family: ipv6
  entries: [{prefix: "2001:db8:100::/40", le: 64}]
```

The default VRF takes the tenant's IPv6 routes within `2001:db8:100::/40`, and `tenant` takes
every IPv4 route of `internet`. The kernel installs an imported route in the importing VRF's table
with the next hop it had, on a link of the VRF it came from. `default` names the default VRF's
instance.

`policy` names a `RoutingPolicy` that decides which routes the instance takes from all of `vrfs`,
and without one it takes every route. FRR has one such policy per family, so its prefix lists are
of that family. An instance imports no routes of its own VRF, and each VRF it names needs a `BGP`
document. Only BGP's routes are imported. The static routes of a VRF's `Network` documents stay in
that VRF.

A `RoutingPolicy` with `prepend` repeats the AS number of the instances that use it, so instances
of different AS numbers cannot share it.

## Check that it worked

Deliver the documents as a trial, as above. `chalkctl status` lists the sessions with their VRF,
the default VRF's first:

```sh
chalkctl status r1 --secrets secrets.age
```

```text
bgp:
  VRF       PEER     NEIGHBOR       STATE        UPTIME  RECEIVED  SENT  BFD
  internet  transit  203.0.113.254  Established  2h0m0s  812       1     -
  tenant    leaves   swp1           Established  1h2m3s  4         9     up
  tenant    leaves   swp2           Established  1h2m3s  4         9     up
```

`chalkctl node routes --vrf` lists a VRF's table and the paths of its instance:

```sh
chalkctl node routes r1 --vrf tenant --secrets secrets.age
```

Without `--vrf`, the command lists the main table and the default VRF's paths. A path that an
instance imported names its VRF as its peer:

```sh
chalkctl node routes r1 2001:db8:100::/40 --secrets secrets.age
```

```text
DESTINATION        TYPE     PROTOCOL  TABLE  METRIC  NEXT HOPS
2001:db8:100::/48  unicast  bgp       254    20      fe80::b817:4cff:fe8b:9cf on swp1

BGP PREFIX         BEST  AS PATH  PEER        NEXT HOPS                 LOCAL PREF  MED  COMMUNITIES
2001:db8:100::/48  *     64601    vrf tenant  fe80::b817:4cff:fe8b:9cf  -           0    -
```

The route sits in the main table, and its next hop is on `swp1`, a link of `tenant`. `--vrf` and
`--table` exclude each other, and `--vrf default` is the main table. The diagnostics
`bgp-summary`, `bgp-neighbor`, `bgp-route` and `bfd-peers` take `vrf`, which is `default` when it
is left out:

```sh
chalkctl node diag r1 --secrets secrets.age bgp-summary vrf=tenant
chalkctl node diag r1 --secrets secrets.age bgp-route vrf=internet family=ipv4 prefix=198.51.100.0/24
```

## Change a VRF's table

A change of `table` creates the VRF anew. Its links leave it and join it again, and the sessions
in it end and start over. Deliver the change as a trial, as any change of VRFs.

## Remove a VRF

Remove a VRF's `BGPPeer`, `BGP` and `VRF` documents in one delivery. The rules refuse a `VRF`
document's removal while a `BGP` or `BGPPeer` document still names the VRF. Once the VRF is gone,
its links are in the default VRF.

FRR keeps the default VRF's instance while another VRF's runs. The rules therefore refuse a
delivery that removes the default VRF's `BGP` document and keeps another VRF's instance running:

```text
chalkctl: r1: the config is refused: the config drops the default VRF's BGP document and keeps a running BGP instance of another VRF, which FRR does not allow; drop the other VRFs' BGP documents in the same delivery, or keep the default VRF's
```

## Fix a refused delivery

The rules read the documents' names and globs without knowing which links networkd matches. The
renderers of the outputs `networkd` and `frr` check the rest against the `Network` documents when the router
renders its config, before the delivery changes anything. `chalkctl validate` does not run them.
A renderer's refusal fails the delivery with a message for each output that refused, such as:

```text
output networkd: its renderer failed (exit 1); see the node's journal
```

`chalkctl logs r1` shows the reason on a line that starts with `chalkos-render:`. The renderers
refuse what networkd or FRR would apply otherwise than the documents say:

| Message | Fix |
| --- | --- |
| `VRF tenant names the interface eth9, which no Network document's [Match] Name= matches without other [Match] keys; VRF= reaches a link through its Network document, so give it one` | Add a `Network` that names the link in `Name=` without other `[Match]` keys. |
| `Network 20-both matches links of no VRF and VRF a; the links of one Network are in one VRF` | Split the `Network`, so that each matches the links of one VRF. |
| `Network 10-swp's Name=swp* may match links of VRF tenant and links outside it; name its links` | Name the links in `Name=`, or put the whole glob into the VRF. |
| `Network 00-all matches the device of VRF tenant before 00-vrf-tenant; rename it to sort after 00-vrf-tenant` | Rename the `Network`, or narrow its `Name=`. |
| `Network 10-a is the first to match eth5, so networkd never applies Network 20-b's VRF=mgmt to it; give 10-a VRF=mgmt, rename it to sort after 20-b, or drop 20-b's VRF=` | Take one of the remedies the message lists. |
| `Network 20-x: Name= holds "eth[0", which is no glob the renderer can match as networkd does` | Write `Name=` without backslashes and bracket expressions. |
| `BGPPeer leaves: interfaces: swp* matches no link of VRF default, only links of other VRFs` | Give the peer the VRF of its links. |
| `BGPPeer x: interface tenant is the device of VRF tenant` | Name the VRF's links, not its device. |

## Limits

- The firewall's zones and NAT rules, NAT64 and the resolver do not take VRFs into account. The
  kernel reports a VRF's device as the incoming interface of a packet from the VRF's links, so a
  `FirewallZone` that names a link a VRF holds matches none of that link's traffic. Its policies
  then accept nothing from the link, and the chains' defaults decide.
- networkd also matches `Name=` against a link's alternative names. The documents do not give
  them, so the renderer cannot tell which link a `Network` matches by an alternative name. Name
  links by their interface names.
- Routes leak between VRFs through BGP alone.

## What next

- [VRFs on a router](../how-it-works/router/vrfs.md) explains the rendered units, the removal of
  VRFs, FRR's instances and the state chalkd reads.
- [Route with BGP](bgp.md) configures an instance, its peers and its policies.
- [Config kinds](../reference/kinds.md#router-chalkos-dev) describes each field.
