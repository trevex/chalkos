---
title: "Translate to IPv4 and resolve names"
description: "Give IPv6-only networks the IPv4 internet through a router's NAT64, answer their DNS queries with DNS64 and local zones, and check the translations and the resolver"
---

# Translate to IPv4 and resolve names

This page shows how a router gives IPv6-only clients the IPv4 internet. A `NAT64` translates their
connections to addresses of a NAT64 prefix into IPv4 connections. A `Resolver` answers their DNS
queries. With DNS64 it answers for a name that has only A records with AAAA records in that
prefix.
[NAT64 and DNS64 on a router](../how-it-works/router/nat64-and-dns.md) explains how chalkos runs
both.

## Before you begin

You need a router set up as [Set up a router](set-up.md) describes, and the secrets file. The
examples configure the fabric edge `r1` of [Route with BGP](bgp.md):

- Its IPv6-only clients are in `2001:db8::/32`, behind `eth1` and `eth2`.
- Its uplink `eth0` reaches the IPv4 internet.
- It announces `192.0.2.0/24` to its transit provider, so the provider routes those addresses to
  it.

A NAT64's connections leave from IPv4 addresses that the router holds on its uplink, or that the
uplink routes to it. The firewall's NAT never sees a translated connection, so a masquerade
`NATRule` does not translate it, and a private address behind one does not work.

## Add a NAT64

A `NAT64` is one translator. It takes the well-known prefix `64:ff9b::/96` unless `prefix` says
otherwise:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: NAT64
metadata: {name: edge}
spec:
  pool4: [{prefix: 192.0.2.8/30}]
```

A client's connection to `64:ff9b::c633:6402` leaves the router as a connection to
`198.51.100.2`, the IPv4 address that the last 32 bits embed. It leaves from an address and port
of `pool4`. Each entry of `pool4` gives:

- `prefix`: IPv4 addresses, 65536 at most.
- `ports`: a port or a range `<first>-<last>`, by default `61001-65535`. For ICMP they are the
  identifiers of echo requests. How many ports each address has bounds how many connections it
  carries for a protocol.
- `protocols`: `tcp`, `udp` and `icmp` by default.

An empty `pool4`, the default, takes the router's own IPv4 addresses with the ports 61001 to
65535. That suits an uplink whose address comes from DHCP. One NAT64 at most has an empty
`pool4`.

The router's own connections take the kernel's ephemeral ports, 32768 to 60999, and Jool
refuses them in a pool. A range that touches them is refused:

```text
chalkctl: r1: the config is refused: NAT64 "edge" spec.pool4: a pool entry takes the ports 1-65535, which lie outside 1-32767 and 61000-65535 or run backwards; Jool refuses the kernel's ephemeral ports, 32768 to 60999
```

To give the pool's addresses most of their ports, give them two entries:

```yaml
spec:
  pool4:
    - {prefix: 192.0.2.8/30, ports: "1-32767"}
    - {prefix: 192.0.2.8/30, ports: "61000-65535"}
```

A node may hold several `NAT64` documents, each with a prefix and a pool of its own. A NAT64's
name has 15 characters at most. Prefixes are 32, 40, 48, 56, 64 or 96 bits long, as RFC 6052
gives them. Two NAT64s whose prefixes overlap, or whose pools share an address, are refused. So
are two entries of one pool that share an address, a protocol and a port.

A delivery that changes a NAT64's pool keeps its connections. One that changes its prefix, or
renames it, replaces the translator, and the connections it carried end.

## Announce the prefix

The router installs an unreachable route to each NAT64's prefix, which lets BGP announce it. Add
the prefix to the networks of the `BGP` document, so that the clients' routers send its traffic to
this router:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: BGP
metadata: {name: bgp}
spec:
  asn: 64512
  routerID: 192.0.2.1
  networks:
    - {prefix: "2001:db8:ffff::1/128"}
    - {prefix: "64:ff9b::/96"}
    - {prefix: 192.0.2.0/24}
```

A router without BGP needs routes to the prefix on the clients' side, such as a static route of a
`Network` document on their router.

## Resolve names with DNS64

A `Resolver` runs the router's DNS resolver. A node has one at most:

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

The delivery applies the NAT64s and reloads the resolver, which keeps its cache. A `Resolver`
added starts the resolver, and one removed stops it. `chalkctl status` shows both after
the BGP sessions:

```sh
chalkctl status r1 --secrets secrets.age
```

```text
nat64:
  INSTANCE  SESSIONS  BINDINGS  POOL TCP  POOL UDP  POOL ICMP
  edge      1         1         18140     18140     18140
resolver: running
```

Each NAT64 has a line. `SESSIONS` counts the connections it translates, over all protocols.
`BINDINGS` counts the IPv4 addresses and ports that those connections hold. The `POOL` columns
count the addresses and ports of each protocol's pool: 4 addresses of 4535 ports each here. A
protocol that the pool leaves out shows `-`. So does every protocol of an empty pool, which takes
the router's own addresses.

`nat64: unknown:` gives jool's error when chalkd could not read the NAT64s' state.
`resolver: not running:` gives unbound-control's error. A router without `NAT64` documents prints
no `nat64:` line, and one without a `Resolver` no `resolver:` line. A node in maintenance mode
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

The query comes from the router's own address, which `allow` must hold. `nat64-sessions` lists a
NAT64's connections, for TCP, then UDP, then ICMP. Each shows the remote host's and the client's
address and port, then the pool's address and port and the address the client connected to:

```sh
chalkctl node diag r1 --secrets secrets.age nat64-sessions instance=edge
```

```text
---------------------------------
(V4_FIN_V6_FIN_RCV) Expires in 0:03:59.285
Remote: 198.51.100.2#443	2001:db8:1::2#41784
Local: 192.0.2.8#62646	64:ff9b::c633:6402#443
---------------------------------
---------------------------------
---------------------------------
```

Both diagnostics need an operator client: `nat64-sessions` shows who connects where. The status
needs a reader.

## What next

- [NAT64 and DNS64 on a router](../how-it-works/router/nat64-and-dns.md) explains the translator,
  the resolver's config and their limits.
- [Route with BGP](bgp.md) announces the NAT64 prefix.
- [Config kinds](../reference/kinds.md#router-chalkos-dev) describes each field.
