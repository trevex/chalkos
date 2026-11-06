---
title: "Route traffic by policy"
description: "Send a router's forwarded traffic by source, port or DSCP into a VRF, another table or through a next hop, whatever its destination's route"
---

# Route traffic by policy

A `PolicyRoute` document sends the traffic its rules match to a VRF's table, another table or a
next hop, which a `NextHop` document declares. The router then routes that traffic by the
target's table, whatever route its destination has in the table of the link it came in on. This
page sends a LAN's traffic into a VRF, through a second uplink and back to the main table, and
checks the result.
[Policy routing on a router](../how-it-works/router/policy-routing.md) explains how chalkos renders
it.

## Before you begin

You need a router set up as [Set up a router](set-up.md) describes, and the secrets file. The
examples configure a LAN router `r1` with these links:

- `lan0` and `lan1`, the LAN, in the `FirewallZone` `lan`.
- `wana0`, the uplink the main table routes through.
- `wanb0`, a second uplink to another router.
- `vt0`, in the VRF `transit` of table 100.

Each link has a `Network` document. A VRF target needs its `VRF` document, as
[Separate routing with VRFs](vrfs.md) describes. A next hop needs its `NextHop` document and a
`Network` that names the next hop's link alone in `[Match] Name=`. Keep the default reverse-path
filter, `rpFilter: loose`, when traffic leaves through a next hop; [Limits](#limits) explains why.

## Send a LAN's traffic into a VRF

The `PolicyRoute` `lan` sends guests' HTTPS and every packet of DSCP 46 from the zone `lan` into
the VRF `transit`:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: PolicyRoute
metadata: {name: lan}
spec:
  in: {zone: lan}
  rules:
    - match: {destination: {cidrs: [192.0.2.0/24, "2001:db8:1::/64", "2001:db8:2::/64"]}}
      to: {table: 254}
    - match: {source: {set: guests}, protocol: tcp, destinationPorts: [443]}
      to: {vrf: transit}
    - match: {dscp: [46]}
      to: {vrf: transit}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: AddressSet
metadata: {name: guests}
spec: {family: ipv4, prefixes: [192.0.2.64/26]}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: VRF
metadata: {name: transit}
spec: {table: 100, interfaces: [vt0]}
```

[Send traffic to another table](#send-traffic-to-another-table) explains the first rule. The second
sends TCP to port 443 from the `AddressSet` `guests` into `transit`, and the third sends DSCP 46
of both families there.

The replies come back on `vt0`, a link of `transit`, so the router routes them by table 100. That
table needs a route back to the LAN. Give it one in the LAN's `Network` documents:

```yaml
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-lan}
spec:
  match: {name: lan0}
  network: {address: [192.0.2.1/25, "2001:db8:1::1/64"], ipv6AcceptRA: false}
  route:
    - {destination: 192.0.2.0/25, table: 100}
    - {destination: "2001:db8:1::/64", table: 100}
```

A BGP instance of `transit` that imports the default VRF's routes does the same; see
[Import routes from another VRF](vrfs.md#import-routes-from-another-vrf).

## Send traffic through another router

The `PolicyRoute` `servers` sends the servers' traffic through the router at the far end of
`wanb0`. The `NextHop` documents `wanb4` and `wanb6` declare that router, one per family, and
the rules name them in `to.nextHop`:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: PolicyRoute
metadata: {name: servers}
spec:
  in: {interfaces: [lan0]}
  rules:
    - match: {source: {cidrs: [192.0.2.32/27]}}
      to: {nextHop: wanb4}
    - match: {source: {cidrs: ["2001:db8:1::20/123"]}}
      to: {nextHop: wanb6}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: NextHop
metadata: {name: wanb4}
spec: {address: 198.51.100.130, interface: wanb0, table: 200}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: NextHop
metadata: {name: wanb6}
spec: {address: "2001:db8:b::2", interface: wanb0, table: 201}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 21-wanb}
spec:
  match: {name: wanb0}
  network: {address: [198.51.100.129/25, "2001:db8:b::1/64"], ipv6AcceptRA: false}
```

A `NextHop` has an address, its link and a table of its own. The address is a unicast address of
the router at the far end, link-local ones included. The rules refuse an unspecified, loopback,
multicast or broadcast address, and an IPv4-mapped IPv6 address such as `::ffff:198.51.100.130`,
which stands for an IPv4 router and is no IPv6 neighbour on a link. The link is not `lo`. The
table is none of the kernel's tables 253, 254 and 255 and no VRF's table, and no other `NextHop`
and no rule's `table` target takes it. `chalkctl validate` checks all of this.

