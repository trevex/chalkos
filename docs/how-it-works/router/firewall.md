---
title: "The router's firewall"
description: "The nftables table of a router image: the protected rules, chalkd's port, zones and policies, NAT, the reverse-path filter and the fallback ruleset"
---

# The router's firewall

A router image filters with one nftables table, `inet chalkos`, which the output
`router-nftables` renders from the node's router documents. NixOS's firewall and its nftables
service are off on these images, so no other table of chalkos filters the router's traffic. This
page describes the table's chains in the order a packet meets them.

## One table holds every rule

nftables runs every base chain of a hook, and a packet that one chain drops stays dropped
whatever the others accept. A second table could therefore not protect anything from the first.
The renderer writes one table instead, and the rules chalkos protects come first in each of its
base chains. No document can change them.

The file starts with `table inet chalkos` and `delete table inet chalkos`. Loading it replaces the
table in the same transaction that defines the new one.

A NixOS module of the image that opens a port through `networking.firewall`, such as
`allowedTCPPorts` or `extraInputRules`, reaches no rule. The image's build warns of each such
option and names it.

## The input chain

The input chain sees the connections to the router itself. Its policy drops what no rule accepts.
It holds these rules, in order:

| Rules | Accept |
| --- | --- |
| Protected | The loopback interface; established and related connections, after conntrack's invalid packets are dropped; ICMPv6 errors, echo requests, neighbour discovery, router solicitations and advertisements and multicast listener discovery; ICMP errors and echo requests; DHCPv6 replies from the server port to the client port of a link-local address. |
| chalkd | TCP port 50000 on the interfaces of `Router.spec.management.interfaces`, or on every interface while it is null. |
| Services | The ports of the router's own services, from the interfaces and sources their documents name: BGP's TCP port 179 of each `BGPPeer` and, for each with `bfd`, BFD's UDP ports 3784 and 3785, from its interfaces or its address, and 4784 from its address; DNS's UDP and TCP port 53 from the `Resolver`'s `allow` to its `listen`. |
| Policies to `local` | A jump to the chain of each `FirewallPolicy` whose `to` is `local`, for the interfaces of its `from` zone. |

