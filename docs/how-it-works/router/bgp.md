---
title: "BGP on a router"
description: "How a router image runs BGP: FRR, the config the output frr renders from the BGP kinds, the passwords, the reload, the boot order, the firewall's ports and the state chalkd reads"
---

# BGP on a router

A router image runs BGP with FRR: its daemons zebra, bgpd, bfdd and staticd, and vtysh, its shell.
The output `frr` renders FRR's whole config, an instance per VRF, from the node's `BGP`, `BGPPeer`,
`PrefixList`, `CommunityList`, `RoutingPolicy` and `VRF` documents. No document holds FRR commands
of its own.

## The output frr renders FRR's whole config

`chalkos-render frr` writes `frr.conf` from the documents. The edge of
[Route with BGP](../../router/bgp.md) renders, in part:

```text
frr defaults traditional
hostname r1
log syslog informational
service integrated-vtysh-config
!
ipv6 prefix-list pl-fabric seq 5 permit 2001:db8::/32 le 128
!
route-map rp-from-fabric permit 10
 match ipv6 address prefix-list pl-fabric
exit
route-map rp-from-fabric deny 65535
exit
!
router bgp 64512
 bgp router-id 192.0.2.1
 no bgp default ipv4-unicast
 bgp bestpath as-path multipath-relax
 neighbor peer-fabric peer-group
 neighbor peer-fabric remote-as external
 neighbor peer-fabric description the fabric's leaves
 neighbor peer-fabric password @secret_bgp.fabric@
 neighbor peer-fabric bfd
 neighbor peer-fabric bfd profile chalkos
 neighbor peer-fabric capability extended-nexthop
 neighbor eth1 interface peer-group peer-fabric
 neighbor eth2 interface peer-group peer-fabric
 !
 address-family ipv6 unicast
  network 2001:db8:ffff::1/128
  network 64:ff9b::/96 route-map rp-nat64
  maximum-paths 64
  maximum-paths ibgp 1
  neighbor peer-fabric activate
  neighbor peer-fabric default-originate
  neighbor peer-fabric route-map rp-from-fabric in
  neighbor peer-fabric route-map rp-to-fabric out
 exit-address-family
exit
!
bfd
 profile chalkos
  receive-interval 300
  transmit-interval 300
  detect-multiplier 3
 exit
exit
!
end
```

The config follows these rules:

| Documents | Config |
| --- | --- |
| `BGP` | `router bgp <asn>`, or `router bgp <asn> vrf <name>` for a document with `vrf`, with `frr defaults traditional` and every count and timer of the document written out, so FRR's defaults decide nothing the document sets. Both address families list the document's networks of the family and its `maximum-paths`. |
| `BGPPeer` | A peer group `peer-<name>` in the block of its VRF's instance, and a neighbour in it for the peer's address, its interface or each link its `interfaces` match. |
| A peer's family | `activate`, and a route-map in each direction: the family's policy, or `accept-all` without one. FRR's `ebgp-requires-policy` then always holds. |
| `PrefixList` | `ip prefix-list` or `ipv6 prefix-list pl-<name>`, the entries at sequence numbers 5, 10, 15 and on. |
| `CommunityList` | `bgp community-list standard cl-<name>`, or `bgp large-community-list` for large communities. |
| `RoutingPolicy` | `route-map rp-<name>`, the terms at sequence numbers 10, 20, 30 and on, and the default at 65535. A term's AS path and the communities it removes get lists of their own, `asp-<policy>-<seq>` and `rm-<policy>-<seq>`. |
| `bfd` of a peer | The profile of its instance's BFD timers: `chalkos` for the default VRF, `chalkos-<vrf>` for another. |

The prefixes keep the names apart. FRR names peers and interfaces alike, and without them a peer
group could take the name of an interface or of another list. The renderer refuses an interface
named like another peer's group, such as `peer-fabric`. A node without a `BGP` document renders
the first lines and `end` alone, and FRR runs no BGP.

The sequence numbers follow the positions of the entries and terms. A term put before others
renumbers those after it, and the reload rewrites the renumbered entries.

## A peer's interfaces expand against the Network documents