`21-wanb` has no gateway: the main table routes through `wana0` alone. The router fills each
`NextHop`'s table with a default route through the next hop, followed by an unreachable default
route. While `wanb0` is down, the servers' traffic hits the unreachable default and is dropped
instead of falling back to the main table. networkd adds the route through the next hop again
once the link comes up. A `NextHop` that no rule names gets its table and routes as well, but no
packet is sent to that table.

A rule with a next hop matches packets of the next hop's family alone. The first rule above
matches IPv4 and the second IPv6, so each family of the servers goes through its own next hop.
The rules refuse a rule whose `family` or addresses leave out the next hop's family.

### Share a next hop

Rules in one document or in several can name the same `NextHop`. They share its table, its route
and its mark. The `PolicyRoute` `video` sends IPv4 of DSCP 34 from `lan1` through `wanb4` too:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: PolicyRoute
metadata: {name: video}
spec:
  in: {interfaces: [lan1]}
  rules:
    - match: {family: ipv4, dscp: [34]}
      to: {nextHop: wanb4}
```

The router still keeps one table 200 with one route through `198.51.100.130`, and one rule of the
kernel that sends the mark of table 200 there. To move both policies to another router, change
the address of `wanb4` alone.

## Send traffic to another table

`to: {table: <number>}` sends the traffic to a table you fill yourself, with `[Route] Table=` in a
`Network` or with FRR. Table 254 is the main table. The first rule of `lan` sends the traffic
to the LAN's prefixes there, so the traffic between `lan0` and `lan1` stays in the main table.
Without it, the rules of `servers` would send a server's traffic to `lan1` through `wanb0`, as a
next hop's table holds only a default route.

The router adds no unreachable default to a table that a `table` target names. A destination
that the table has no route to takes the route it would take without the policy.

## Match traffic

A rule's `match` takes the terms of a `FirewallPolicy` rule:

- `family`: `ipv4` or `ipv6`.
- `protocol`: such as `tcp`, `udp` or `sctp`.
- `sourcePorts` and `destinationPorts`: numbers or ranges `"<first>-<last>"`, with `protocol`
  `tcp`, `udp` or `sctp`.
- `source` and `destination`: an `AddressSet` in `set`, or prefixes in `cidrs`, one of the two.

It adds `dscp`, a list of values from 0 to 63. 46 is EF, expedited forwarding. A match needs at
least one term. To send every packet, say so, such as with `source: {cidrs: [0.0.0.0/0, "::/0"]}`.

`in` says where the traffic comes in: `zone` names a `FirewallZone` and matches its links, and
`interfaces` names links or globs such as `vlan*`. A document without `in` matches traffic from
every link.

The first rule that matches a packet decides. The router applies the documents in the order of
their names, and each document's rules in order. `lan` comes before `servers`, so a server's
packet of DSCP 46 takes `transit`, not `wanb0`.

## Check that it worked

Deliver the documents as any config:

```sh
chalkctl validate
chalkctl apply-config r1 --secrets secrets.age
```

The diagnostic `ip-rules` lists the kernel's rules of one family. Each target table has a rule of
priority 900 that matches its mark:

```sh
chalkctl node diag r1 --secrets secrets.age ip-rules family=inet6
```

The diagnostic prints the rules as one JSON array on a single line, shown here a rule per line:

```json
[{"priority":0,"src":"all","table":"local"},
{"priority":900,"src":"all","fwmark":"0x10000","fwmask":"0xff0000","table":"100","protocol":"static"},
{"priority":900,"src":"all","fwmark":"0x20000","fwmask":"0xff0000","table":"200","protocol":"static"},
{"priority":900,"src":"all","fwmark":"0x30000","fwmask":"0xff0000","table":"201","protocol":"static"},
{"priority":900,"src":"all","fwmark":"0x40000","fwmask":"0xff0000","table":"main","protocol":"static"},
{"priority":1000,"src":"all","l3mdev":null},
{"priority":32766,"src":"all","table":"main"}]
```

The diagnostic `routes` lists a next hop's table, here with the route through the next hop and
the unreachable default:

```sh
chalkctl node diag r1 --secrets secrets.age routes family=inet6 table=201
```

```json
[{"dst":"default","gateway":"2001:db8:b::2","dev":"wanb0","protocol":"static","metric":1024,"flags":[],"pref":"medium"},
{"type":"unreachable","dst":"default","dev":"lo","protocol":"static","metric":4278198272,"flags":[],"pref":"medium"}]
```

The firewall's chain `policy-routing` counts the packets each rule marked. Its comments name the
document and the rule's number:

```sh
chalkctl node diag r1 --secrets secrets.age nft-ruleset
```

```text
...
		iifname { "lan0", "lan1" } ip dscp ef counter packets 10 bytes 536 meta mark set meta mark & 0xff01ffff | 0x00010000 return comment "lan 3"
		iifname "lan0" ip saddr 192.0.2.32/27 counter packets 11 bytes 596 meta mark set meta mark & 0xff02ffff | 0x00020000 return comment "servers 1"
