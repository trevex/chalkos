---
title: "Translate to IPv4 and resolve names"
description: "Give IPv6-only networks the IPv4 internet through a router's NAT64, answer their DNS queries with DNS64 and local zones, do both in a VRF, and check the translations and the resolvers"
---

# Translate to IPv4 and resolve names

This page shows how a router gives IPv6-only clients the IPv4 internet. A `NAT64` translates their
connections to addresses of a NAT64 prefix into IPv4 connections. A `Resolver` answers their DNS
queries. With DNS64 it answers for a name that has only A records with AAAA records in that
prefix. Both work in the default VRF and in each other VRF.
[NAT64 and DNS64 on a router](../how-it-works/router/nat64-and-dns.md) explains how chalkos runs
both.

## Before you begin

You need a router set up as [Set up a router](set-up.md) describes, and the secrets file. The
examples configure the fabric edge `r1` of [Route with BGP](bgp.md):

- Its IPv6-only clients are in `2001:db8::/32`, behind `eth1` and `eth2`.
- Its uplink `eth0` reaches the IPv4 internet.
- It announces `192.0.2.0/24` to its transit provider, so the provider routes those addresses to
  it.

A NAT64's connections leave from IPv4 addresses that the uplink routes to the router. With an
empty `pool4` they leave from the address of the uplink they leave through. The firewall's NAT
rules leave them alone either way.

## Add a NAT64

A `NAT64` is one translator. It takes the well-known prefix `64:ff9b::/96` unless `prefix` says
otherwise:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: NAT64
metadata: {name: edge}
spec:
  pool4: [{prefix: 192.0.2.252/30}]
```

A client's connection to `64:ff9b::c633:6402` leaves the router as a connection to
`198.51.100.2`, the IPv4 address that the last 32 bits embed. It leaves from an address and port
of `pool4`. Each entry of `pool4` gives:

- `prefix`: IPv4 addresses, 65536 at most.
- `ports`: a port or a range `<first>-<last>`, by default `61001-65535`. For ICMP they are the
  identifiers of echo requests. How many ports each address has bounds how many connections it
  carries for a protocol.
- `protocols`: `tcp`, `udp` and `icmp` by default.

An empty `pool4`, the default, takes the address of the link a connection leaves through, with
the ports 61001 to 65535. The router masquerades the NAT64's own address to it. That suits an
uplink whose address comes from DHCP. Several NAT64s may have an empty `pool4`.

An entry of `pool4` holds addresses that the uplink routes to the router, never an address of
the router's own links. The router answers its own addresses itself, so their replies never
reach the NAT64. The rules refuse an entry that holds an address a `Network` document gives a
link, such as `192.0.2.253/24` on the uplink of `10-wan`, so `chalkctl validate` reports it:

```text
NAT64 edge: the pool entry 192.0.2.252/30 holds 192.0.2.253, an address a Network document gives a link; the router answers it itself, so leave pool4 empty to take the address of the link the connections leave through
```

An address that DHCP gives the uplink is not in any document, so the rules cannot refuse it.
`chalkos-nat64.service` checks the router's addresses instead. When a pool holds one, the unit
fails before it changes any instance and names the link:

```text
chalkos-nat64: NAT64 edge: the pool entry 192.0.2.252/30 holds 192.0.2.253, an address of the link eth0, which the router answers itself; leave pool4 empty to take the address of the link the connections leave through; no instance changed
```

Give such an uplink an empty `pool4`.

Each address of a pool belongs to its NAT64 alone. No `Network` gives a link one of them, and no
NAT rule translates to one or matches a destination among them. The output `router-nftables`
refuses such a rule:

```text
NATRule web: to.address 192.0.2.253 lies in 192.0.2.252/30, the pool of NAT64 edge, whose addresses belong to the NAT64 whole; translate to an address outside the pool
```

The router's own connections take the kernel's ephemeral ports, 32768 to 60999, and Jool
refuses them in a pool. A range that touches them is refused:

```text
chalkctl: r1: the config is refused: NAT64 "edge" spec.pool4: a pool entry takes the ports 1-65535, which lie outside 1-32767 and 61000-65535 or run backwards; Jool refuses the kernel's ephemeral ports, 32768 to 60999
```

To give the pool's addresses most of their ports, give them two entries:

```yaml
spec:
  pool4:
    - {prefix: 192.0.2.252/30, ports: "1-32767"}
    - {prefix: 192.0.2.252/30, ports: "61000-65535"}