A peer's `interfaces` expand when the config renders. Each name or glob matches the names in the
`[Match]` sections' `Name=` of the node's `Network` documents, with the globs and negations among
those names left out. The leaf example's `interfaces: ["swp*"]` gives a session on each of `swp1`
and `swp2`, which its documents `20-swp1` and `20-swp2` name. Its document `30-swp` matches the
glob `swp*` and names no link of its own. A link that only a `Link` or `NetDev` names gets no
session. A glob expands over the links of the peer's VRF alone, as
[VRFs on a router](vrfs.md#frr-runs-an-instance-per-vrf) explains.

A peer's single `interface` renders as it is written, whether or not a `Network` names it, and
must be a link of the peer's VRF. A name that reads as an address, such as `fe80::1`, is refused
in both, because FRR could not tell the interface from a neighbour's address. Two peers that name one session are refused too.

## The check parses the config, and the renderer checks the rest

A delivery renders `frr.conf` and checks it with `vtysh --dryrun` before it changes anything.
vtysh parses each line with the daemons' commands and needs no daemon. It catches a command that
no daemon takes, and a value outside a command's range, such as `le 129`. It does not check what
the daemons would refuse when they apply it, such as a route-map that names a missing list.

At boot, FRR's mgmtd applies the prefix lists, route-maps and static routes of the config in one
transaction, so one value it refuses would leave all of them out. The schemas, the rules and the
renderer therefore refuse the values FRR's daemons refuse. The renderer checks again what the
schemas and rules check, should a document bypass them, and more:

- a `ge` or `le` outside the prefix's length and the family's longest prefix, or a `ge` above `le`;
- two entries of a PrefixList with one prefix, `ge` and `le`, one community twice in a
  CommunityList and two networks of one range;
- a community with a part above 65535 in a standard community, or above 4294967295 in a large
  one;
- a policy whose PrefixLists are of both families, and a peer's or network's policy of another
  family;
- an `asPath` that bgpd cannot compile;
- a description with an `@`, two spaces in a row or more than 80 characters.

bgpd compiles an `asPath` with glibc's `regcomp` as a POSIX extended regular expression, each `_`
standing for the start, the end or a separator of the path. bgpd drops an as-path list it cannot
compile, and its term then matches no route. The renderer checks each expression as glibc reads
it. It refuses a repetition of nothing, such as `^*`, parentheses that do not pair up and an
interval that ends below its start. The schema leaves out brackets, whose meaning differs between
POSIX and other regular expressions.

## Passwords reach FRR as credentials

The renderer sees no `Secret`. A peer's password renders as the placeholder
`@secret_<secret>.<key>@` on its peer group's `password` line. `frr.service` gets the passwords
that the `BGPPeer` documents name as its credentials. Before it starts and on each reload, a
helper of the unit writes `/run/frr/frr.conf` with each password line's placeholder replaced by
its credential. Only root reads the file, mode 0600, and `/etc/frr/frr.conf` links to it.

The helper fills in the password lines alone. A placeholder elsewhere stays as written, and a
description cannot hold one, as it holds no `@`. A password that is empty, longer than 80
characters, or holds white space, a line break or anything but printable ASCII fails the unit and
leaves the file as it was. So does a placeholder whose credential the unit lacks.

A rotated password changes no rendered file. The effect of `frr` fires all the same, because the
value of a `Secret` that its documents reference changed. The reload loads the credentials again
before the helper runs.

## A delivery reloads FRR

The effect of `frr` reloads `frr.service` once the delivery is stored. The reload fills in the
passwords again and runs `frr-reload.py --reload`. That compares the new config with the one FRR
runs and applies the difference, so a session that the change leaves alone stays up. A reload
that fails fails the delivery, with the config stored, and FRR keeps running. A failed
`frr.service` is restarted instead.

chalkd reloads the units of one delivery in one `systemctl reload`. `frr.service` orders after
`chalkos-firewall.service`, so the firewall accepts a new peer's ports before FRR dials it.

## FRR starts after the config and the firewall

At boot, `frr.service` starts after `chalkos-config.service` has rendered the outputs and after
`chalkos-firewall.service`. It orders before `chalkos-node-addresses.target` and is wanted by it,
as a unit that announces the node's addresses. Without a rendered `frr.conf`, the helper writes
the image's fallback config, which runs no BGP.

`frr.service` starts only when chalkd has written its credentials, which it does once the node
has config. In maintenance mode, which loads no config, the node runs no FRR.

networkd leaves zebra's routes and next hops alone. By default it removes the routes it did not
configure itself each time it configures a link, so router images turn `ManageForeignRoutes=` and
`ManageForeignNextHops=` off.

## The firewall accepts each peer's ports

The input chain of `inet chalkos` accepts BGP, TCP port 179, from each `BGPPeer`, and BFD from
each with `bfd`. A peer of `interface` or `interfaces` is accepted on those interfaces, globs as
written, and a peer of `address` from that address alone. BFD takes UDP ports 3784 and 3785 of
single-hop sessions, and 4784 of multi-hop ones from a peer of `address`. A peer on a link is one
hop away. The edge renders:

```text
iifname { "eth1", "eth2" } tcp dport { 179 } accept comment "BGPPeer fabric"
iifname { "eth1", "eth2" } udp dport { 3784, 3785 } accept comment "BGPPeer fabric"
ip saddr { 203.0.113.254/32 } tcp dport { 179 } accept comment "BGPPeer transit"
```

These rules come after chalkd's port and before the policies to `local`, which cannot close them.
[The router's firewall](firewall.md#the-input-chain) describes the chain. A peer of a VRF is
accepted on that VRF's links by `meta sdifname`, as
[VRFs on a router](vrfs.md#the-firewall-accepts-a-vrf-peers-ports-on-its-links) explains.

## chalkd reads FRR's state

`chalkctl status` shows a router's BGP sessions. chalkd asks vtysh for
`show bgp vrf all neighbors json`, within 5 seconds, and reports each session's VRF, peer,
neighbour, state, uptime, routes received and sent, and BFD state. It finds a session's `BGPPeer`
by the name of its peer group. When vtysh fails, the status gives its error, and the status's
other parts stand.

The `Routes` call lists the kernel's routes, which chalkd reads through netlink, and the paths BGP
selected, from `show bgp ipv4 unicast detail json` and its IPv6 twin. For another VRF it lists that
VRF's table and asks for `show bgp vrf <name> ipv4 unicast detail json`. A route of several next
hops lists each with its weight, and an IPv4 route over an IPv6 next hop names it.
`chalkctl node routes` prints both. A node of another image answers that the router layer is not
in its image.

The diagnostics `bgp-summary`, `bgp-neighbor`, `bgp-route` and `bfd-peers` run vtysh as a member
of `frrvty`, the group of FRR's sockets, and need a reader.

## Limits

- FRR announces a network of `BGP.spec.networks` only while the router's table holds a route to
  it, whatever the policy says.
- zebra prefers a route the kernel holds from elsewhere over BGP's path to the same prefix, and
  then installs none of BGP's. A default route that networkd takes from router advertisements on
  an uplink keeps BGP's default route out of the kernel. `ipv6AcceptRA: false` on that `Network`
  lets it in.
- FRR negotiates the extended next hop on sessions of `interface` and `interfaces` whatever
  `extendedNextHop` says.
- The rules do not compare a network's policy with the network's family. The renderer refuses a
  network whose policy matches PrefixLists of another family.
- A peer's `interfaces` match the names of `Network` documents, not the links that exist, and a
  `Network` that matches links by anything but `Name=` names none.
- The AS number rule compares a `BGP` document with the running one of the same name. A `BGP`
  document of another name replaces the old one, AS number included.

## Related pages

- [Route with BGP](../../router/bgp.md) configures BGP.
- [How a router works](overview.md): the layer's outputs and the boot order.
- [VRFs on a router](vrfs.md): an instance per VRF and imported routes.
- [Config kinds](../../reference/kinds.md#router-chalkos-dev) for every field of the BGP kinds.
- [Built-ins](../../reference/builtins.md#outputs-router-images) for the output, its effect, the
  rules and the diagnostics.
