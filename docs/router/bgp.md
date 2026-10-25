---
title: "Route with BGP"
description: "Give a router a BGP instance, peer with its neighbours on interfaces or at addresses, filter routes with prefix lists and routing policies, and check its sessions and routes"
---

# Route with BGP

This page shows how to run BGP on a router: its instance, its peers, the policies on their routes
and how to check them. A router without a `BGP` document runs no BGP.
[BGP on a router](../how-it-works/router/bgp.md) explains how chalkos turns the documents into
FRR's config.

## Before you begin

You need a router set up as [Set up a router](set-up.md) describes, and the secrets file. The
examples configure the fabric edge `r1`. It peers with the leaves of a fabric on `eth1` and
`eth2` without addresses of its own there, and with a transit provider at `203.0.113.254` on its
WAN. Each of its interfaces has a `Network` document:

```yaml
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-fabric}
spec:
  match: {name: [eth1, eth2]}
  network: {ipv6AcceptRA: false}
```

`ipv6AcceptRA: false` keeps networkd from installing a default route from the router
advertisements on the link. FRR sends and reads those advertisements to find its neighbours. FRR's
zebra prefers a route that the kernel already holds from elsewhere, such as a default route from
router advertisements, over the path BGP learns, and then installs no route of its own for that
prefix. Turn router advertisements off on each interface whose default route should come from BGP.

## Give the router a BGP instance

A node has one `BGP` document at most. It gives the router its AS number and router ID:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: BGP
metadata: {name: bgp}
spec:
  asn: 64512
  routerID: 192.0.2.1
  multipath: {ebgp: 64, asPathRelax: true}
  networks:
    - {prefix: "2001:db8:ffff::1/128"}
    - {prefix: "64:ff9b::/96", policy: nat64}
    - {prefix: 192.0.2.0/24}
```

`multipath` installs up to 64 equal paths from external peers for one prefix, and `asPathRelax`
counts paths through different leaves' ASes as equal. The router announces each of `networks`
while its own table holds a route to it, such as the address of a loopback or a static route. A
network's `policy` sets the attributes of its announcement, as a term of
[a routing policy](#filter-routes-with-policies) does. Two networks of one range are refused.

`routerID` is required. BGP names a router by an IPv4 address, which need not be one of its own,
so give an IPv6-only router one too.

## Peer on the fabric's links

A `BGPPeer` of `interfaces` runs a session on each link it names, with the neighbour it finds
there by its IPv6 link-local address:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: BGPPeer
metadata: {name: fabric}
spec:
  interfaces: [eth1, eth2]
  remoteAS: external
  bfd: true
  extendedNextHop: true
  password: {secretRef: {name: bgp, key: fabric}}
  description: the fabric's leaves
  families:
    ipv6: {defaultOriginate: true, import: from-fabric, export: to-fabric}
```

`remoteAS: external` takes a neighbour of any AS but the router's own, and `internal` one of the
router's own. With `bfd`, BFD checks the link at the timers of the `BGP` document and ends the
session when it finds the link down. With the default timers of 300 ms and a multiplier of 3 at
both ends, that takes about 900 ms. `defaultOriginate` sends the leaves a default route whatever
the router's own table holds. `extendedNextHop` carries IPv4 routes over IPv6 sessions, for a
peer whose `families` gives `ipv4` too.

`interfaces` and `interface` find their links in different ways:

- `interfaces` takes names and globs, such as `eth*`. Each matches the interface names in the
  `[Match]` sections' `Name=` of the node's `Network` documents. A link that no `Network` names
  that way gets no session, also when the interface exists.
- `interface: eth1` names one link and runs its session whether or not a `Network` names it.

`description` holds up to 80 printable ASCII characters. An `@` and two spaces in a row are
refused.

The password is a key of a `Secret`:

```yaml
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: bgp}
spec:
  data: {fabric: <password>}
```

`<password>` stands for the session's TCP MD5 password, which the neighbour sets too. FRR takes
one of 1 to 80 printable characters without white space. Another password fails `frr.service`
when the config is delivered or the node boots. The name and key of the `Secret` hold letters,
digits, `.`, `_` and `-` alone.

## Peer at an address

