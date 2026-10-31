---
title: "Lease addresses with DHCP"
description: "Serve IPv4 addresses on a router's LAN links with Kea: pools, reservations, the options clients get, PXE boot files and a server per VRF"
---

# Lease addresses with DHCP

A `DHCPSubnet` document makes the router lease IPv4 addresses of one subnet on its links. Kea's
DHCPv4 server answers the clients, from pools and from reservations by MAC address, and gives them
a gateway, DNS servers, a domain and the files PXE clients boot. This page serves an office LAN,
reserves an address, boots PXE clients, serves a VRF's links and checks the leases.
[DHCP on a router](../how-it-works/router/dhcp.md) explains how chalkos runs Kea.

## Before you begin

You need a router set up as [Set up a router](set-up.md) describes, and the secrets file. Its
image must carry Kea. Router images do unless their variant sets the image option
[`chalkos.router.dhcp.enable`](../reference/image-options.md#chalkosrouterdhcpenable) to `false`.
An image without Kea does not accept `DHCPSubnet` documents: `chalkctl validate` reports that it
`does not accept kind "DHCPSubnet"`. Ask the image author for an image with DHCP.

The examples configure a LAN router `r1` with these links:

- `lan0`, the office LAN, with the address `192.0.2.1/24`.
- `vlan20`, a VLAN on `lan0` for guests, in the VRF `guest`, with the address `198.51.100.1/24`.

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
the list of names decides which links the server answers on. A link takes one subnet.

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
dhcp4 guest: running
  SUBNET  RANGE            LEASED  POOL
  guests  198.51.100.0/24  3       241
```

A server that does not run says why, such as
`dhcp4 guest: not running: kea-dhcp4@guest.service is failed`.
`chalkctl logs r1 --unit kea-dhcp4@guest.service` shows Kea's log, and
`chalkctl logs r1 --unit chalkos-dhcp.service` the log of the unit that starts the servers.

The diagnostic `dhcp-leases` lists the leases, a line each with the VRF, the subnet, the address,
the MAC address, the host name and when the lease ends. `subnet` and `mac` narrow the list. It
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

The rules of `DHCPSubnet` refuse documents at the same point.
[The built-ins' reference](../reference/builtins.md#rules-dhcpsubnet) lists each rule with its
expression:

| Message | Fix |
| --- | --- |
| `DHCPSubnet office has a pool outside its subnet` | Move the pool into the subnet, or widen the subnet. |
| `DHCPSubnet office has a reservation outside its subnet` | Reserve an address of the subnet. |
| `DHCPSubnet office has a gateway outside its subnet` | Give a gateway of the subnet. |
| `DHCPSubnet "office" spec.subnet: the subnet 2001:db8:1::/64 is no IPv4 range of 30 bits at most of addresses a DHCP server leases` | Give an IPv4 range of 30 bits at most, outside `0.0.0.0/8`, `127.0.0.0/8`, `169.254.0.0/16` and `224.0.0.0/3`. |
| `DHCPSubnet "office" spec.dns: the DNS server 2001:db8::53 is no IPv4 address` | Give the DNS servers' IPv4 addresses. |
| `DHCPSubnet "office" spec.reservations: the MAC address 52:54:00:12:34:56 has more than one reservation` | Keep one reservation of the MAC address. |
| `DHCPSubnet "office" spec.reservations: the address 192.0.2.10 is reserved more than once` | Reserve each address once. |
| `DHCPSubnet "office" spec.boot: boot names no file; give bios, uefi or ipxe, or leave boot out` | Give a file, or drop `boot`. |
| `DHCPSubnet "office" spec.boot: boot's server is no IPv4 address` | Give the TFTP server's IPv4 address. |
| `DHCPSubnet "office" spec.boot: boot's bios and uefi files load over TFTP from boot's server; give server` | Give the TFTP server's address in `server`. |

The renderer of the output `kea` checks what spans documents and the order of addresses, before
the delivery changes anything. `chalkctl validate` does not run it. A refusal fails the delivery,
and `chalkctl logs r1` shows the reason on a line that starts with `chalkos-render:`:

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
| `DHCPSubnet office and DHCPSubnet lab both serve lan0; a link takes one subnet` | Serve the link in one subnet. |
| `DHCPSubnet office and DHCPSubnet servers overlap in VRF default: 192.0.2.0/24 and 192.0.2.64/26` | Give the subnets ranges apart. |
| `DHCPSubnet office: leaseTime 999999d is longer than 4294967295 seconds, the longest Kea takes` | Shorten the lease. |

## Limits

- The router leases IPv4 addresses alone. IPv6 clients take their addresses from the router
  advertisements of the `Network` documents.
- Kea reads and answers DHCP on raw sockets, ahead of the router's firewall. A `FirewallPolicy`
  does not filter DHCP on a served link, and the firewall opens no port for it.
- The router serves its own links. It does not answer requests that a relay forwards from other
  networks.
- A link takes its address from a `Network` document. The renderer cannot check an address that
  a link takes from DHCP or that another program sets, so such a link is refused.
- The leases live in a file of each server on the node's `/var`. Two routers do not share them,
  and a reinstall that wipes `/var` forgets them.
- The servers update no DNS records for their clients.

## What next

- [DHCP on a router](../how-it-works/router/dhcp.md) explains the rendered configs, the servers
  of each VRF and the leases.
- [Translate to IPv4 and resolve names](nat64-and-dns.md) runs the resolver the clients ask.
- [Separate routing with VRFs](vrfs.md) declares the VRFs a subnet's links are in.
- [Config kinds](../reference/kinds.md#dhcpsubnet) describes each field.
- [Built-ins](../reference/builtins.md#diagnostic-dhcp-leases) describes the diagnostic
  `dhcp-leases`.