```

A node may hold several `NAT64` documents, each with a prefix and a pool of its own. A NAT64's
name has 11 characters at most, because its link `n64-<name>` is an interface name, which has 15.
Prefixes are 32, 40, 48, 56, 64 or 96 bits long, as RFC 6052 gives them. Two NAT64s of one VRF
whose prefixes overlap are refused, and so are two NAT64s whose pools share an address. Two
entries of one pool that share an address, a protocol and a port are refused too.

A delivery that changes a NAT64's pool keeps its connections. One that changes its prefix, or
renames it, replaces the translator, and the connections it carried end.

### Keep 169.254.64.0/18 free

Each NAT64 joins the router through a link of its own, whose two ends take a /30 of
`169.254.64.0/18`. The /30 follows from a hash of the NAT64's name alone, so a NAT64 keeps its
link's addresses while others come and go. While the node has a NAT64, the rules refuse a static
address in that range on any other link:

```text
a Network document gives a link the address 169.254.96.241, which lies in 169.254.64.0/18, the range of the NAT64s' links; give the link another address
```

networkd's IPv4 link-local addressing picks an address of `169.254.0.0/16` by itself, which may
lie in the NAT64s' range, and no document names it. Leave it off on a router with NAT64s: it is
off unless a `Network` sets `linkLocalAddressing` to `ipv4` or `yes`.

Two names may hash to the same /30. Among 10 NAT64s the chance is about 1%, and among 20 about
4.5%. The output `jool` then refuses the later name:

```text
NAT64 nat8 and NAT64 nat282 both take the link addresses 169.254.120.112/30, which the hashes of their names pick; rename NAT64 nat8
```

Rename the NAT64 you are adding, even when the message names the other one. The running NAT64
then keeps its link's addresses and the connections that its masquerade tracks.

## Announce the prefix

The router routes each NAT64's prefix to the NAT64's link, a route of the protocol `nat64` that
exists while the NAT64 runs. An explicit pool's prefixes get such a route too. `chalkctl node
routes` lists them:

```sh
chalkctl node routes r1 --protocol nat64 --secrets secrets.age
```

```text
DESTINATION     TYPE     PROTOCOL  TABLE  METRIC  NEXT HOPS
192.0.2.252/30  unicast  nat64     254    0       169.254.96.242 on n64-edge
64:ff9b::/96    unicast  nat64     254    1024    fe80::2 on n64-edge
```

BGP announces a network while the router's table holds a route to it, so the prefix belongs in
the networks of the `BGP` document. The clients' routers then send its traffic to this router.
`r1`'s `BGP` of [Route with BGP](bgp.md) lists it already, with the routing policy `nat64`, which
tags its announcement with a community:

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

A router without BGP needs routes to the prefix on the clients' side, such as a static route of a
`Network` document on their router.

## Resolve names with DNS64

A `Resolver` runs a DNS resolver of the router. A VRF has one at most, and a `Resolver` without
`vrf` serves the default VRF:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: Resolver
metadata: {name: resolver}
spec:
  listen: ["2001:db8:ffff::1"]
  allow: ["2001:db8::/32"]
  dns64: {prefix: "64:ff9b::/96"}
```

The resolver answers on port 53 of each address of `listen`, over UDP and TCP, to the clients of
`allow`. It refuses every other client. The firewall accepts DNS from `allow` to `listen` by
itself, ahead of the policies to `local`. `listen` holds addresses of the router, such as the
address of a loopback or a dummy interface, or of an interface to the clients. A config whose
`listen` and `allow` share no family is refused, as the resolver would answer no one.

!!! warning "A range of a whole family opens the resolver to the internet"

    An `allow` of `::/0` or `0.0.0.0/0` answers whoever reaches an address of `listen`. When one
    of them faces the internet, the router is an open resolver, which others use for
    amplification attacks. Give `allow` the clients' ranges alone.

`dns64` synthesises an AAAA record in its prefix from each A record of a name that has no AAAA
record, so clients reach IPv4-only names through the NAT64. Give it the prefix of a NAT64 that the
clients route to the router; the rules do not compare the two. The resolver also answers
`ipv4only.arpa` with RFC 8880's addresses in the prefix, from which clients learn the prefix
itself.