A peer of `address` runs one session with that neighbour:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: BGPPeer
metadata: {name: transit}
spec:
  address: 203.0.113.254
  remoteAS: 65001
  families:
    ipv4: {import: from-transit, export: to-transit, maxPrefix: 1000}
```

`maxPrefix` ends the session once the neighbour sends more than 1000 routes. A peer names exactly
one of `address`, `interface` and `interfaces`, and one family at least. A family without
`import` accepts every route, and one without `export` sends every route the router has.

The address is a unicast address beyond the router. A link-local address is refused, as a
neighbour on a link takes `interface` or `interfaces`. A peer at an IPv6 address that exchanges
IPv4 routes needs `extendedNextHop: true`. A session that two peers name is refused.

## Filter routes with policies

A `RoutingPolicy` decides which routes a peer's family accepts or sends. Its terms match in
order, and the first that matches decides. `default` decides the rest, and is `reject` unless the
policy says otherwise:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: RoutingPolicy
metadata: {name: from-fabric}
spec:
  terms:
    - {match: {prefixList: fabric}, then: {action: accept}}
  default: reject
---
apiVersion: router.chalkos.dev/v1alpha1
kind: RoutingPolicy
metadata: {name: to-fabric}
spec:
  terms:
    - {match: {prefixList: edge-originated}, then: {action: accept}}
  default: reject
---
apiVersion: router.chalkos.dev/v1alpha1
kind: PrefixList
metadata: {name: fabric}
spec:
  family: ipv6
  entries: [{prefix: "2001:db8::/32", le: 128}]
---
apiVersion: router.chalkos.dev/v1alpha1
kind: PrefixList
metadata: {name: edge-originated}
spec:
  family: ipv6
  entries:
    - {prefix: "2001:db8:ffff::/48", le: 128}
    - {prefix: "64:ff9b::/96"}
    - {prefix: "::/0", action: deny}
```

A `PrefixList` entry without `ge` or `le` matches its prefix alone. `le: 128` also matches the
routes within `2001:db8::/32` up to `/128`. Both `ge` and `le` lie from the prefix's length to the
family's longest prefix, and `ge` is no higher than `le`. Two entries with the same prefix, `ge`
and `le` are refused, because the later one never decides. The PrefixLists of a policy are of one
family, and a peer's family takes policies of its own family alone.

A term may set attributes of the routes it accepts. The transit's policies keep its blackholed
routes out, prefer the routes of its own AS and tag the rest. They announce the router's IPv4
range, and the ranges within it from `/25`, with a MED of 50 and a longer AS path:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: RoutingPolicy
metadata: {name: from-transit}
spec:
  terms:
    - {match: {communityList: blackhole}, then: {action: reject}}
    - match: {asPath: "^65001_"}
      then: {action: accept, localPref: 200, removeCommunities: ["65001:100"]}
    - then: {action: accept, addCommunities: ["64512:100", "64512:100:1"]}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: RoutingPolicy
metadata: {name: to-transit}
spec:
  terms:
    - {match: {prefixList: transit-out}, then: {action: accept, med: 50, prepend: 2}}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: PrefixList
metadata: {name: transit-out}
spec:
  family: ipv4
  entries: [{prefix: 192.0.2.0/24, ge: 25, le: 32}, {prefix: 192.0.2.0/24}]
---
apiVersion: router.chalkos.dev/v1alpha1
kind: CommunityList
metadata: {name: blackhole}
spec: {communities: ["65001:666"]}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: RoutingPolicy
metadata: {name: nat64}
spec:
  terms:
    - then: {action: accept, addCommunities: ["64512:64"]}
```

`asPath` is a regular expression over the route's AS path, in which `_` stands for the start, the
end or a space between two ASes. It holds digits, single spaces and `_^$.*+?()|{},-`, without
brackets. FRR's bgpd compiles it as a POSIX extended regular expression of glibc, and the
delivery is refused when the output `frr` finds one that bgpd would not compile, such as `^*`.

`prepend: 2` puts the router's AS in front of the path twice. A `CommunityList` holds standard
communities, `<AS>:<value>`, or large ones, `<AS>:<value>:<value>`, not both, and each community
once. A term that rejects sets nothing.

The order of terms and entries gives their sequence numbers in FRR's config. A term or entry put
before others renumbers those after it, and the reload rewrites them.

## Deliver and check the sessions

Validate the files and deliver them:

```sh
chalkctl validate
chalkctl apply-config r1 --secrets secrets.age
```

A delivery reloads FRR, which applies the changes and keeps the sessions they leave alone.
`chalkctl status` lists the router's sessions under `bgp:`:

```sh
chalkctl status r1 --secrets secrets.age
```

```text
bgp:
  PEER     NEIGHBOR       STATE        UPTIME  RECEIVED  SENT  BFD
  fabric   eth1           Established  1h2m3s  1         2     up
  fabric   eth2           Idle         -       0         0     down
  transit  203.0.113.254  Active       -       0         0     -
