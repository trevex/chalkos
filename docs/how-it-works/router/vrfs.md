---
title: "VRFs on a router"
description: "How a router image runs VRFs: management in the default VRF, the units networkd gets, the unreachable defaults, chalkos-vrf, FRR's instances, the firewall, NAT64s and resolvers in VRFs, and chalkd"
---

# VRFs on a router

A router image turns each `VRF` document into a kernel VRF through networkd, runs a BGP instance
per VRF in FRR and imports routes between them with FRR's `import vrf`. For the edge of
[Separate routing with VRFs](../../router/vrfs.md), `chalkctl status` lists sessions in two VRFs:

```text
bgp:
  VRF       PEER     NEIGHBOR       STATE        UPTIME  RECEIVED  SENT  BFD
  internet  transit  203.0.113.254  Established  2h0m0s  812       1     -
  tenant    leaves   swp1           Established  1h2m3s  4         9     up
  tenant    leaves   swp2           Established  1h2m3s  4         9     up
```

The node's own services stay in the default VRF, which holds every link that no VRF holds.

## Management stays in the default VRF

chalkd, chrony, the node's own name resolution, image pulls and the health check's connection to
chalkd run in the default VRF and know nothing of VRFs. The management VRF is the default VRF,
together with the rule that no VRF holds a management interface. Data-plane traffic goes into
named VRFs.

The output `router-sysctl` sets `net.ipv4.tcp_l3mdev_accept`, `udp_l3mdev_accept` and
`raw_l3mdev_accept` to 0. The three settings cover IPv6 as well. A socket of the default VRF then
takes no connection or datagram from a VRF's link, so chalkd cannot be reached through a
data-plane VRF, even where a firewall rule would let the packet in. The kernel's default for raw
sockets is 1, so the router sets all three.

Two rules guard the node API. A VRF may not hold an interface of
`Router.spec.management.interfaces`, and the rule matches the globs on both sides as globs. While
the node API answers on every interface, a `Network` document must name at least one link that no
VRF holds. The rules cannot tell which link chalkctl reaches the router through, so a delivery
with `apply-config --try` covers the rest: the router rolls the config back unless chalkctl
confirms it over a new connection.

## The output networkd renders the VRFs

On a router image the output `networkd` reads the `VRF` documents beside networkd's own and
renders them with `chalkos-render router-networkd`. The output keeps networkd's directory,
`/run/systemd/network`, which no other output may share. The VRF `tenant` renders
`00-vrf-tenant.netdev`:

```ini
[NetDev]
Kind=vrf
Name=tenant

[VRF]
Table=200
```

It also renders `00-vrf-tenant.network`, which brings the device up, as networkd leaves a device
without a matching `.network` down. `RequiredForOnline=no` leaves the device out of the node's
online state. Its routes end the VRF's table in unreachable default routes:

```ini
[Match]
Name=tenant

[Link]
RequiredForOnline=no

[Route]
Destination=0.0.0.0/0
Metric=4278198272
Table=200
Type=unreachable

[Route]
Destination=::/0
Metric=4278198272
Table=200
Type=unreachable
```

When the VRF's table has no route for a destination, the kernel's lookup goes on to the main
table. Without these routes, a VRF's traffic would follow the default VRF's routes and
leave through its links. FRR reads the metric as distance 255, so a default route of BGP or of a
member's `Network` wins over them. A default route among a VRF instance's `networks` would match
them at all times, so the output `frr` refuses one and points to a peer family's
`defaultOriginate` instead.

The `Network` documents of the VRF's links get `VRF=` in a `[Network]` section:

```ini
[Match]
Name=eth2 swp1 swp2

[Network]
IPv6AcceptRA=false
VRF=tenant
```

## Membership follows how networkd picks a Network

networkd applies to a link the first `.network` file, by file name, whose `[Match]` matches it. The
renderer reads the `Network` documents the same way, so `VRF=` lands on the file that networkd
applies to each of a VRF's links:

- It reads `Name=` as systemd does. A value splits into words at white space, an empty value
  resets the words before it, and a value whose first word starts with `!` negates all its words.
  It drops the words systemd drops, such as an interface index or `all`.
- A literal interface of a `VRF` document is the link of that name. A glob stands for the links
  that the `Network` documents may match, up to the first `Network` that matches all of them.
- A `Network` with `[Match]` keys besides `Name=`, such as `MACAddress=`, may match a link that its
  `Name=` matches, or not. The renderer cannot tell, so it reads later documents too, and it
  refuses a VRF's link that only such a `Network` matches.
- A `Network` gets the VRF of the links it is the first to match. Every link it may match belongs
  to that VRF, or the renderer refuses the document.
- A `Network` that sorts before `00-vrf-<name>` and matches the VRF's device is refused, because
  networkd would apply it to the device.