Without `forwarders` the resolver looks names up itself, from the root servers. To send queries
to other resolvers instead, list them:

```yaml
spec:
  forwarders: ["2001:db8:2::53", 198.51.100.53]
```

`dnssec`, `true` by default, validates the answers with the root's trust anchor of the image.
Forwarders must then pass DNSSEC records on. Turn validation off with `dnssec: false` when they
do not.

## Translate and resolve in a VRF

A `NAT64` and a `Resolver` with `vrf` serve the clients of that VRF alone. The VRF `guest` of
[Separate routing with VRFs](vrfs.md#keep-a-vrfs-connections-apart) holds a guest LAN on `eth3`,
with the address `2001:db8:3::1/64`, and an uplink of its own on `eth4`. Its guests get a NAT64
and a resolver of their own:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: NAT64
metadata: {name: guest}
spec: {vrf: guest}
---
apiVersion: router.chalkos.dev/v1alpha1
kind: Resolver
metadata: {name: guest}
spec:
  vrf: guest
  listen: ["2001:db8:3::1"]
  allow: ["2001:db8:3::/64"]
  dns64: {prefix: "64:ff9b::/96"}
```

The NAT64 `guest` has an empty `pool4`, so its connections leave `eth4` from the uplink's
address. The router routes its prefix to its link in `guest`'s table, where the guests reach it,
and no other VRF does:

```sh
chalkctl node routes r1 --vrf guest --protocol nat64 --secrets secrets.age
```

```text
DESTINATION   TYPE     PROTOCOL  TABLE  METRIC  NEXT HOPS
64:ff9b::/96  unicast  nat64     300    1024    fe80::2 on n64-guest
```

A NAT64 of another VRF may translate the same prefix. Each VRF's table routes the prefix to its
own NAT64, so the NAT64s never see each other's clients. Two NAT64s' pools share no address,
whatever their VRFs. To announce a VRF's prefix, name it in `networks` of the VRF's `BGP`
document.

The resolver of `guest` answers on the VRF's links alone and sends its own queries out through
them, so the VRF's table needs routes to the root servers or to the forwarders. `listen` holds
addresses of the VRF's links. The output `unbound` refuses an address that `Network` documents
give only to links of other VRFs, such as `2001:db8:ffff::1` on the management link of `10-mgmt`:

```text
Resolver guest: the address 2001:db8:ffff::1 of listen belongs to links of Network 10-mgmt in VRF default alone, so the resolver of VRF guest never receives its queries; listen on an address of the VRF's links
```

The firewall opens the resolver's port 53 on the VRF's links alone. Each VRF has one `Resolver`
at most, and a `NAT64` or `Resolver` that names a VRF no `VRF` document declares is refused.

## Answer local names

`localZones` answers the names of zones from records of the document:

```yaml
spec:
  localZones:
    - name: lan.example.org
      records:
        - {name: router.lan.example.org, type: AAAA, value: "2001:db8:1::1"}
        - {name: lan.example.org, type: MX, value: 10 mail.lan.example.org}
        - {name: _imaps._tcp.lan.example.org, type: SRV, value: 0 1 993 mail.lan.example.org}
        - {name: lan.example.org, type: TXT, value: v=spf1 -all, ttl: 300}
    - {name: ads.example.net, type: always_nxdomain}
```

Names are in lower case, without the root's dot, and a record's name is the zone's or one below
it. A zone of type `static`, the default, answers from its records alone. `transparent` looks up
the names its records lack, and `always_nxdomain` answers that no name exists.
[Config kinds](../reference/kinds.md#resolver) lists the other types and each record type's
value.

The local zones are refused when they break one of these rules:

- They hold 150 records at most, over all zones.
- A record appears once, and an A or AAAA record holds an address of its family.
- A CNAME record's name holds no other record, in any zone.
- A `redirect` zone holds records of its own name alone, which it answers for every name below
  it.
- With `dns64`, no zone is `ipv4only.arpa`, below it or above it, such as `arpa`. The resolver
  would answer such a zone before DNS64 does.
- A record's value holds up to 255 printable ASCII characters, without quotes or backslashes.

DNS64 synthesises no AAAA record from a local zone's A records.

## Tell the clients

The clients learn the resolver and the NAT64 prefix from the router advertisements on their
link. On a router that sends them, such as the LAN router of the clients, set both in the LAN's
`Network` document:

```yaml
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-lan}
spec:
  match: {name: lan0}
  network: {address: ["2001:db8:1::1/64"], ipv6SendRA: true}
  ipv6Prefix: {prefix: "2001:db8:1::/64"}
  ipv6SendRA: {dns: ["2001:db8:ffff::1"]}
  ipv6PREF64Prefix: {prefix: "64:ff9b::/96"}
