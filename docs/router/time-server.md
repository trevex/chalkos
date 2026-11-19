---
title: "Serve time with NTP"
description: "Let a router answer NTP for its networks with a TimeServer, point nodes at it, serve the router's own clock without a source, and check its clients"
---

# Serve time with NTP

This page shows how a router serves time to the machines on its networks, so that nodes without
internet access, such as those of an air gap, keep their clocks right. A `TimeServer` document
lets the router's chrony answer NTP from the clients' address ranges. The router itself follows
the time servers of its own `TimeSync`, as every node does, and passes that time on.

## Before you begin

You need a router set up as [Set up a router](set-up.md) describes, and the secrets file. Its
image must accept the kind `TimeServer`. Router images do unless their modules set the image
option [`chalkos.router.ntp.enable`](../reference/image-options.md#chalkosrouterntpenable) to
`false`. An image without the time server refuses the kind: `chalkctl validate` reports that it
`does not accept kind "TimeServer"`.

The examples configure a LAN router `r1` with the addresses `192.0.2.1/24` and
`2001:db8:1::1/64` on `lan0`, whose clients are in `192.0.2.0/24` and `2001:db8:1::/64`.

## Add a time server

A router has one `TimeServer` at most:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: TimeServer
metadata: {name: ntp}
spec:
  listen: [192.0.2.1, "2001:db8:1::1"]
  allow: [192.0.2.0/24, "2001:db8:1::/64"]
```

chrony answers NTP from the clients of `allow` and ignores every other client. The firewall
accepts UDP port 123 from `allow` to the addresses of `listen` by itself, ahead of the policies
to `local`, so no `FirewallPolicy` is needed. A config whose `listen` and `allow` share no family
is refused, as the server would answer no one.

`listen` holds addresses of the router in the default VRF, where chrony runs. chrony binds one
address per family at most, so it listens on every address of the router, and the firewall alone
keeps NTP on the other addresses closed. An address that `Network` documents give only to the
links of a VRF is refused, as chrony never sees packets of a VRF's links.

The router serves plain NTP without NTS, so its clients name it with `nts: false`. The time the
router passes on is the time of its own `TimeSync`'s servers. A router that reaches none of them
answers as unsynchronised, at stratum 16, and its clients refuse that time.

chrony answers each client once every two seconds on average, with bursts of up to 16 answers,
such as those of a client that starts with `iburst`. It drops the requests beyond that, so a
misconfigured client that polls too often gets fewer answers and loads the router less.

## Serve the router's clock without a source

`localStratum` lets the router serve its own clock when it follows no time server, as chrony's
`local stratum <n> orphan`:

```yaml
spec:
  localStratum: 10
```

chrony switches to its own clock five minutes after it starts without a source, or once the
estimated error of the source it followed grows past one second. Its clients then follow the
router's clock, however far it drifts from the real time, and a router whose clock was wrong at
boot passes the error on. Give a stratum above that of every other server the clients may reach,
such as 10, so they prefer any server that has a real source.

After a boot or a restart of chronyd without a reachable source, such as a delivery that changes
the `TimeServer`, the router answers as unsynchronised for up to those five minutes. Its clients
refuse the answers meanwhile and keep their clocks running from where they stand.

A production air gap needs accurate time for certificates that other systems check, so give it a
reference clock, such as a GPS receiver with its own NTP server, and name that server in the
nodes' `TimeSync`. `localStratum` suits a lab, or keeps the nodes' clocks together while the
reference is away.

## Point the nodes at the router

Each node names the router in its `TimeSync`, with `nts: false`:

```yaml
apiVersion: time.chalkos.dev/v1alpha1
kind: TimeSync
metadata: {name: time}
spec:
  servers:
    - {host: "2001:db8:1::1", nts: false}
```

A `TimeSync` replaces the default servers, so a node with this one follows the router alone.

## Deliver and check

Validate the files and deliver them:

```sh
chalkctl validate
chalkctl config apply r1 --secrets secrets.age
```

The delivery opens the port and restarts chronyd, which reads the time server's config when it
starts. The router's clock keeps running through the restart, and chrony takes up its sources again
within seconds. A `TimeServer` removed closes the port and restarts chronyd without it.

A node that follows the router shows the router's address on the line `time:` of its status.
In the router test's lab, the node `k1` follows `r1` at `fd00:300::1`:

```sh
chalkctl status k1 --secrets secrets.age
```

```text
time: synchronised to fd00:300::1, offset -0.000017 s
```

The diagnostic `ntp-clients` lists the clients the router answered, as `chronyc clients` prints
them, followed by chrony's counts of the packets it received and dropped, as
`chronyc serverstats` prints them. `r1` of the same lab printed:

```sh
chalkctl node diag r1 --secrets secrets.age ntp-clients
```

```text
Hostname                      NTP   Drop Int IntL Last     Cmd   Drop Int  Last
===============================================================================
fd00:300::31                    3      0   1   -     1       0      0   -     -
127.0.0.1                       0      0   -   -     -       1      0   -     0

NTP packets received       : 3
NTP packets dropped        : 0
Command packets received   : 16
Command packets dropped    : 0
Client log records dropped : 0
NTS-KE connections accepted: 0
NTS-KE connections dropped : 0
Authenticated NTP packets  : 0
Interleaved NTP packets    : 0
NTP timestamps held        : 0
NTP timestamp span         : 0
NTP daemon RX timestamps   : 0
NTP daemon TX timestamps   : 3
NTP kernel RX timestamps   : 3
NTP kernel TX timestamps   : 0
NTP hardware RX timestamps : 0
NTP hardware TX timestamps : 0
```

`fd00:300::31` is `k1`, which sent three NTP requests. `NTP` counts the requests, `Drop` those
chrony dropped beyond its rate limit, and `Last` the seconds since the latest. `127.0.0.1` is
chronyc itself, whose commands chrony counts under `Cmd`. A client that does not appear sent no
request that reached chrony: check its `TimeSync`, its route to the router, and that `allow` holds
the address it sends from. A client with drops polls more often than the rate limit allows. The
diagnostic needs a reader client.

## Fix a refused delivery

The rules check each `TimeServer`, and the
[validator `router`](../reference/builtins.md#validator-router) compares its `listen` with the
`Network` and `VRF` documents. `chalkctl validate` and `chalkctl config apply` run both before
chalkctl sends anything:

| Message | Fix |
| --- | --- |
| `TimeServer ntp listens on no address of a family its clients' ranges are of, so it would answer no one; give listen and allow a family in common` | Add an address of the clients' family to `listen`, or the clients' range of that family to `allow`. |
| `TimeServer/ntp: spec.listen: the address fe80::1 is no unicast address the time server can listen on` | Give a global or unique local address of the router. |
| `TimeServer/ntp: spec.listen[0]: the address 2001:db8:3::1 of listen belongs to links of Network 40-guest in VRF guest alone, so the time server of VRF default never receives its queries; listen on an address of the VRF's links` | Listen on an address of a link in the default VRF. |

## What next

- [Config kinds](../reference/kinds.md#timeserver) describes each field of `TimeServer`.
- [Build an air-gapped cluster](../guides/air-gapped.md) gives the nodes their time inside an air
  gap.
- [Networking](../how-it-works/node/networking.md#chrony-keeps-the-clock-with-authenticated-servers)
  explains how a node keeps its clock.
