---
title: "Policy routing on a router"
description: "How a router image routes by policy: the firewall's marks, the chain after the reverse-path filter, networkd's rules at priority 900 and the next hops' tables that fail closed"
---

# Policy routing on a router

A router image routes by policy in two halves. The firewall marks the packets that a
`PolicyRoute` rule matches, and a rule of the kernel per mark picks the table the packet's route
comes from. networkd keeps the kernel's rules and the next hops' tables. For the example of
[Route traffic by policy](../../router/policy-routing.md), the output `router-nftables` renders this
chain:

```nft
chain policy-routing {
	type filter hook prerouting priority mangle + 20; policy accept;
	meta mark & 0x00ff0000 != 0 return
	fib daddr type local return
	iifname { "lan0", "lan1" } ip daddr { 192.0.2.0/24 } counter meta mark set meta mark & 0xff00ffff | 0x00040000 return comment "lan 1"
	iifname { "lan0", "lan1" } ip6 daddr { 2001:db8:1::/64, 2001:db8:2::/64 } counter meta mark set meta mark & 0xff00ffff | 0x00040000 return comment "lan 1"
	iifname { "lan0", "lan1" } ip saddr @addresses-guests tcp dport { 443 } counter meta mark set meta mark & 0xff00ffff | 0x00010000 return comment "lan 2"
	iifname { "lan0", "lan1" } ip dscp { 46 } counter meta mark set meta mark & 0xff00ffff | 0x00010000 return comment "lan 3"
	iifname { "lan0", "lan1" } ip6 dscp { 46 } counter meta mark set meta mark & 0xff00ffff | 0x00010000 return comment "lan 3"
	iifname { "lan0" } ip saddr { 192.0.2.32/27 } counter meta mark set meta mark & 0xff00ffff | 0x00020000 return comment "servers 1"
	iifname { "lan0" } ip6 saddr { 2001:db8:1::20/123 } counter meta mark set meta mark & 0xff00ffff | 0x00030000 return comment "servers 2"
}
```

The documents apply in the order of their names, and each document's rules in order. A rule
writes a line per family it matches, with the DSCP values in front, as nftables matches DSCP with
`ip dscp` and `ip6 dscp`. The first line that matches marks the packet and ends the chain. Without
`PolicyRoute` documents the ruleset has no such chain.

## A mark per target table

A rule's target is a table: a VRF target is its VRF's table, a `table` target its table and a
next hop its own table. The renderer sorts the distinct tables by number and gives the table at
index `i` the mark `(i+1) << 16`. The example's tables 100, 200, 201 and 254 take the marks
`0x10000`, `0x20000`, `0x30000` and `0x40000`. The marks use the bits `0x00ff0000`, so the
documents can send traffic to 255 tables at most, and the renderer refuses more.

The statement `meta mark set meta mark & 0xff00ffff | 0x00010000` sets these bits and keeps every
other bit of the mark. `nft list ruleset` prints it as `meta mark & 0xff01ffff | 0x00010000`,
which sets the same bits. Kubernetes uses single bits outside `0x00ff0000`, and an image carries
the router layer or the Kubernetes layer, never both.

## The chain follows the reverse-path filter

The chain hooks prerouting at `mangle + 20`, after the reverse-path filter at `mangle + 10`. The
filter judges a packet by the table of the link it came in on, and its lookup
`fib saddr . mark . iif` takes the mark into account. A mark set first would make the filter look
the source up in the policy's table and drop the packet. The routing decision follows every
prerouting chain, so the mark still picks the table.

The chain also runs before the firewall's DNAT rules, which hook prerouting at `dstnat`. A rule's
`destination` therefore matches the address before DNAT translates it.

A packet from a VRF's link passes prerouting twice. The first pass sees the link's own name, and
the second the VRF's device, after DNAT. `in` matches on the first pass, where the chain sets the
mark. `meta mark & 0x00ff0000 != 0 return` then keeps that mark on the second pass, so the first
rule that matched still decides. A packet that no rule marked on its first pass meets the rules
again on the second, where only a document without `in` matches it, by its translated address.
`meta sdifname` does not exist in prerouting, which is why `in` names the links themselves.

With `PolicyRoute` documents and VRFs, the filter accepts a marked packet on a VRF device's second
pass before its fib rule. The first pass judged the packet before it had a mark:

```nft
chain prerouting {
	type filter hook prerouting priority mangle + 10; policy drop;
	iifname { "transit" } meta mark & 0x00ff0000 != 0 accept
	meta nfproto ipv4 udp sport . udp dport { 67 . 68, 68 . 67 } accept
	fib saddr . mark oif exists accept
}
```

## The router's own addresses take no policy

The chain's second line, `fib daddr type local return`, ends the chain for a packet to an
address of the router before any rule can mark it. Without `iif`, nftables looks the destination
up in the table of the VRF that the packet's link belongs to: the local table for the default
VRF, the VRF's table for a VRF's link and for the VRF's device. The line therefore matches on
both passes of a VRF's packet, and it matches an address on any link of the VRF or on its
device. With `iif`, nftables would call an address local only when it is on the very link the
packet came in on. That excludes the VRF device's pass and the addresses on the VRF's device.