```

`ipv6SendRA.dns` announces the resolver. `ipv6PREF64Prefix` announces the NAT64 prefix to clients
that synthesise addresses themselves.

## Deliver and check

Validate the files and deliver them:

```sh
chalkctl validate
chalkctl apply-config r1 --secrets secrets.age
```

The delivery applies the NAT64s and reloads the resolvers, which keep their caches. A `Resolver`
added starts its resolver, and one removed stops it. The rules check the documents before
chalkctl sends anything. The renderers' checks against the `Network` documents run on the
router, and a refusal there fails the delivery with
`output jool: its renderer failed (exit 1); see the node's journal`. `chalkctl logs r1` then
shows the message on a line that starts with `chalkos-render:`.

`chalkctl status` shows the NAT64s and the resolvers after the BGP sessions:

```sh
chalkctl status r1 --secrets secrets.age
```

```text
nat64:
  INSTANCE  SESSIONS  BINDINGS  POOL TCP  POOL UDP  POOL ICMP
  edge      1         1         18140     18140     18140
resolver: running
```

Each NAT64 whose instance runs has a line. `SESSIONS` counts the connections it translates, over
all protocols. `BINDINGS` counts the IPv4 addresses and ports that those connections hold. The
`POOL` columns count the addresses and ports of each protocol's pool: 4 addresses of 4535 ports
each here. A protocol that the pool leaves out shows `-`. An empty pool shows 4535 ports per
protocol, the ports 61001 to 65535 of the NAT64's own address.

The resolver of a VRF has a line of its own after the default VRF's. The VRF example's router
prints:

```text
nat64:
  INSTANCE  SESSIONS  BINDINGS  POOL TCP  POOL UDP  POOL ICMP
  guest     3         3         4535      4535      4535
resolver guest: running
```

A NAT64 whose instance did not load has no line. After a boot the status then lists
`failed unit chalkos-nat64.service`, and a delivery fails with the unit's error. A VRF's resolver
that does not run shows its unit's state, such as
`resolver guest: not running: unbound@guest.service is failed`.

`nat64: unknown:` gives jool's error when chalkd could not read the NAT64s' state.
`resolver: not running:` gives unbound-control's error. A router without `NAT64` documents prints
no `nat64:` line, and one without a `Resolver` no `resolver` line. A node in maintenance mode
runs neither.

The diagnostic `dns-query` asks the resolver from the router:

```sh
chalkctl node diag r1 --secrets secrets.age dns-query name=ipv4only.arpa type=AAAA server=2001:db8:ffff::1
```

```text
;; ->>HEADER<<- opcode: QUERY, rcode: NOERROR, id: 28595
;; flags: qr rd ra ; QUERY: 1, ANSWER: 2, AUTHORITY: 0, ADDITIONAL: 0
;; QUESTION SECTION:
;; ipv4only.arpa.	IN	AAAA

;; ANSWER SECTION:
ipv4only.arpa.	3600	IN	AAAA	64:ff9b::c000:ab
ipv4only.arpa.	3600	IN	AAAA	64:ff9b::c000:aa
```

The query comes from the router's own address in the default VRF, which `allow` must hold. It
cannot ask a VRF's resolver. `nat64-sessions` lists a NAT64's connections, for TCP, then UDP,
then ICMP. Each shows the remote host's and the client's address and port, then the pool's
address and port and the address the client connected to:

```sh
chalkctl node diag r1 --secrets secrets.age nat64-sessions instance=edge
```

```text
---------------------------------
(V4_FIN_V6_FIN_RCV) Expires in 0:03:59.285
Remote: 198.51.100.2#443	2001:db8:1::2#41784
Local: 192.0.2.253#62646	64:ff9b::c633:6402#443
---------------------------------
---------------------------------
---------------------------------
```