...
```

nft prints DSCP values by name, such as `ef` for 46, and the mark's bits as `0xff01ffff`, the
same as the rendered `0xff00ffff`. A rule that counts no packets has matched nothing yet.

## Fix a refused delivery

chalkctl refuses documents that do not fit together before it sends anything: a field that names
a document the config lacks, a value that two documents must not share, and what the rules of the
router kinds refuse. [The built-ins' reference](../reference/builtins.md#rules-policyroute) lists
each `PolicyRoute` and [`NextHop`](../reference/builtins.md#rules-nexthop) rule with its
expression:

| Message | Fix |
| --- | --- |
| `PolicyRoute lan: in names a zone or interfaces, one of them; leave in out for traffic from every link` | Keep `zone` or `interfaces`, or drop `in`. |
| `PolicyRoute/lan: spec.in.zone: FirewallZone "dmz" does not exist` | Add the zone, or name one that exists. |
| `PolicyRoute/lan: spec.rules: a rule has an empty match, which would take every packet; to send every packet, say so, such as with source: {cidrs: [0.0.0.0/0]}` | Add a term to the match. |
| `PolicyRoute/lan: spec.rules: a rule's source or destination names both set and cidrs, or neither; give one, or leave the field out to match every address` | Keep one of `set` and `cidrs`, or leave out an empty `source` or `destination`. |
| `PolicyRoute/lan: spec.rules[1].match.source.set: AddressSet "guests" does not exist` | Add the `AddressSet` document, or fix the name. |
| `PolicyRoute/lan: spec.rules: a rule has addresses or a set of another family than its own` | Drop the other family's addresses, or change the rule's `family`. |
| `PolicyRoute/lan: spec.rules: a rule matches no packet: its family, protocol, addresses and sets leave neither IPv4 nor IPv6` | Make the rule's family, protocol, addresses and sets agree on one family. |
| `PolicyRoute/lan: spec.rules: a rule has ports without the protocol tcp, udp or sctp` | Set `protocol` to `tcp`, `udp` or `sctp`. |
| `PolicyRoute/lan: spec.rules: a rule's to names a VRF, a table or a next hop, one of them` | Keep one target in `to`. |
| `PolicyRoute/servers: spec.rules: a rule's next hop is of another family than the packets the rule matches` | Match the `NextHop`'s family, or name a `NextHop` of the rule's family. |
| `PolicyRoute/lan: spec.rules[1].to.vrf: VRF "transit" does not exist` | Add the `VRF` document, or fix the name. |
| `PolicyRoute/servers: spec.rules[0].to.nextHop: NextHop "wanb4" does not exist` | Add the `NextHop` document, or fix the name. |
| `PolicyRoute/lan: spec.rules: a rule's table is 255, the kernel's local table, or a VRF's table; name the VRF in vrf, or take another table` | Name the VRF in `vrf`, or take another table. |
| `PolicyRoute/lan: spec.rules: a rule's table 200 is the table of NextHop wanb4; name the NextHop in nextHop, or take another table` | Name the `NextHop` in `nextHop`, or take another table. |
| `NextHop/wanb4, NextHop/wanb6: spec.table 200 is not unique; give each NextHop a table of its own` | Give each `NextHop` its own table. |
| `NextHop/wanb4: spec.table: the table is 253, 254 or 255, the kernel's own; give the NextHop a table of its own` | Take a table that is not the kernel's. |
| `NextHop/wanb4: spec.table: the table is that of VRF transit; give the NextHop a table of its own` | Take a table that no VRF uses. |
| `NextHop/wanb4: spec.interface: the interface is lo, which no next hop is on; name the link the next hop is on` | Name the link the next hop is on in `interface`. |
| `NextHop/wanb6: spec.address: the next hop ::ffff:198.51.100.130 is no unicast address of a router, or is an IPv4-mapped IPv6 address; give the router's address, an IPv4 one as IPv4` | Give the next hop's unicast address, an IPv4 address as IPv4. |

