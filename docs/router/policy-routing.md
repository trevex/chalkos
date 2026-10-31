---
title: "Route traffic by policy"
description: "Send a router's forwarded traffic by source, port or DSCP into a VRF, another table or through a next hop, whatever its destination's route"
---

# Route traffic by policy

A `PolicyRoute` document sends the traffic its rules match to a VRF's table, another table or a
next hop. The router then routes that traffic by the target's table, whatever route its
destination has in the table of the link it came in on. This page sends a LAN's traffic into a
VRF, through a second uplink and back to the main table, and checks the result.
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
[Separate routing with VRFs](vrfs.md) describes. A next hop needs a `Network` that names the next
hop's link alone in `[Match] Name=`. Keep the default reverse-path filter, `rpFilter: loose`, when
traffic leaves through a next hop; [Limits](#limits) explains why.

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
    - match: {destination: [192.0.2.0/24, "2001:db8:1::/64", "2001:db8:2::/64"]}
      to: {table: 254}
    - match: {source: ["set:guests"], protocol: tcp, destinationPorts: [443]}
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
`wanb0`, a next hop per family:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: PolicyRoute
metadata: {name: servers}
spec:
  in: {interfaces: [lan0]}
  rules:
    - match: {source: [192.0.2.32/27]}
      to: {nextHop: {address: 198.51.100.130, interface: wanb0, table: 200}}
    - match: {source: ["2001:db8:1::20/123"]}
      to: {nextHop: {address: "2001:db8:b::2", interface: wanb0, table: 201}}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 21-wanb}
spec:
  match: {name: wanb0}
  network: {address: [198.51.100.129/25, "2001:db8:b::1/64"], ipv6AcceptRA: false}
```

A next hop has an address, its link and a table of its own, which no other next hop and no other
rule names. `21-wanb` has no gateway: the main table routes through `wana0` alone. The router
fills each next hop's table with a default route through the next hop, followed by an unreachable
default route. While `wanb0` is down, the servers' traffic hits the unreachable default and is
dropped instead of falling back to the main table. networkd adds the route through the next hop
again once the link comes up.

A rule with a next hop matches packets of the next hop's family alone. The first rule above
matches IPv4 and the second IPv6, so each family of the servers goes through its own next hop.
The rules refuse a rule whose `family` or addresses leave out the next hop's family.

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
- `source` and `destination`: prefixes, or `set:<name>` of an `AddressSet` alone.

It adds `dscp`, a list of values from 0 to 63. 46 is EF, expedited forwarding. A match needs at
least one term. To send every packet, say so, such as with `source: [0.0.0.0/0, "::/0"]`.

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

The rules of the router kinds refuse documents that do not fit together, before chalkctl sends
anything:

| Message | Fix |
| --- | --- |
| `PolicyRoute lan: in names a zone or interfaces, one of them; leave in out for traffic from every link` | Keep `zone` or `interfaces`, or drop `in`. |
| `PolicyRoute lan names the zone dmz, which no FirewallZone document declares` | Add the zone, or name one that exists. |
| `PolicyRoute lan: transit is the device of VRF transit; name the VRF's links, or a zone of the VRF` | Name the VRF's links in `interfaces`, or a zone that holds them. |
| `PolicyRoute servers: the next hop 2001:db8:b::2 takes table 201, which another next hop or a rule's table takes too; give each next hop a table of its own` | Give the next hop a table that no other target uses. |
| `PolicyRoute "lan" spec.rules: a rule has an empty match, which would take every packet; to send every packet, say so, such as with source: [0.0.0.0/0]` | Add a term to the match. |
| `PolicyRoute "lan" spec.rules: a rule's to names a VRF, a table or a next hop, one of them` | Keep one target in `to`. |
| `PolicyRoute "servers" spec.rules: a rule's next hop is of another family than the packets the rule matches` | Match the next hop's family, or take a next hop of the rule's family. |
| `PolicyRoute "lan" spec.rules: a rule names a VRF that no VRF document declares` | Add the `VRF` document, or fix the name. |
| `PolicyRoute "lan" spec.rules: a rule's table is 255, the kernel's local table, or a VRF's table; name the VRF in vrf, or take another table` | Name the VRF in `vrf`, or take another table. |
| `PolicyRoute "servers" spec.rules: a next hop's table is 253, 254 or 255, the kernel's own, or a VRF's table; give the next hop a table of its own` | Take a table that is neither the kernel's nor a VRF's. |

The renderers of the outputs `router-nftables` and `networkd` check the rest against the
`Network` documents, before the delivery changes anything. `chalkctl validate` does not run them.
A renderer's refusal fails the delivery, and `chalkctl logs r1` shows the reason on a line that
starts with `chalkos-render:`:

| Message | Fix |
| --- | --- |
| `PolicyRoute lan: tran* matches transit, the device of VRF transit; name the VRF's links or a zone of the VRF` | Name the VRF's links, or a zone that holds them. |
| `PolicyRoute lan: n64-* matches n64-edge, the link of NAT64 edge, whose traffic Jool translates; name other links` | Narrow the glob, so it leaves out the NAT64's link. |
| `PolicyRoute lan: the zone core holds no link` | Give the zone a link, or name another zone. |
| `PolicyRoute servers rule 1: the next hop 198.51.100.130 is on wanb0, which no Network document names; give it one, which gets the next hop's route` | Add a `Network` that names the link in `Name=`. |
| `PolicyRoute servers rule 1: Network 21-wan matches wanb0 and other links, which would take its policy routing too; give wanb0 a Network of its own` | Give the next hop's link a `Network` that names it alone. |
| `PolicyRoute servers rule 1: Network 21-wanb may match wanb0 by other [Match] keys; name wanb0 in a Network that its Name= alone matches` | Drop the other `[Match]` keys, or add a `Network` of `Name=wanb0` alone that sorts first. |
| `PolicyRoute lan, servers: Network 00-nic may match lo by other [Match] keys; name lo in a Network that its Name= alone matches` | Add a `Network` of `Name=lo` that sorts before `00-nic`. A `00-nic` without `Name=` may instead sort after `00-policy-routing`. |
| `PolicyRoute lan, servers: Network 99-all matches lo and other links, which would take its policy routing too; give lo a Network of its own` | Add a `Network` of `Name=lo` that sorts before `99-all`. |
| `Network 00-policy-routing takes the name of the policy routing's unit; rename the document` | Rename the `Network`. |
| `the PolicyRoute documents send traffic to 256 tables, more than the 255 their marks tell apart` | Send the traffic to fewer tables. |

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
  then the link stays `configuring`, and the next hop's traffic is dropped.

## What next

- [Policy routing on a router](../how-it-works/router/policy-routing.md) explains the marks, the
  chain and networkd's rules.
- [Filter and translate traffic](firewall-and-nat.md) writes the zones and address sets the rules
  name.
- [Separate routing with VRFs](vrfs.md) declares the VRFs a policy sends traffic into.
- [Config kinds](../reference/kinds.md#policyroute) describes each field.
- [Built-ins](../reference/builtins.md#diagnostic-ip-rules) describes the diagnostics `ip-rules`
  and `routes`.
