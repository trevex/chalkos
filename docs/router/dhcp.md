---
title: "Lease addresses with DHCP"
description: "Serve IPv4 and IPv6 addresses on a router's links with Kea, delegate IPv6 prefixes to other routers and relay DHCP to servers elsewhere: pools, reservations, options, PXE boot files and a server per VRF"
---

# Lease addresses with DHCP

A `DHCPSubnet` document makes the router lease addresses of one subnet on its links, IPv4 or
IPv6 by the subnet's range. Kea's DHCPv4 and DHCPv6 servers answer the clients, from pools and
from reservations by MAC address, and give them DNS servers and a domain. IPv4 clients also get a
gateway and PXE boot files, and IPv6 routers on the link may get a prefix of their own. A
`DHCPRelay` document makes the router relay its links' requests to DHCP servers elsewhere
instead. This page serves an office LAN, reserves an address, boots PXE clients, serves a VRF's
links, serves IPv6, delegates prefixes, relays requests and checks the leases.
[DHCP on a router](../how-it-works/router/dhcp.md) explains how chalkos runs Kea and dnsmasq.

## Before you begin

You need a router set up as [Set up a router](set-up.md) describes, and the secrets file. Its
image must carry Kea. Router images do unless their variant sets the image option
[`chalkos.router.dhcp.enable`](../reference/image-options.md#chalkosrouterdhcpenable) to `false`.
An image without Kea does not accept `DHCPSubnet` documents: `chalkctl validate` reports that it
`does not accept kind "DHCPSubnet"`. Ask the image author for an image with DHCP;
[Leave router features out](../build/variants-and-platforms.md#leave-router-features-out) lists
the options of every router feature.

Relays need dnsmasq, which router images carry unless their variant sets
[`chalkos.router.dhcpRelay.enable`](../reference/image-options.md#chalkosrouterdhcprelayenable)
to `false`. An image without it refuses `DHCPRelay` documents the same way.

The examples configure a LAN router `r1` with these links:

- `lan0`, the office LAN, with the addresses `192.0.2.1/24` and `2001:db8:1::1/64`.
- `vlan20`, a VLAN on `lan0` for guests, in the VRF `guest`, with the address `198.51.100.1/24`.
- `cust0`, a link to customers' home routers, in the VRF `access`, with the address
  `2001:db8:2::1/64`.

Each link the router serves needs a `Network` document that names it in `[Match] Name=` and gives
it a static address in the subnet. Kea answers a link's clients from that address.

## Serve a LAN

The `DHCPSubnet` `office` leases addresses of `192.0.2.100` to `192.0.2.199` on `lan0`:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: DHCPSubnet
metadata: {name: office}
spec:
  interfaces: [lan0]
  subnet: 192.0.2.0/24
  pools: [{first: 192.0.2.100, last: 192.0.2.199}]
  gateway: 192.0.2.1
  dns: [192.0.2.1]
  domain: lan.example.org
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-lan0}
spec:
  match: {name: lan0}
  network: {address: [192.0.2.1/24], vlan: [vlan20], ipv6AcceptRA: false}
```

The clients route through `gateway` and ask the DNS servers of `dns`, here the router itself. A
`Resolver` on `192.0.2.1` answers them, as
[Translate to IPv4 and resolve names](nat64-and-dns.md) describes. `domain` is the domain of the
clients' names, which they also search. A lease lasts 12 hours unless `leaseTime` says otherwise,
such as `1h` or `7d`.

`interfaces` names each link by its full name; it takes no glob such as `lan*`. Kea reads and
answers DHCPv4 through raw packet sockets on exactly these links, and nftables does not see that
traffic. The router's firewall opens no port for it and a `FirewallPolicy` cannot filter it, so
the list of names decides which links the server answers on. A link takes one subnet of each
family.

## Reserve addresses

A reservation gives a client the same address every time, by its MAC address:

```yaml
spec:
  interfaces: [lan0]
  subnet: 192.0.2.0/24
  pools: [{first: 192.0.2.100, last: 192.0.2.199}]
  reservations:
    - {mac: "52:54:00:12:34:56", address: 192.0.2.10, hostname: printer}
```

Write the MAC address in lower case. A reserved address lies inside the subnet and outside its
pools. Neither a pool nor a reservation may hold the router's own address on the link, the
gateway, a DNS server or the boot server. `hostname` is the name the client gets; leave it out for
none. A subnet without `pools` leases its reservations alone.

## Boot PXE clients

`boot` gives PXE clients the file to load, by their firmware. The files are served elsewhere, by
a TFTP or HTTP server:

```yaml
spec:
  interfaces: [lan0]
  subnet: 192.0.2.0/24
  pools: [{first: 192.0.2.100, last: 192.0.2.199}]
  boot:
    server: 192.0.2.5
    bios: undionly.kpxe
    uefi: ipxe.efi
    ipxe: http://192.0.2.5/boot.ipxe
```

A client with BIOS firmware, of architecture 0 in its option 93, gets `bios`. One with x86-64 UEFI
firmware, of architecture 7 or 9, gets `uefi`. Both load the file from the TFTP server at
`server`, so `bios` and `uefi` need `server`. A client that already runs iPXE says so in its user
class, option 77, and gets `ipxe` instead, here a script over HTTP. Without `ipxe`, iPXE would get
its own binary again and load it in a loop. Give the files that your clients need; a file left
out goes to no client. A `boot` with `ipxe` alone, an HTTP URL, needs no `server`.

## Serve a VRF's links

A subnet whose links are in a VRF is served by a DHCP server of that VRF, apart from the default
VRF's. The guests' subnet on `vlan20`, in the VRF `guest`, leases for an hour:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: DHCPSubnet
metadata: {name: guests}
spec:
  interfaces: [vlan20]
  subnet: 198.51.100.0/24
  pools: [{first: 198.51.100.10, last: 198.51.100.250}]
  gateway: 198.51.100.1
  dns: [198.51.100.1]
  leaseTime: 1h
---
apiVersion: router.chalkos.dev/v1alpha1
kind: VRF
metadata: {name: guest}
spec: {table: 300, interfaces: [vlan20]}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: NetDev
metadata: {name: 20-vlan20}
spec:
  netDev: {kind: vlan, name: vlan20}
  vlan: {id: 20}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-vlan20}
spec:
  match: {name: vlan20}
  network: {address: [198.51.100.1/24], ipv6AcceptRA: false}
```

A subnet's links are all in one VRF. Two subnets of one VRF do not overlap, while subnets of
different VRFs may use the same range. [Separate routing with VRFs](vrfs.md) declares the VRF.

## Serve IPv6

A subnet of an IPv6 range makes Kea's DHCPv6 server lease its addresses. The `DHCPSubnet`
`office6` leases `2001:db8:1::100` to `2001:db8:1::1ff` on `lan0` and reserves an address for the
printer:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: DHCPSubnet
metadata: {name: office6}
spec:
  interfaces: [lan0]
  subnet: 2001:db8:1::/64
  pools: [{first: "2001:db8:1::100", last: "2001:db8:1::1ff"}]
  dns: ["2001:db8:1::1"]
  domain: lan.example.org
  reservations:
    - {mac: "52:54:00:12:34:56", address: "2001:db8:1::10", hostname: printer}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-lan0}
spec:
  match: {name: lan0}
  network: {address: [192.0.2.1/24, "2001:db8:1::1/64"], vlan: [vlan20], ipv6AcceptRA: false, ipv6SendRA: true}
  ipv6SendRA: {managed: true}
  ipv6Prefix: {prefix: "2001:db8:1::/64"}
```

IPv6 clients ask a DHCPv6 server only when the router advertisements of their link tell them to.
They also learn their routers from the advertisements. The link's `Network` sends them, with
`ipv6SendRA: true` in `network`, and its `ipv6SendRA` section sets the flags. A subnet that leases
addresses, from pools or reservations, needs `managed: true`. One that gives only options or
prefixes needs `managed: true` or `otherInformation: true`. The renderer refuses a subnet whose
link's advertisements lack the flag. It also reads networkd's former names of these keys,
`ipv6PrefixDelegation` in `network` and the section `ipv6PrefixDelegation`.

An IPv6 subnet differs from an IPv4 one in these ways:

- It is served on one link: `interfaces` names one link, as Kea's DHCPv6 server picks a subnet
  by its link.
- It has no `gateway` and no `boot`. `dns` lists IPv6 addresses.
- A pool or reservation does not hold the subnet's first address, the subnet routers' anycast
  address.
- Kea learns a client's MAC address from its DUID, when it is a DUID-LL or DUID-LLT, or from an
  EUI-64 link-local address. A client with another DUID and a random link-local address does not
  match a reservation by MAC address and takes an address of the pools.
- Clients prefer their addresses for the whole `leaseTime` and renew at half of it.

A link takes one subnet of each family, so `lan0` serves `office` and `office6` side by side.

## Delegate prefixes to other routers

`prefixDelegation` gives routers on the link a prefix each to number their own networks, as a
provider gives its customers' home routers. The subnet `customers` delegates a /56 of
`2001:db8:100::/40` to each home router on `cust0` that asks, for a day:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: DHCPSubnet
metadata: {name: customers}
spec:
  interfaces: [cust0]
  subnet: 2001:db8:2::/64
  dns: ["2001:db8:2::1"]
  leaseTime: 1d
  prefixDelegation: {prefix: "2001:db8:100::/40", length: 56}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-cust0}
spec:
  match: {name: cust0}
  network: {address: ["2001:db8:2::1/64"], ipv6SendRA: true, ipv6AcceptRA: false}
  ipv6SendRA: {otherInformation: true}
```

`length` is longer than the range's prefix: here the range holds 65 536 prefixes of /56. In the
link's VRF, the range overlaps no subnet and no other delegated range. It holds none of the
router's own addresses, no destination of a `Network`'s routes and no NAT64's prefix.

The router routes each delegated prefix to the link-local address of the router that asked for
it, in the link's VRF, until its lease ends. A release or the end of the lease removes the route,
and the routes survive the router's boot.

## Relay requests to another server

A `DHCPRelay` forwards the DHCP requests of its links' clients to servers elsewhere, through the
links' VRF, and their answers back. The relay `office` relays `lan0` to a central server of each
family behind the uplink `wan0`:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: DHCPRelay
metadata: {name: office}
spec:
  interfaces: [lan0]
  servers: [203.0.113.53, "2001:db8:ff::53"]
```

DHCPv4 requests go to the IPv4 servers and DHCPv6 requests to the IPv6 ones. The relay forwards
a link's requests from the router's address of the family on the link. The server picks the
client's subnet by that address, so the link's `Network` gives the link a static address of each
family the servers have, a global one for IPv6. The servers are unicast addresses. A DHCPv6
server answers relayed requests on such an address, as Kea's does on an interface given as
`<name>/<address>`, such as `"interfaces": ["eth0/2001:db8:ff::53"]`.

A relay in a VRF reaches its servers through the VRF's routes. The relay `guests` relays DHCPv4
of `vlan20`, in the VRF `guest`, to a server on the VRF's own uplink `vlan30`, a VLAN on `wan0`:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: DHCPRelay
metadata: {name: guests}
spec:
  interfaces: [vlan20]
  servers: [203.0.113.154]
---
apiVersion: router.chalkos.dev/v1alpha1
kind: VRF
metadata: {name: guest}
spec: {table: 300, interfaces: [vlan20, vlan30]}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 31-vlan30}
spec:
  match: {name: vlan30}
  network: {address: [203.0.113.130/25], ipv6AcceptRA: false}
```

`vlan30` also needs a `NetDev` and a place in `wan0`'s `vlan` list, as `vlan20` has on `lan0`.
The example `dhcp-relay.yaml` holds the whole router.

A relay's links are in one VRF, each link takes one relay, and `interfaces` names each link,
without a glob, as a subnet's does. Relays of one family may run in any VRFs, the default one
included, as `office` and `guests` relay DHCPv4 here. A family that the router relays it does not
serve in the same VRF, nor when either the relay or the server is in the default VRF. Kea's and
dnsmasq's sockets of a family share a port, which works only when each is bound to a VRF of its
own. Relay DHCPv4 from a VRF and serve DHCPv4 in another VRF, or relay one family and serve the
other anywhere.

## Check that it worked

Deliver the documents as any config:

```sh
chalkctl validate
chalkctl apply-config r1 --secrets secrets.age
```

`chalkctl status` shows each VRF's DHCP server and, while it runs, the addresses each subnet
leased and the addresses of its pools. A reserved address counts as leased once its client took
it:

```sh
chalkctl status r1 --secrets secrets.age
```

```text
dhcp4 default: running
  SUBNET  RANGE         LEASED  POOL
  office  192.0.2.0/24  12      100
dhcp6 default: running
  SUBNET   RANGE            LEASED  POOL  DELEGATED  DELEGABLE
  office6  2001:db8:1::/64  9       256   0          0
dhcp4 guest: running
  SUBNET  RANGE            LEASED  POOL
  guests  198.51.100.0/24  3       241
dhcp6 access: running
  SUBNET     RANGE            LEASED  POOL  DELEGATED  DELEGABLE
  customers  2001:db8:2::/64  0       0     2          65536
```

A DHCPv6 server's subnets also count the prefixes they delegated and those they may delegate. A
count past 18446744073709551615, as of the addresses of a /64 pool, shows as `>=2^64`. A
router that relays shows a line for each VRF's relay, such as `dhcp-relay guest: running`.

A server or relay that does not run says why, such as
`dhcp4 guest: not running: kea-dhcp4@guest.service is failed`.
`chalkctl logs r1 --unit kea-dhcp6@access.service` shows Kea's log,
`chalkctl logs r1 --unit chalkos-dhcp.service` the log of the unit that starts the servers and
`chalkctl logs r1 --unit dnsmasq-relay@guest.service` a relay's.

The routes of delegated prefixes have the protocol `dhcp-pd`:

```sh
chalkctl node routes r1 --secrets secrets.age --vrf access --protocol dhcp-pd
```

```text
DESTINATION            TYPE     PROTOCOL  TABLE  METRIC  NEXT HOPS
2001:db8:100::/56      unicast  dhcp-pd   500    1024    fe80::5054:ff:fe12:3456 on cust0
2001:db8:100:100::/56  unicast  dhcp-pd   500    1024    fe80::5054:ff:fe65:4321 on cust0
```

The diagnostic `dhcp-leases` lists the leases, a line each with the VRF, the subnet, the address
or the delegated prefix and its length, the MAC address, or `-` when the server learned none, the
host name and when the lease ends. `subnet` and `mac` narrow the list. It
takes the operator role, as the leases show which machines are on the networks:

```sh
chalkctl node diag r1 --secrets secrets.age dhcp-leases subnet=office
```

```text
VRF	SUBNET	ADDRESS	MAC	HOSTNAME	ENDS
default	office	192.0.2.10	52:54:00:12:34:56	printer	2026-11-01T22:00:00Z
default	office	192.0.2.100	52:54:00:aa:bb:cc	-	2026-11-01T21:47:12Z
```

## Fix a refused delivery

The schema of `DHCPSubnet` takes each interface by its name, so `chalkctl validate` refuses a glob
before chalkctl sends anything:
``DHCPSubnet "office": - at '/spec/interfaces/0': 'lan*' does not match pattern '^(?:[A-Za-z0-9_.:@-]{1,15})$'``.
Name each link instead.

The rules of `DHCPSubnet` and `DHCPRelay` refuse documents at the same point, and so does the
check of the values the kinds take once, such as a reservation's MAC address.
[The built-ins' reference](../reference/builtins.md#rules-dhcpsubnet) lists each rule with its
expression:

| Message | Fix |
| --- | --- |
| `DHCPSubnet office has a pool outside its subnet` | Move the pool into the subnet, or widen the subnet. |
| `DHCPSubnet office has a reservation outside its subnet` | Reserve an address of the subnet. |
| `DHCPSubnet office has a gateway outside its subnet` | Give a gateway of the subnet. |
| `DHCPSubnet "office" spec.subnet: the subnet 192.0.2.0/31 is no IPv4 range of 30 bits at most or IPv6 range of 120 bits at most of addresses a DHCP server leases` | Give an IPv4 range of 30 bits at most, outside `0.0.0.0/8`, `127.0.0.0/8`, `169.254.0.0/16` and `224.0.0.0/3`, or an IPv6 range of 120 bits at most, outside `::/8`, `fe80::/10` and `ff00::/8`. |
| `DHCPSubnet office has a DNS server of another family than its subnet` | Give DNS servers of the subnet's family. |
| `DHCPSubnet office6 gives a gateway to an IPv6 subnet, whose clients learn their routers from router advertisements` | Drop `gateway`; send router advertisements from the link's `Network`. |
| `DHCPSubnet office6 gives PXE boot files to an IPv6 subnet` | Drop `boot`. |
| `DHCPSubnet office delegates prefixes in an IPv4 subnet` | Drop `prefixDelegation`, or delegate in an IPv6 subnet. |
| `DHCPSubnet office6 serves an IPv6 subnet on more than one link; give each link a DHCPSubnet of its own` | Name one link. |
| `DHCPSubnet "customers" spec.prefixDelegation: prefixDelegation's prefix 2001:db8:100::/56 is no IPv6 range shorter than 56 bits` | Give an IPv6 range and a longer `length`. |
| `DHCPRelay "office" spec.servers: the server ff05::1:3 is no unicast address a relay forwards to` | Name the servers by their unicast addresses. |
| `DHCPRelay/office: spec.servers[0], spec.servers[1]: "2001:db8:ff::53" is not unique; name each server once` | Name each server once. |
| `DHCPRelay/guests: spec.interfaces[0], DHCPRelay/office: spec.interfaces[1]: "lan0" is not unique; a link takes one relay` | Relay the link in one document, and name it once there. |
| `DHCPSubnet/office: spec.reservations[0].mac, spec.reservations[2].mac: "52:54:00:12:34:56" is not unique; keep one reservation of the MAC address` | Keep one reservation of the MAC address. |
| `DHCPSubnet/office: spec.reservations[1].address, spec.reservations[3].address: "192.0.2.10" is not unique; reserve each address once` | Reserve each address once. |
| `DHCPSubnet "office" spec.boot: boot names no file; give bios, uefi or ipxe, or leave boot out` | Give a file, or drop `boot`. |
| `DHCPSubnet "office" spec.boot: boot's server is no IPv4 address` | Give the TFTP server's IPv4 address. |
| `DHCPSubnet "office" spec.boot: boot's bios and uefi files load over TFTP from boot's server; give server` | Give the TFTP server's address in `server`. |

The renderers of the outputs `kea` and `dhcp-relay` check what spans documents and the order of
addresses, before the delivery changes anything. `chalkctl validate` does not run them. A refusal
fails the delivery, and `chalkctl logs r1` shows the reason on a line that starts with `chalkos-render:`:

| Message | Fix |
| --- | --- |
| `DHCPSubnet office: the pool 192.0.2.199-192.0.2.100 ends before it starts` | Swap `first` and `last`. |
| `DHCPSubnet office: the pools 192.0.2.100-192.0.2.150 and 192.0.2.150-192.0.2.199 overlap` | Give each pool addresses of its own. |
| `DHCPSubnet office: the reservation of 52:54:00:12:34:56, 192.0.2.120, lies in the pool 192.0.2.100-192.0.2.199; reserve addresses outside the pools` | Reserve an address outside the pools, or shrink the pool. |
| `DHCPSubnet office: the pool's first address, 192.0.2.0, is the network or broadcast address of the subnet 192.0.2.0/24` | Start the pool after the subnet's first address and end it before its last. |
| `DHCPSubnet office: the pool 192.0.2.10-192.0.2.50 holds 192.0.2.30, the router's own address on the subnet's links` | Keep the router's addresses out of the pools and reservations. |
| `DHCPSubnet office: the pool 192.0.2.100-192.0.2.199 holds 192.0.2.150, the gateway` | Keep the gateway out of the pools and reservations. |
| `DHCPSubnet office: the reservation of 52:54:00:12:34:56, 192.0.2.53, is a DNS server` | Keep the DNS servers and the boot server out of the pools and reservations. |
| `DHCPSubnet office: the gateway, 192.0.2.0, is the network or broadcast address of the subnet 192.0.2.0/24` | Give a gateway between the subnet's first and last address. |
| `DHCPSubnet office: Network 10-lan0 gives lan0 no static address in the subnet 203.0.113.0/24, which Kea answers its clients from` | Give the link's `Network` an address in the subnet. |
| `DHCPSubnet office: no Network document matches lan9 by Name= alone, so the renderer cannot find its address in the subnet 192.0.2.0/24; give the link one` | Add a `Network` that names the link. |
| `DHCPSubnet office: its links are in VRF default and VRF guest; a subnet is served in one VRF` | Give each VRF's links a subnet of their own. |
| `DHCPSubnet office: its links are in the VRF "lan", which no VRF document declares` | Declare the VRF with a `VRF` document, or drop `VRF=` from the link's `Network`. |
| `DHCPSubnet office: guest is the device of VRF guest; name the VRF's links` | Name the VRF's links. |
| `DHCPSubnet office: n64-edge is the link of NAT64 edge` | Leave the NAT64's link out. |
| `DHCPSubnet office and DHCPSubnet lab both serve lan0; a link takes one subnet of each family` | Serve the link in one subnet of each family. |
| `DHCPSubnet office and DHCPSubnet servers overlap in VRF default: 192.0.2.0/24 and 192.0.2.64/26` | Give the subnets ranges apart. |
| `DHCPSubnet office6 and DHCPSubnet lab6 get the same number 2387838497 in Kea; change one subnet's range` | Change the range of one IPv6 subnet, whose number is a hash of its range. |
| `DHCPSubnet office: leaseTime 999999d is longer than 4294967295 seconds, the longest Kea takes` | Shorten the lease. |
| `DHCPSubnet office6: Network 10-lan0 sends no router advertisements on lan0 with Managed=yes, which tell its clients to ask the DHCPv6 server for addresses; give it ipv6SendRA: true in network and managed: true in ipv6SendRA` | Send router advertisements with the Managed flag from the link's `Network`. |
| `DHCPSubnet customers: Network 20-cust0 sends no router advertisements on cust0 with Managed=yes or OtherInformation=yes, which tell its clients to ask the DHCPv6 server; give it ipv6SendRA: true in network and otherInformation: true in ipv6SendRA` | Send router advertisements with the OtherInformation flag. |
| `DHCPSubnet office6: the pool's first address, 2001:db8:1::, is the subnet routers' anycast address of the subnet 2001:db8:1::/64` | Start the pool after the subnet's first address. |
| `DHCPSubnet customers: prefixDelegation's prefix 2001:db8::/32 overlaps the subnet 2001:db8:2::/64` | Delegate from a range apart from the subnet. |
| `DHCPSubnet customers: prefixDelegation's prefix 2001:db8:100::/40 holds 2001:db8:100::1, the address Network 30-lan3 gives a link of VRF access` | Delegate from a range that holds none of the router's addresses in the VRF. |
| `DHCPSubnet customers: prefixDelegation's prefix 2001:db8:100::/40 holds the route of 2001:db8:100:100::/56 that Network 30-lan3 gives a link of VRF access` | Delegate from a range apart from the VRF's static routes. |
| `DHCPSubnet customers: prefixDelegation's prefix 2001:db8:100::/40 holds NAT64 edge's prefix 2001:db8:100:ff::/96 of VRF access` | Delegate from a range apart from the NAT64's prefix. |
| `the DHCPSubnet documents delegate more than 65536 prefixes, the most the router routes; delegate fewer` | Delegate from smaller ranges or delegate shorter prefixes, so that all subnets together delegate 65 536 prefixes at most. |
| `DHCPRelay office: Network 10-lan0 gives lan0 no static global IPv6 address, which the relay forwards its clients' requests from` | Give the link a static address of each family the relay's servers have. |
| `DHCPRelay office relays DHCPv4 in VRF default and DHCPSubnet lab serves DHCPv4 in VRF lab; dnsmasq's and Kea's sockets of one family bind beside each other only in two VRFs other than the default one` | Relay and serve the family in two VRFs other than the default one. |

## Limits

- Kea reads and answers DHCPv4 on raw sockets, ahead of the router's firewall. A `FirewallPolicy`
  does not filter DHCPv4 on a served link, and the firewall opens no port for it. DHCPv6 and the
  relays take UDP sockets, and the firewall opens their ports on their links alone.
- The router serves its own links. Its servers do not answer requests that another relay forwards
  from other networks, and a prefix delegated through a relay is not routed.
- A family relayed and served on one router needs two VRFs other than the default one.
- DHCPv6 reservations match a client's MAC address only when its DUID or link-local address holds
  it.
- A link takes its address from a `Network` document. The renderer cannot check an address that
  a link takes from DHCP or that another program sets, so such a link is refused.
- The leases live in a file of each server on the node's `/var`. Two routers do not share them,
  and a reinstall that wipes `/var` forgets them and the routes of their prefixes.
- Kea tells the router of a release without waiting for it to record the lease before. A release
  right after its lease can leave the prefix routed until the lease would have ended, the
  subnet's `leaseTime` at most.
- The router routes 65 536 delegated prefixes at most, of all its subnets together. The renderer
  refuses subnets that delegate more, such as two ranges of /40 that delegate /56s.
- The servers update no DNS records for their clients.

## What next

- [DHCP on a router](../how-it-works/router/dhcp.md) explains the rendered configs, the servers
  of each VRF and the leases.
- [Translate to IPv4 and resolve names](nat64-and-dns.md) runs the resolver the clients ask.
- [Separate routing with VRFs](vrfs.md) declares the VRFs a subnet's links are in.
- [Config kinds](../reference/kinds.md#dhcpsubnet) describes each field.
- [Config kinds](../reference/kinds.md#dhcprelay) describes the relay's fields.
- [Built-ins](../reference/builtins.md#diagnostic-dhcp-leases) describes the diagnostic
  `dhcp-leases`.