[BGP on a router](bgp.md#the-firewall-accepts-each-peers-ports) shows the rules of the `BGPPeer`
documents, and
[NAT64 and DNS64 on a router](nat64-and-dns.md#the-firewall-accepts-dns-from-the-resolvers-clients)
those of the `Resolver`. A policy to `local` may accept more. It cannot drop what the earlier rules
accepted.
The input chain of a LAN router whose node API answers on `lan0` reads:

```text
chain input {
	type filter hook input priority filter; policy drop;
	iifname "lo" accept
	ct state established,related accept
	ct state invalid drop
	icmpv6 type { destination-unreachable, packet-too-big, time-exceeded, parameter-problem, echo-request, mld-listener-query, mld-listener-report, mld-listener-done, nd-router-solicit, nd-router-advert, nd-neighbor-solicit, nd-neighbor-advert, mld2-listener-report } accept
	icmp type { destination-unreachable, time-exceeded, parameter-problem, echo-request } accept
	ip6 daddr fe80::/64 udp sport 547 udp dport 546 accept
	iifname { "lan0" } tcp dport 50000 accept
	iifname { "lan0", "vlan*" } jump policy-lan-local
	iifname { "wan0" } jump policy-wan-local
}
```

## The forward chain

The forward chain sees what the router routes from one interface to another.

- While no `FirewallPolicy` goes between two zones, the chain holds no rule and accepts
  everything. A pure routing role needs no firewall documents. conntrack drops nothing for it,
  so a flow whose replies take another router passes too.
- Once a policy goes between two zones, the chain's policy drops. It accepts established and
  related connections, drops conntrack's invalid packets, and accepts ICMP and ICMPv6 errors. It
  accepts a connection that a `dnat` rule translated, then hands each connection to the policy of
  its pair of zones. A connection between zones without a policy is dropped, and so is one from or
  to an interface in no zone.

From then on, traffic between two interfaces of one zone, such as `lan0` and `vlan10` of the zone
`lan`, is dropped too. A policy from the zone to itself, `from: lan` and `to: lan`, lets it
through.

## Zones and policies

A `FirewallZone` names interfaces, by name or by a glob, a prefix followed by `*`, the one
wildcard nftables matches. The zone `local` is the router itself and has no document. A rule
refuses an interface or glob that two zones name as written. It does not compare globs with
names, so `eth*` in one zone and `eth0` in another pass.

A `FirewallPolicy` goes from a zone to another zone, to the same zone, or to `local`. Its rules
match in order, and the first that matches decides. A connection no rule matches gets the policy's
`default`, which is `drop` unless the policy says otherwise. Each policy becomes a chain
`policy-<name>`, which ends with that default:

```text
chain policy-wan-local {
	ip6 saddr @addresses-trusted tcp dport { 8080 } accept
	drop
}
```

The input and forward chains jump to the policies' chains in the order of the policies' names.
Each chain ends with a verdict, so the first jump whose interfaces match decides. Where the globs
of two zones overlap, the policy of the first name takes the interface's connections.

A rule matches a family when its `family`, its protocol and its addresses leave it: `icmp` is
IPv4's, `icmpv6` IPv6's, and a list of ranges matches the ranges of each family in that family. A
rule whose addresses hold both families renders one line for each. `reject` answers with an
administratively prohibited error of the packet's family. A rule with `log` logs each packet it
matches, prefixed with the policy's name and the rule's number.

An `AddressSet` holds ranges of one family as a named nftables set, `addresses-<name>`. A rule
names it as `set:<name>`, alone in its `source` or `destination`.

## NAT rules

`NATRule` documents apply whether or not any policy exists. `dnat` rules sit in a chain on the
prerouting hook, and `masquerade` and `snat` rules in one on the postrouting hook, in the order of
their names. Each rule translates one family, IPv4 unless its `family` says otherwise:

```text
chain srcnat {
	type nat hook postrouting priority srcnat; policy accept;
	meta nfproto ipv4 oifname { "wan0" } ip saddr { 192.0.2.0/24 } masquerade
}
```

A `dnat` rule opens its own forwarding: the forward chain accepts the connections it translated,
whatever the policies say.

NAT rules never see the connections a NAT64 translates, nor do the input and forward chains:
Jool takes their packets at the prerouting hook and sends the translated ones itself.
[NAT64 and DNS64 on a router](nat64-and-dns.md#translated-connections-bypass-the-firewalls-nat)
describes them.

## The reverse-path filter

`Router.spec.rpFilter` adds a chain on the prerouting hook. It drops a packet whose source the
router has no route back to: `loose` takes a route through any interface, and `strict` only one
through the interface the packet came in on. `off` renders no chain. DHCPv4 between ports 67 and 68
passes, as with NixOS's own filter on other images. The kernel's route lookup passes ICMPv6 from
the unspecified address `::`, so duplicate address detection and multicast listener reports pass
as well. The chain covers both families, so the kernel's IPv4 filter stays off.

## The fallback ruleset

`chalkos-firewall.service` loads a fallback ruleset when the node has no rendered one: in
maintenance mode, before the first install, and when the node's config did not render at boot.
The fallback is the ruleset of a node without router documents, with a forward chain that drops
what the protected rules do not accept. Its node API answers on every interface, so an operator
can reach the node to deliver config.

The unit also loads the fallback when nft refuses the rendered ruleset at boot, and then fails.
The node keeps the fallback until a delivery changes the ruleset: chalkd then restarts the failed
unit, which loads the new one.

## Limits

- A rule names one `AddressSet` for its source and one for its destination, with no ranges
  beside it.
- Zones compare interfaces and globs as written, so globs that overlap across zones are not
  refused. The policy of the first name then decides.
- With policies between zones, conntrack drops the packets it marks invalid, such as the middle of
  a TCP connection whose start took another router.
- The router's own outgoing traffic is accepted. A policy cannot come from `local`.
- The policies do not filter the connections a NAT64 translates.

## Related pages

- [How a router works](overview.md): the outputs, the boot order and the management guard.
- [Filter and translate traffic](../../router/firewall-and-nat.md) for the documents.
- [Ports and firewall](../../reference/ports-and-firewall.md#router-images).
