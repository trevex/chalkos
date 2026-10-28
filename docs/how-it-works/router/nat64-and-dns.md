---
title: "NAT64 and DNS64 on a router"
description: "How a router image runs NAT64 with Jool and its resolver with Unbound: the rendered instances and config, the prefix's route, the reloads, the boot order, the firewall's port and the state chalkd reads"
---

# NAT64 and DNS64 on a router

A router image translates IPv6 connections to IPv4 with Jool, a stateful NAT64 in the kernel. It
resolves names with Unbound, whose DNS64 module synthesises AAAA records in a NAT64 prefix. The
output `jool` renders an instance of Jool per `NAT64` document, and the output `unbound` renders
Unbound's whole config from the `Resolver` document. The documents name their prefixes
independently: a NAT64 names the prefix it translates, the resolver the prefix it synthesises
in, and `BGP.spec.networks` the prefix BGP announces.

## The output jool renders an instance per NAT64

`chalkos-render jool` writes a file per `NAT64`, named after it, in Jool's own format. The edge of
[Translate to IPv4 and resolve names](../../router/nat64-and-dns.md) renders `edge.json`:

```json
{
  "instance": "edge",
  "framework": "netfilter",
  "global": {
    "pool6": "64:ff9b::/96"
  },
  "pool4": [
    {
      "protocol": "TCP",
      "prefix": "192.0.2.8/30",
      "port range": "61001-65535"
    },
    {
      "protocol": "UDP",
      "prefix": "192.0.2.8/30",
      "port range": "61001-65535"
    },
    {
      "protocol": "ICMP",
      "prefix": "192.0.2.8/30",
      "port range": "61001-65535"
    }
  ]
}
```

Each instance runs in netfilter mode in the router's network namespace. It takes the packets to
its prefix at the prerouting hook, before the kernel routes them, and the packets to its pool's
addresses and ports on the way back. An entry of `pool4` becomes an entry per protocol. An empty
`pool4` renders an empty pool, from which Jool takes the router's own addresses and the ports
61001 to 65535.

`jool file check`, the output's check, reads a file's syntax and bounds alone. The renderer parses
every value and refuses what Jool's kernel module would refuse once the instance loads: a name of
more than 15 characters, a prefix of another length than RFC 6052's or one whose bits 64 to 71
are set, and ports of the kernel's ephemeral range, 32768 to 60999. Jool refuses that range for
any address, also one the router does not hold. The renderer also refuses two entries of one pool
that share an address, a protocol and a port, and two NAT64s with empty pools, which would share
the router's addresses.

## chalkos-nat64 brings the instances to the rendered files

`chalkos-nat64.service` runs the instances. It is a oneshot unit that starts at boot after
`chalkos-config.service` and `chalkos-firewall.service`, and before `frr.service`. Each delivery
that changes the output reloads it. A start and a reload do the same:

- `jool file check` reads every file first. A file it refuses fails the unit, and no instance
  changes.
- `jool file handle` creates each file's instance, or replaces it in one step. A replaced instance
  keeps its sessions, so a delivery that changes a pool ends no connection.
- Jool keeps an instance's prefix. An instance whose `NAT64` changed its prefix is removed first,
  and its sessions end.
- Each prefix gets an unreachable route of protocol 64, which `chalkctl node routes` names
  `nat64`. BGP announces a network only while the router's table holds a route to it, so this
  route lets `BGP.spec.networks` announce the prefix. Jool takes the prefix's traffic before the
  kernel routes it. While no instance runs, senders get ICMPv6 unreachable.
- Instances and routes that no file names are removed, those of a removed or renamed `NAT64`
  among them. Jool lists the instances of every network namespace, and the unit removes those of
  the router's own namespace alone.

A step that fails leaves the others to run, and the unit then fails with each failed step.
Stopping the unit removes every instance and route. In maintenance mode the node renders no
output, so it runs no instance. The image loads Jool's kernel module at boot, and networkd leaves
the routes of protocol 64 alone, as it leaves FRR's.

## Translated connections bypass the firewall's NAT

Jool sends a translated packet itself, past the hooks where conntrack tracks connections, so
nftables's NAT never sees it. A `NATRule` that masquerades the uplink does not translate it, and
an address of `pool4` must be the router's own or routed to it. The firewall's policies do not
filter the translated connections either. The prerouting chain's reverse-path filter runs before
Jool, and Jool takes the packets before the input and forward chains.

## The output unbound renders Unbound's whole config

`chalkos-render unbound` writes `unbound.conf` from the `Resolver`. The edge's `Resolver` renders:

```text
# Unbound's config, rendered by chalkos-render unbound from the node's Resolver.
# unbound-control reload_keep_cache applies it.
server:
  username: ""
  chroot: ""
  pidfile: ""
  directory: "/"
  do-daemonize: no
  use-syslog: no
  ip-freebind: yes
  port: 53
  interface: 2001:db8:ffff::1
  access-control: 0.0.0.0/0 refuse
  access-control: ::/0 refuse
  access-control: 2001:db8::/32 allow
  module-config: "dns64 validator iterator"
  trust-anchor-file: "/nix/store/…-dns-root-data-…/root.key"
  dns64-prefix: 64:ff9b::/96
  domain-insecure: "ipv4only.arpa."
auth-zone:
  name: "ipv4only.arpa."
  zonefile: "/nix/store/…-ipv4only.arpa.zone"
  for-downstream: no
  for-upstream: yes
  fallback-enabled: no
remote-control:
  control-enable: yes
  control-interface: /run/unbound/unbound.ctl
```