The router's own traffic in a VRF, such as BGP, BFD and the VRF's resolver, therefore stays in
the VRF however broadly a document matches. Without the line, a document without `in` and with
`source: [0.0.0.0/0]`, or with `dscp: [48]`, the class FRR's BGP sends with, would mark the
packets of a VRF's BGP sessions. The mark's rule at priority 900 would then route them by the
target table, away from the router. An address of another VRF is not local in the packet's VRF,
so a policy may still send traffic to it.

## The kernel's rules at priority 900

The kernel looks a packet's route up through its rules in order of priority. On the example's
router `ip -6 rule show` prints:

```text
0:	from all lookup local
900:	from all fwmark 0x10000/0xff0000 lookup 100 proto static
900:	from all fwmark 0x20000/0xff0000 lookup 200 proto static
900:	from all fwmark 0x30000/0xff0000 lookup 201 proto static
900:	from all fwmark 0x40000/0xff0000 lookup main proto static
1000:	from all lookup [l3mdev-table]
32766:	from all lookup main
```

Each target table has a rule that matches its mark within `0xff0000`. Priority 900 comes before
the VRFs' rule at 1000, so a policy sends a VRF link's traffic out of the VRF, and the default
VRF's traffic into one. The local table's rule at priority 0 still comes first, but the local
table holds only the default VRF's addresses. The kernel puts the local routes of a VRF's links
and device into the VRF's table, which only the rule at 1000 reaches. A mark would send a packet
to such an address past that rule, so the chain leaves these packets unmarked, as
[The router's own addresses take no policy](#the-routers-own-addresses-take-no-policy) describes.

## networkd keeps the rules and the next hops' tables

On a router image the output `networkd` reads the `PolicyRoute` documents, and
`chalkos-render router-networkd` writes the kernel's rules and the next hops' tables into
networkd's units. The rules and the unreachable defaults of each next hop's table go into the
`Network` of `lo`. Without a `Network` of the config that matches `lo` by `Name=lo` alone, the
renderer writes `00-policy-routing.network`:

```ini
[Match]
Name=lo

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

...

[RoutingPolicyRule]
Family=both
FirewallMark=0x10000/0xff0000
Priority=900
Table=100

...
```

networkd removes a link's routes and rules when the link goes down or loses its carrier, and
`lo` never goes down. The rules and the unreachable defaults therefore stay while a next hop's
link is down.

The route through a next hop goes into the `Network` of the next hop's link. The renderer adds
these sections to `21-wanb.network`:

```ini
[Route]
Gateway=198.51.100.130
Table=200

[Route]
Gateway=2001:db8:b::2
Table=201
```

The kernel removes the route when `wanb0` goes down and does not add it back. networkd adds it
again once the link comes up. The `Network` must match the next hop's link alone by its `Name=`.
networkd leaves a link `configuring` while its `Network` has a gateway that the link's addresses
do not reach, so a `Network` of several links would hold back those that do not reach the next
hop.

A `Network` of the config that matches `lo` by `Name=lo` alone takes the sections in place of
`00-policy-routing`. The renderer refuses a `Network` that may match `lo` by other `[Match]` keys,
and one that matches `lo` by a glob together with other links, such as `Name=*`. networkd applies
the first `Network` that matches a link, so such a `Network` would hide the rules from `lo` or
carry them to every link it matches. A `Network` of `Name=lo` alone, sorted before it, fixes the
refusal.

chalkos runs no unit of its own for the rules. networkd removes the rules it does not know
whenever it configures a link that has rules of its own, and no event tells a unit that a link
came up, so the unit could not add a next hop's route again.

## A missing next hop drops the traffic

A next hop's table holds the default route through the next hop and, after it, an unreachable
default of metric 4278198272, as each VRF's table ends. While the next hop's link is down, the
lookup ends at the unreachable default, and the kernel drops the packet instead of looking on in
the main table. A `table` target gets no unreachable default, so a destination its table has no
route to takes the route it would take without the policy.

## A delivery reloads the firewall and networkd

The outputs `router-nftables` and `networkd` both read the `PolicyRoute` documents. A delivery
that changes them reloads `chalkos-firewall.service` and `systemd-networkd.service`, in no set
order. A new target's traffic takes the route it had before until both have reloaded. A delivery
that adds or removes a target table can renumber the marks. Until both reloads are done, a
packet may take another target's table or the main table.

## Limits

- `rpFilter: strict` drops the replies that come back through a next hop's link, unless the main
  table routes their source through that link too.
- The replies to traffic sent into a VRF need a route back in the VRF's table.
- During a delivery that changes the targets, a packet may take another target's table or the
  main table for a moment.
- The router's own traffic takes no policy, and the router marks no connections.
- A rule's `destination` matches the address before DNAT, except on a VRF's second pass.
- A next hop's gateway that the addresses of its link do not reach leaves the link `configuring`
  in networkd, and the next hop's traffic is dropped.

## Related pages

- [Route traffic by policy](../../router/policy-routing.md) for the documents.
- [The router's firewall](firewall.md): the table, its chains and the reverse-path filter.
- [VRFs on a router](vrfs.md): the VRFs' tables and their unreachable defaults.
- [Ports and firewall](../../reference/ports-and-firewall.md#router-images).