The renderer also refuses a link that globs of two VRFs may hold and a `Network` whose own `VRF=`
names another VRF. A `Network` whose own `VRF=` an earlier `Network` hides from a link is
refused, with the remedies: give the earlier one the same `VRF=`, rename it to sort after, or drop
the `VRF=`. A networkd document named as a VRF's units and a `NetDev` that creates a device of a
VRF's name are refused too.

The renderer refuses a backslash in `Name=`, which systemd reads as an escape, and a bracket
expression it cannot match as `fnmatch` does, such as `eth[0` or a class like `[[:digit:]]`.

## chalkos-vrf removes what networkd keeps

`networkctl reload` creates a VRF whose `.netdev` is new and moves links in and out of VRFs as
their `.network` files say. It does not change the table of a VRF that exists, and it does not
remove a VRF whose `.netdev` went. `chalkos-vrf.service` covers both. It reads the `.netdev` files
of `Kind=vrf` from `/etc/systemd/network`, `/run/systemd/network`, `/usr/local/lib/systemd/network`
and `/usr/lib/systemd/network`, with the first of a name winning as networkd reads them. It then
removes each kernel VRF that no file declares with its table.

The unreachable default routes stay in a table after its VRF goes, and a VRF that takes the table
later would find them there. `chalkos-vrf.service` therefore flushes them from the old table
first, and deletes the VRF only once they went. A run that fails between the two finds the VRF
again and flushes once more.

After it removes a VRF, `chalkos-vrf.service` reloads a running networkd, which creates the VRF
anew with its new table and puts its links back into it. A change of `table` therefore ends the
sessions of the VRF, and they start over.

The effect `vrf` reloads `chalkos-vrf.service` whenever the output `networkd` changes. chalkd
reloads the units of one delivery in one `systemctl reload`, and the units' order applies:
`chalkos-vrf.service`, then `systemd-networkd.service`, then `frr.service`. At boot,
`chalkos-vrf.service` runs after `chalkos-config.service` and before networkd.

## FRR runs an instance per VRF

The output `frr` reads the `VRF` and `Network` documents beside the BGP kinds. It renders a
`router bgp` block per `BGP` document, the default VRF's first:

```text
router bgp 64512
 bgp router-id 192.0.2.1
 no bgp default ipv4-unicast
 !
 address-family ipv4 unicast
  maximum-paths 1
  maximum-paths ibgp 1
 exit-address-family
 !
 address-family ipv6 unicast
  maximum-paths 1
  maximum-paths ibgp 1
  import vrf route-map rp-from-tenant
  import vrf tenant
 exit-address-family
exit
!
...
router bgp 64512 vrf tenant
 bgp router-id 192.0.2.1
 no bgp default ipv4-unicast
 neighbor peer-leaves peer-group
 neighbor peer-leaves remote-as external
 neighbor peer-leaves bfd
 neighbor peer-leaves bfd profile chalkos-tenant
 neighbor peer-leaves capability extended-nexthop
 neighbor swp1 interface peer-group peer-leaves
 neighbor swp2 interface peer-group peer-leaves
```

A peer group sits in the block of its peer's VRF. Prefix lists, community lists and route-maps are
FRR's global objects, which every instance may use. Each instance with a BFD peer has a BFD
profile of its timers: `chalkos` for the default VRF's, `chalkos-<vrf>` for another's. FRR finds
the kernel's VRFs itself, so an instance whose VRF networkd has not created yet waits for it.

A peer's `interfaces` expand only over the links of the peer's VRF, as the membership rules above
find them. For a peer of the default VRF, those are the links of no VRF. The renderer refuses a
glob that matches links of other VRFs alone, a peer's `interface` that lies in another VRF and a
peer that names a VRF's device, on which bgpd runs no session.

A route-map's `set as-path prepend` repeats an AS number, and one route-map serves every instance
that names its policy. The renderer prepends the AS number of the instances that use the policy,
and refuses a policy with `prepend` that instances of two AS numbers use.

FRR keeps its default instance while another VRF's runs, and `frr-reload.py` reports the failed
removal without failing. A rule therefore refuses a delivery that drops the default VRF's `BGP`
document and keeps another VRF's running instance.

## Imported routes keep their next hop

`import vrf` copies the best paths of another VRF's instance into the importing instance. zebra
installs them in the importing VRF's table with the next hop they had, on a link of the other VRF.
The kernel routes a packet of the default VRF out of a VRF's link this way.

FRR marks an imported path as the instance's own and names the VRF it came from. chalkd reports
that VRF, and `chalkctl node routes` prints it as the path's peer, such as `vrf tenant`. When FRR
names no VRF of the next hop, chalkd reports the route distinguisher the path came from instead.

## The firewall accepts a VRF peer's ports on its links