The [validator `router`](../reference/builtins.md#validator-router) checks `in` against the
VRFs' devices, the NAT64s' links and the links of the zones. `chalkctl validate` and
`chalkctl apply-config` run it before chalkctl sends anything, and the router runs it again at
the delivery. It reports every fault it finds at once, and `chalkctl` prints its refusals after
`the config is refused:`, as it prints a rule's, with the document and the field each is about:

| Message | Fix |
| --- | --- |
| `PolicyRoute/lan: spec.in.interfaces[0]: tran* matches transit, the device of VRF transit; name the VRF's links or a zone of the VRF` | Name the VRF's links in `interfaces`, or a zone that holds them. The validator refuses the VRF's own name, `transit`, the same way. |
| `PolicyRoute/lan: spec.in.interfaces[0]: n64-* matches n64-edge, the link of NAT64 edge, whose traffic Jool translates; name other links` | Narrow the glob, so it leaves out the NAT64's link. |
| `PolicyRoute/lan: spec.in.zone: the zone guest holds n*, which matches n64-guest, the link of NAT64 guest, whose traffic Jool translates; name the zone's other links in interfaces` | Name the zone's other links in `interfaces`, or narrow the `Network` that puts `n*` in the zone's VRF. |
| `PolicyRoute/lan: spec.in.zone: the zone core holds no link` | Give the zone a link, or name another zone. |

The renderers of the outputs `router-nftables` and `networkd` check the rest, the number of
target tables and where the next hops' routes and the kernel's rules go among the `Network`
documents, when the router renders its config, at a delivery or at
`chalkctl apply-config --dry-run`. `chalkctl validate` does not run them. A renderer's refusal
fails the delivery, and `chalkctl` prints it in the same form:

| Message | Fix |
| --- | --- |
| `NextHop/wanb4: spec.interface: NextHop wanb4 is on wanb0, which no Network document names; give the link one, which gets the next hop's route` | Add a `Network` that names the link in `Name=`. The renderer checks every `NextHop`, also one that no rule names. |
| `Network/21-wan: spec.match.name: Network 21-wan matches wanb0 and other links, which would take its policy routing too; give wanb0 a Network of its own` | Give the next hop's link a `Network` that names it alone. |
| `Network/21-wanb: spec.match: Network 21-wanb may match wanb0 by other [Match] keys; name wanb0 in a Network that its Name= alone matches` | Drop the other `[Match]` keys, or add a `Network` of `Name=wanb0` alone that sorts first. |
| `Network/00-nic: spec.match: Network 00-nic may match lo by other [Match] keys; name lo in a Network that its Name= alone matches` | Add a `Network` of `Name=lo` that sorts before `00-nic`. A `00-nic` without `Name=` may instead sort after `00-policy-routing`. |
| `Network/99-all: spec.match.name: Network 99-all matches lo and other links, which would take its policy routing too; give lo a Network of its own` | Add a `Network` of `Name=lo` that sorts before `99-all`. |
| `Network/00-policy-routing: metadata.name: Network 00-policy-routing takes the name of the policy routing's unit; rename the document` | Rename the `Network`. |
| `PolicyRoute/servers: spec.rules[3].to: the PolicyRoute documents send traffic to 256 tables, more than the 255 their marks tell apart` | Send the traffic to fewer tables. The refusal names the rule whose target is the 256th table, in the order of the documents and their rules. |

The router writes the kernel's rules into the `Network` that networkd applies to `lo`. A
`Network` that matches `lo` by a glob together with other links, such as `Name=*`, would carry
them to every link it matches, so the renderer refuses it. A `Network` of `lo` by `Name=lo`
alone, sorted before it, takes the rules instead.

## Limits

- `rpFilter: strict` drops the replies that come back through a next hop's link, unless the main
  table routes their source through that link too. Keep `loose`, the default, with next hops.
- The replies to traffic sent into a VRF come back in the VRF and need a route back in its table,
  from a BGP import or a `Network`'s `[Route] Table=`.
- A delivery that adds or removes a target table can renumber the marks. The firewall and networkd
  reload in the same delivery, in no set order. While they reload, a packet may take another
  target's table or the main table.
- The rules see the forwarded traffic and the traffic to the router in prerouting. The router's
  own traffic takes no policy.
- The router does not mark connections. The replies to a connection that came in through an
  uplink leave by the routes or policies of their own direction.
- A policy matches packets. A connection whose packets carry different DSCP values can take two
  paths.
- A rule's `destination` matches the address before the firewall's DNAT rules translate it. A
  packet from a VRF's link that no rule marked meets the chain again with the VRF's device, after
  DNAT, where a document without `in` matches its translated address.
- networkd adds a next hop's route only once the addresses of its link reach the gateway. Until
  then the link stays `configuring`, and the next hop's traffic is dropped. This holds for a
  `NextHop` that no rule names too: remove one you no longer use, so its link can come up.

## What next

- [Policy routing on a router](../how-it-works/router/policy-routing.md) explains the marks, the
  chain and networkd's rules.
- [Filter and translate traffic](firewall-and-nat.md) writes the zones and address sets the rules
  name.
- [Separate routing with VRFs](vrfs.md) declares the VRFs a policy sends traffic into.
- [Config kinds](../reference/kinds.md#policyroute) describes each field of `PolicyRoute` and
  [of `NextHop`](../reference/kinds.md#nexthop).
- [Built-ins](../reference/builtins.md#diagnostic-ip-rules) describes the diagnostics `ip-rules`
  and `routes`.