```

Each line is a session: on a link, named by its interface, or with an address. `RECEIVED` counts
the routes the router accepted from the neighbour and `SENT` those it sends, over every family.
`STATE` is BGP's state of the session, and a session in any state but `Established`, such as
`Active` or `Idle`, exchanges no routes. `bgp: no sessions` means the router has no BGP instance
or no session. `bgp: unknown:` gives vtysh's error when chalkd could not read FRR's state, such as
`failed to connect to any daemons` while FRR does not run. A node in maintenance mode runs no
FRR.

`chalkctl node routes` shows the kernel's routes and the paths BGP selected, to a prefix and
within it when one is given. A leaf `r2` with sessions to two edges of AS 64512, each of which
originates a default route, shows:

```sh
chalkctl node routes r2 ::/0 --secrets secrets.age
```

```text
DESTINATION      TYPE       PROTOCOL  TABLE  METRIC  NEXT HOPS
::/0             unicast    bgp       254    20      fe80::1 on eth2 (weight 1), fe80::2 on eth1 (weight 1)
2001:db8:2::/48  blackhole  boot      254    1024    -

BGP PREFIX            BEST  AS PATH  PEER   NEXT HOPS                  LOCAL PREF  MED  COMMUNITIES
::/0                  *     64512    eth2   fe80::e06b:dcff:fe54:f465  200         0    64512:1 64512:1:2
::/0                        64512    eth1   fe80::b817:4cff:fe8b:9cf   200         0    -
2001:db8:2::/48       *     -        local  ::                         -           0    -
2001:db8:ffff::1/128  *     64512    eth2   fe80::e06b:dcff:fe54:f465  200         0    -
2001:db8:ffff::1/128        64512    eth1   fe80::b817:4cff:fe8b:9cf   200         0    -
```

The kernel's default route has two next hops, one for each path BGP installed. `*` marks the
best path of a prefix, and `local` a path of the router's own networks. `--protocol` and
`--table` narrow the kernel's routes, as
[chalkctl node routes](../reference/cli/chalkctl_node_routes.md) describes.

The diagnostics `bgp-summary`, `bgp-neighbor`, `bgp-route` and `bfd-peers` print FRR's own views
of the sessions, one session, the paths to a prefix and the BFD sessions:

```sh
chalkctl node diag r1 --secrets secrets.age bgp-neighbor peer=eth1
chalkctl node diag r1 --secrets secrets.age bgp-route prefix=::/0
chalkctl node diag r1 --secrets secrets.age bgp-route family=ipv4 prefix=192.0.2.0/24
```

`bgp-route` asks for IPv6 unless `family` says `ipv4`. The status, the routes and these
diagnostics need a reader client.

## Change the AS number

The rules refuse a delivery that changes `spec.asn` of the `BGP` document:

```text
chalkctl: r1: the config is refused: BGP "bgp" spec.asn: the AS number stays 64512; to change it, deliver the config without the BGP document, which ends the router's sessions, and then with the new AS number
```

Deliver the config without the `BGP` and `BGPPeer` documents first, which ends every session, as
a `BGPPeer` needs a `BGP` document. Then deliver it with the new number. The rule compares the
`BGP` document of one name. A `BGP` document of another name replaces the old one in one
delivery, AS number included, and the change of AS ends the sessions too.

## What next

- [BGP on a router](../how-it-works/router/bgp.md) explains the rendered config, the passwords
  and the state chalkd reads.
- [Config kinds](../reference/kinds.md#router-chalkos-dev) describes each field.
- [Filter and translate traffic](firewall-and-nat.md) filters what the router forwards. The
  firewall accepts each peer's BGP and BFD ports by itself.