For a packet that a VRF's link delivers to the router, the input hook of nftables sees the VRF's
device as `iifname` and the link as `sdifname`. The input chain therefore accepts the ports of a
`BGPPeer` with `vrf` and `interface` or `interfaces` by both:

```text
iifname "tenant" meta sdifname { "swp*" } tcp dport { 179 } accept comment "BGPPeer leaves"
iifname "tenant" meta sdifname { "swp*" } udp dport { 3784, 3785 } accept comment "BGPPeer leaves"
```

The rule matches the peer's globs against the links of its VRF alone, so a link of another VRF
whose name the glob matches stays closed. A peer of the default VRF keeps its `iifname` rules,
which match no VRF's link.

A peer of `address` with `vrf` is accepted from that address on the VRF's device, so the same
address in another VRF stays closed:

```text
iifname "internet" ip saddr { 203.0.113.254/32 } tcp dport { 179 } accept comment "BGPPeer transit"
```

The rule of a peer of `address` in the default VRF names no interface, as on a router without
VRFs. No socket of the default VRF takes a connection from a VRF's link, so bgpd answers it in
the default VRF alone.

## Zones and conntrack zones take VRFs

A `FirewallZone` holds whole VRFs in `vrfs` or single links of a VRF in `interfaces`. A VRF with
`conntrackZone` keeps its connections in a conntrack zone of its own, and the rules refuse an
import into or out of such a VRF. [The router's firewall](firewall.md#zones-of-vrfs) shows how
the ruleset matches them.

## NAT64s and resolvers join their VRFs

A `NAT64` with `vrf` runs in a network namespace of its own, as every NAT64 does. Its link
`n64-<name>` is a member of the VRF that no `VRF` document names: `chalkos-nat64.service` puts it
into the VRF's device itself, and the output `networkd` renders a `.network` that leaves it to the
unit. The VRF's table routes the NAT64's prefix and explicit pool to the link, and the forward chain matches
the link's packets by `meta sdifname`. The renderer adds the link to the VRF's conntrack zone, so
a connection and its translation share the zone.
[Each instance runs in a namespace of its own](nat64-and-dns.md#each-instance-runs-in-a-namespace-of-its-own)
shows the path of a packet.

A `Resolver` with `vrf` runs as `unbound@<vrf>.service`. Its unit sets
`BindNetworkInterface=<vrf>`, so systemd binds each of Unbound's sockets to the VRF's device: the
resolver answers on the VRF's links and sends its queries through the VRF's table, while the unit
keeps Unbound's hardening. `ip vrf exec` would need privileges that the hardening takes away. The
unit is bound to the VRF's device unit, and stops when networkd removes the device.
`chalkos-resolvers.service` starts it again once networkd has created the VRF anew. The input
chain opens the resolver's port 53 with the VRF's device as `iifname`.

## chalkd reads the VRFs' state

`chalkctl status` lists the sessions of every VRF. chalkd asks vtysh for
`show bgp vrf all neighbors json` and reports each session's VRF, the default VRF's sessions
first.

The `Routes` call takes a VRF. chalkd reads its table from the node's running `VRF` document,
below `/run/chalkos/config/router.chalkos.dev/VRF`, lists the kernel's routes of that table and
asks for `show bgp vrf <name> ipv4 unicast detail json` and its IPv6 twin. A request names a VRF
or a table, not both, and `default` is the main table and the default VRF's instance. A VRF of no
running document is refused with `the node has no VRF <name>`.

The diagnostics `bgp-summary`, `bgp-neighbor`, `bgp-route` and `bfd-peers` take a `vrf` argument,
`default` unless it is given, and pass it to vtysh as `vrf <name>`.

The status lists each VRF's resolver after the default VRF's, as `resolver <vrf>: running`. chalkd
asks systemd whether `unbound@<vrf>.service` is active.

## Limits

- The kernel's rule for the local table comes before the VRFs' tables. A VRF's packet to an
  address of a default VRF's link is delivered to the router, and the kernel answers it, such as
  with an echo reply or a TCP reset. chalkd stays out of reach, as no socket of the default VRF
  takes it. The router's IPv6 echo reply to an address that a VRF and the default VRF both hold
  leaves through the default VRF.
- No protected rule covers the router's own traffic to its addresses in a VRF. The VRF's
  policies to `local` decide it.
- The rules read interface names and globs without knowing which links networkd matches. The
  renderers compare them with the names in the `Network` documents, not with the links the node
  has.
- networkd also matches `Name=` against a link's alternative names, which the documents do not
  give. The renderer reads interface names alone.
- Routes leak between VRFs through BGP alone.

## Related pages

- [Separate routing with VRFs](../../router/vrfs.md) configures VRFs.
- [BGP on a router](bgp.md): FRR's config, the passwords and the reload.
- [How a router works](overview.md): the layer's outputs and the boot order.
- [The router's firewall](firewall.md): the input chain, zones of VRFs and conntrack zones.