- `ip-freebind` lets Unbound bind an address before networkd has assigned it.
- The config refuses every client of both families, and then allows the ranges of `allow`. Of
  two lines of one range the later decides, so an `allow` of `::/0` opens the family.
- With `dnssec`, the validator takes the root's trust anchor of the image. An image update
  brings a new anchor, so the resolver keeps no anchor of its own in `/var`.
- Unbound does not answer `ipv4only.arpa` by itself. The auth zone, from a file of the image,
  holds RFC 8880's `192.0.0.170` and `192.0.0.171`. The iterator reads them in place of asking
  another server, and the DNS64 module synthesises the AAAA records from them. The zone is
  unsigned, so validation skips it.
- A local zone becomes a `local-zone` line and a `local-data` line per record. Unbound answers
  them before DNS64, which synthesises nothing from them.
- Forwarders become a `forward-zone` of the root.

The renderer parses every value of the document before it writes it: addresses and ranges as
addresses, names against a pattern of lower-case labels and record data by type. A record stands
between single quotes and a TXT record's text between double quotes, so a value that holds a
quote, a backslash or anything but printable ASCII is refused. The output's check runs
`unbound-checkconf` on the file in the output's sandbox, so a value that Unbound refuses refuses
the delivery before it changes anything.

The rules refuse local zones that Unbound would load and answer wrongly: a duplicate record,
which Unbound keeps one copy of, a CNAME beside another record of its name, a `redirect` zone's
record below its own name, and a zone over `ipv4only.arpa` with DNS64. The rules compare the
records in pairs, so the zones hold 150 records at most, which keeps the comparison within
chalkd's cost limit for rules.

## A delivery reloads Unbound and keeps its cache

`/etc/unbound/unbound.conf` links to the rendered file, and NixOS's `unbound.service` runs it. The
unit starts only when the node has a rendered config, after `chalkos-config.service`,
`chalkos-firewall.service` and `chalkos-nat64.service`. In maintenance mode the node has none and
runs no resolver. Two effects act on the unit:

- `unbound` reloads it when the config changes, with `unbound-control reload_keep_cache`. That
  reads the new config, binds new addresses and keeps the cache.
- `unbound-start` restarts it when a delivery adds or removes the `Resolver`, and starts it if it
  is stopped. A reload skips a unit that does not run. Once the document is gone, the unit's
  condition no longer holds and the restart leaves it stopped.

chalkd reloads the units of one delivery in one `systemctl reload`, which follows their order:
`chalkos-firewall`, then `chalkos-nat64`, then `frr` and `unbound`. So the resolver's port is open
before it answers on a new address, and a new prefix's route exists before FRR announces it.

The node's own name lookups go through systemd-resolved as on every node, not through Unbound.

## The firewall accepts DNS from the resolver's clients

The input chain of `inet chalkos` accepts DNS, UDP and TCP port 53, from the ranges of `allow` to
the addresses of `listen`, with a line per family that both hold. The edge renders:

```text
ip6 saddr { 2001:db8::/32 } ip6 daddr { 2001:db8:ffff::1/128 } udp dport { 53 } accept comment "Resolver resolver"
ip6 saddr { 2001:db8::/32 } ip6 daddr { 2001:db8:ffff::1/128 } tcp dport { 53 } accept comment "Resolver resolver"
```

These rules come after chalkd's port and the peers' ports and before the policies to `local`,
which cannot close them. [The router's firewall](firewall.md#the-input-chain) describes the
chain.

## chalkd reads the state of Jool and Unbound

`chalkctl status` shows a router's NAT64 instances and its resolver. chalkd asks FRR, Jool and
Unbound side by side, and the questions to each have 5 seconds together.

- NAT64: chalkd lists Jool's instances with `jool instance display` and keeps those that the
  output `jool` rendered a file of. For each, it reads the counters of sessions and bindings from
  `jool stats display` and sums the ports of each protocol's pool from `jool pool4 display`. An
  instance removed meanwhile is left out.
- Resolver: when `/etc/unbound/unbound.conf` leads to a rendered file, chalkd asks
  `unbound-control status`. The resolver runs when it answers.

When a question fails, the status gives its error, and the status's other parts stand.

The diagnostic `nat64-sessions` lists an instance's sessions with `jool session display`, for TCP,
UDP and ICMP. Jool shows them to `CAP_NET_ADMIN` alone, which the diagnostic's unit gets. It
needs an operator, as the sessions show who connects where.

## Limits

- The router's own connections, such as the resolver's queries, take no NAT64. Jool translates
  packets that arrive, not those the router sends.
- The firewall's policies do not limit who uses a NAT64. Routing to the prefix and the
  reverse-path filter do.
- DNS64 synthesises no AAAA record from a local zone's records.
- A pool's ports stay out of the kernel's ephemeral range, 32768 to 60999, also for addresses the
  router does not hold.
- A NAT64 whose prefix changes, or which is renamed, loses its sessions.
- The rules do not compare a resolver's DNS64 prefix with the NAT64s' prefixes.
- The local zones hold 150 records at most.
- Jool is an out-of-tree module, and the kernel marks itself tainted when it loads it. Under
  Secure Boot the module loads, as the kernel checks no module signatures. dm-verity guards the
  store that holds it.

## Related pages

- [Translate to IPv4 and resolve names](../../router/nat64-and-dns.md) configures both.
- [BGP on a router](bgp.md): announcing the prefix.
- [How a router works](overview.md): the layer's outputs and the boot order.
- [Built-ins](../../reference/builtins.md#outputs-router-images) for the outputs, their effects,
  the rules and the diagnostics.