For a NAT64 with an empty pool, `Local:` shows the NAT64's own address, such as
`169.254.111.206#62646` for `guest`, which the router masquerades to the uplink's address. The
diagnostic runs in the NAT64's network namespace, so an instance that does not run fails with
`failed_precondition: diagnostic nat64-sessions: the network namespace nat64-guest does not exist`.

Both diagnostics need an operator client: `nat64-sessions` shows who connects where. The status
needs a reader.

## Upgrade a router whose NAT64 ran beside its links

On older images, Jool ran in the router's own network namespace and took the router's own
addresses as its pool. Images that run each NAT64 in a namespace of its own refuse or break some
of the configs those images took. Change these before you upgrade. The older image takes each
change, except that it runs one NAT64 with an empty `pool4` at most:

- Rename each NAT64 whose name has more than 11 characters. The rules refuse it with
  `NAT64 <name> has a name of more than 11 characters, which its link n64-<name> does not take as an interface's name; give it a shorter one`.
  A rename replaces the translator, and its connections end.
- Empty each `pool4` that holds an address of the router's own links. An empty pool takes the
  uplink's address, as before. `chalkctl validate` with the new image in the inventory refuses
  a pool of a static address, as [Add a NAT64](#add-a-nat64) shows. A pool of an address from
  DHCP passes it, and `chalkos-nat64.service` then fails at the new image's first boot, so the
  router falls back to the image it ran.
- Move every static address out of `169.254.64.0/18`, and leave IPv4 link-local addressing off
  on the router's links.
- Give no `dnat` or `snat` rule a `to.address` in a NAT64's pool, and no `dnat` rule a
  `match.destination` in a NAT64's pool or prefix.
- Give each VRF whose clients use a NAT64 a `NAT64` of its own, with `vrf` set. On older images
  the one Jool of the router's namespace translated whatever reached its prefix, from any VRF
  whose routes led there. Each NAT64 now translates the traffic of its own VRF alone, so a VRF
  without one loses NAT64.

`chalkctl validate` does not run the renderers, which check the rest when the new image renders
the config at its first boot. A refusal there fails the boot, and the router falls back to the
image it ran. Look for these before you upgrade:

- Two NAT64 names whose hashes pick one /30, as
  [Keep 169.254.64.0/18 free](#keep-16925464018-free) shows. Rename one of them.
- A `Network` that sorts before `00-n64-<name>` and may match the NAT64's link `n64-<name>`, such
  as a catch-all `00-all` with `Name=*`. The renderer refuses it with
  `Network 00-all matches the link n64-edge of NAT64 edge before 00-n64-edge; rename it to sort after 00-n64-edge`.
  Rename it to sort after `00-n64-<name>`, or narrow its `Name=`.
- A `FirewallZone` whose `interfaces` name or glob a NAT64's link, such as `*` or `n*`, and a
  NAT rule whose interface matches NAT64 links alone. Name the links the zone or rule means.

The upgrade reboots the router, and the NAT64s' connections end. Once `chalkos-nat64.service`
runs on the new image, clients connect again. The translated connections now pass the firewall's
connection tracking: each takes two entries, one for its IPv6 side and one for its IPv4 side,
where it took none before. Raise `Router.spec.conntrackMax` when the NAT64s carry many connections.

The zones' policies do not filter the connections a NAT64 translates. Any host whose packets to
a NAT64's prefix reach the router in the NAT64's VRF uses the NAT64. Limit who routes the prefix
to the router, by the routes you announce and by the reverse-path filter.

`chalkos-nat64.service` and `chalkos-resolvers.service` are units of the boot. When either fails
on the new image, the boot's health check fails, and the router falls back to the image it ran,
as [Boot, health and rollback](../how-it-works/node/boot-and-rollback.md) describes.

## What next

- [NAT64 and DNS64 on a router](../how-it-works/router/nat64-and-dns.md) explains the translator,
  its namespace and link, the resolver's config and their limits.
- [Separate routing with VRFs](vrfs.md) sets up the VRFs.
- [Route with BGP](bgp.md) announces the NAT64 prefix.
- [Config kinds](../reference/kinds.md#router-chalkos-dev) describes each field.
