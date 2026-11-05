---
title: "DHCP on a router"
description: "How a router image leases addresses with Kea and relays DHCP with dnsmasq: the configs the outputs render per VRF and family, the sockets and the firewall, chalkos-dhcp and the servers of each VRF, the routes of delegated prefixes, the relays, the lease files and the state chalkd reads"
---

# DHCP on a router

A router image leases addresses with Kea's DHCPv4 server, `kea-dhcp4`, and its DHCPv6 server,
`kea-dhcp6`, and relays DHCP with dnsmasq. The output `kea` renders Kea's whole config of the
`DHCPSubnet` documents, one config per VRF and family whose links they serve. The default VRF's
servers run as `kea-dhcp4.service` and `kea-dhcp6.service`, and each other VRF's as
`kea-dhcp4@<vrf>.service` and `kea-dhcp6@<vrf>.service`. `chalkos-dhcp.service` starts, reloads
and stops them. The image carries Kea without its MySQL, PostgreSQL and Kerberos backends, its
hooks other than the lease commands and run_script, and its Python shell, about 8 MiB of the
image's compressed store. The output `dhcp-relay` renders a config of dnsmasq per VRF whose links
`DHCPRelay` documents relay, which `dnsmasq-relay.service` and `dnsmasq-relay@<vrf>.service` run.

The image option
[`chalkos.router.dhcp.enable`](../../reference/image-options.md#chalkosrouterdhcpenable), `true` by
default, decides whether a router image carries DHCP. An image built with it `false` has no Kea,
no output `kea`, no effect `dhcp`, no servers or `chalkos-dhcp.service`, no diagnostic
`dhcp-leases` and no kind `DHCPSubnet`, so its nodes refuse `DHCPSubnet` documents. Its build
then needs no local build of Kea, which the binary cache does not hold in this form.

[`chalkos.router.dhcpRelay.enable`](../../reference/image-options.md#chalkosrouterdhcprelayenable),
`true` by default, decides whether a router image carries the relay. An image built with it
`false` has no dnsmasq, no output or effect `dhcp-relay`, no relays or
`chalkos-dhcp-relays.service`, no user `dnsmasq` and no kind `DHCPRelay`. dnsmasq comes from the
binary cache and adds about 0.3 MiB to the image's compressed store.

## The output kea renders a server per VRF

`chalkos-render kea` writes `kea-dhcp4.json` and `kea-dhcp6.json` for the default VRF and
`kea-dhcp4-<vrf>.json` and `kea-dhcp6-<vrf>.json` for each VRF that a subnet's links are in, a
file for each family it serves there. For the office of
[Lease addresses with DHCP](../../router/dhcp.md) it writes, among others, these parts of
`kea-dhcp4.json`:

```json
{
  "Dhcp4": {
    "interfaces-config": {
      "dhcp-socket-type": "raw",
      "interfaces": ["lan0"],
      "service-sockets-max-retries": 4294967295,
      "service-sockets-retry-wait-time": 5000
    },
    "control-sockets": [{"socket-name": "/run/kea/dhcp4.sock", "socket-type": "unix"}],
    "lease-database": {"lfc-interval": 3600, "name": "/var/lib/kea/dhcp4.leases", "persist": true, "type": "memfile"},
    "hooks-libraries": [{"library": "libdhcp_lease_cmds.so"}],
    "subnet4": [
      {
        "id": 3221225984,
        "subnet": "192.0.2.0/24",
        "pools": [{"pool": "192.0.2.100 - 192.0.2.199"}],
        "option-data": [
          {"data": "192.0.2.1", "name": "routers"},
          {"data": "192.0.2.1", "name": "domain-name-servers"},
          {"data": "lan.example.org", "name": "domain-name"},
          {"data": "lan.example.org", "name": "domain-search"}
        ],
        "reservations": [{"hostname": "printer", "hw-address": "52:54:00:12:34:56", "ip-address": "192.0.2.10"}],
        "user-context": {"name": "office"},
        "valid-lifetime": 43200
      }
    ]
  }
}
```

Each subnet's `interfaces` are the links it is served on, by their full names, each once. The
schema refuses a glob. Kea reads and answers DHCPv4 through raw packet sockets on exactly these
links, which nftables does not see, so the firewall cannot narrow what a glob would catch. A link
that a later `Network` adds would be served without anyone naming it. The renderer finds each
link's VRF as the other router outputs do, from the `VRF` documents and the `Network` documents'
`VRF=`, and gives each VRF a config of its own. A VRF that a `Network`'s `VRF=` names and no `VRF`
document declares gets no server: the renderer refuses the subnet.

Kea numbers each subnet. The renderer takes the subnet's first address as the number, here
192.0.2.0 as 3221225984. No two subnets of a VRF overlap, so the number is unique within a
server, and a subnet keeps its number, and its leases, while other documents come and go or the
document is renamed. `user-context` names the document, which chalkd and the diagnostic
`dhcp-leases` show.

An IPv6 subnet's number is a hash of its range, FNV-1a of its address and length, as its first
address has 128 bits. The renderer refuses two subnets of one server with the same number, which
a change of either range resolves.

The kind's rules are rules of a subnet's spec, which see one document at a time, so they compare
a subnet's lists with each other within their cost. They refuse a pool that ends before it
starts or shares addresses with another, and a reservation in a pool. A pool or reservation that
holds the subnet's network or broadcast address, or an IPv6 subnet's first address, the
subnet-router anycast address, is refused, and so is one that holds the gateway, a DNS server or
the boot server, as Kea would lease a client an address that a server of its clients uses. A
gateway at the subnet's network or broadcast address is refused too.

The renderer refuses what spans documents, which no rule of one document sees: subnets that
overlap in a VRF, a link of two subnets, links of two VRFs, and an interface of a VRF's device or
a NAT64's link. It also refuses a link without a static address in the subnet, from its
`Network`, as Kea answers a link's clients from that address and serves no link without one, and
a pool or reservation that holds that address. `chalkctl validate` does not run these checks; a
delivery and `chalkctl apply-config --dry-run` do.

The output's check runs `kea-dhcp4 -t` or `kea-dhcp6 -t` on each config. Kea refuses a config
that names a link the system lacks, and the check runs in a network namespace of its own, so it
checks a copy without the links. Kea's check refuses a pool that runs backwards, overlaps another
or leaves the subnet, and a reservation outside the subnet. The renderer guards, with an internal
error, what Kea's check passes and the renderer cannot write: an address that does not parse or
has a zone, and a domain with a comma, which Kea's option data reads as more values. The schema
and the rules refuse each of these first.

## PXE files are client classes of the subnet

A subnet's `boot` becomes client classes that the subnet evaluates once Kea has chosen it:

```json
"client-classes": [
  {"name": "office-ipxe", "only-in-additional-list": true, "test": "substring(option[77].hex, 0, 4) == 'iPXE'", "boot-file-name": "http://192.0.2.5/boot.ipxe", "next-server": "192.0.2.5"},
  {"name": "office-uefi", "only-in-additional-list": true, "test": "not member('office-ipxe') and (option[93].hex == 0x0007 or option[93].hex == 0x0009)", "boot-file-name": "ipxe.efi", "next-server": "192.0.2.5"},
  {"name": "office-bios", "only-in-additional-list": true, "test": "not member('office-ipxe') and option[93].hex == 0x0000", "boot-file-name": "undionly.kpxe", "next-server": "192.0.2.5"}
]
```

The subnet lists them in `evaluate-additional-classes`. A class sets the DHCP header's file and
next server fields. A client whose user class, option 77, starts with `iPXE` gets the iPXE file,
and the UEFI and BIOS classes leave it out, so iPXE does not load its own binary again. A class of
one subnet applies to that subnet's clients alone.

## Kea's raw sockets come before the firewall

Kea reads DHCP requests on a raw socket of each link and sends the replies through it. Raw
sockets take a link's frames before the kernel's IP stack and nftables see them, and frames
sent through them pass no netfilter hook. A client without an address can therefore ask and get
an answer, and the router's firewall needs no port for DHCPv4: the table `inet chalkos` has no
line for it, and a `FirewallPolicy` of a zone to `local` does not filter it. The subnets'
`interfaces` alone decide which links get DHCP, which is why each names its links one by one.

Kea also binds a UDP socket to port 67 of each link's address. The input chain drops what
reaches that socket, and Kea answers through the raw socket regardless.

## DHCPv6 takes UDP sockets through the firewall

Kea's DHCPv6 server reads requests on UDP sockets on port 547: on each link's link-local address
and the multicast group `ff02::1:2`, and on `ff05::1:3`. It answers from the link-local address
to the client's port 546. Unlike DHCPv4, the requests pass the router's firewall, so the output
`router-nftables` opens port 547 from link-local sources on each IPv6 subnet's link, in its VRF:

```text
iifname "access" meta sdifname { "cust0" } ip6 saddr { fe80::/10 } udp dport { 547 } accept comment "DHCPSubnet customers"
iifname { "lan0" } ip6 saddr { fe80::/10 } udp dport { 547 } accept comment "DHCPSubnet office6"
```

The answers leave through the output hook, which the table does not filter. A DHCPv6 subnet names
its link in Kea's config, as Kea's DHCPv6 server picks a subnet by the link a request came in on,
so an IPv6 `DHCPSubnet` takes one link.

## Router advertisements send clients to the server

An IPv6 client asks a DHCPv6 server when its link's router advertisements carry the Managed flag,
for addresses, or the OtherInformation flag, for options. networkd sends advertisements on a link
whose `Network` has `[Network] IPv6SendRA=yes`, with the flags of its `[IPv6SendRA]` section. The
renderer reads the `Network` that networkd picks for the subnet's link as networkd reads it: the
last value of `IPv6SendRA=`, `Managed=` and `OtherInformation=` that parses as a boolean, in any
section of the name, each `no` by default. networkd also reads the former names
`IPv6PrefixDelegation=` and `[IPv6PrefixDelegation]`, which the unit lists before the current
ones, so a valid value under a current name wins. The renderer refuses a subnet with pools or
reservations whose link's advertisements lack the Managed flag, and one of options or prefixes
alone that lacks both flags.

## Delegated prefixes become routes

Kea delegates prefixes but routes none of them. A delegated prefix needs a route on the router to
the router that asked for it, at its link-local address on the link. Kea's run_script hook,
loaded by a DHCPv6 server whose subnets delegate prefixes, runs `chalkos-dhcp6-pd` as the user
`kea` on each committed, released, declined and expired lease. For a committed prefix it writes a
record in `/var/lib/chalkos-dhcp6-pd`, named `<link>_<prefix>_<length>`, readable by every user.
The record holds the address the request came from and when the lease ends. The hook writes it
in `/var/lib/chalkos-dhcp6-pd.new` first and renames it into place, so the record appears whole
and once. A release or an expiry removes it. `kea` owns both directories, the records' of mode
0755, but cannot write `/var/lib`, so it cannot put another directory in their place. Kea runs
only scripts of the directory that the unit's `KEA_HOOK_SCRIPTS_PATH` names, which holds
`chalkos-dhcp6-pd` alone.

Kea runs the script without waiting for it. A release that comes right after its commit may run
first and leave the commit's record behind. The route of the released prefix then lasts until
the lease would have ended, the subnet's lease time at most.

`chalkos-dhcp6-routes.service` runs `chalkd router dhcp6-routes` as root with `CAP_NET_ADMIN`
alone, for 30 seconds at most, with no limit on how often it starts. A `.path` unit starts it
when a record changes. A change during a run starts no other, so a timer starts the unit every
minute as well, which also routes again the prefixes of a link that went down and came back. It
runs at boot after `chalkos-dhcp.service`, which starts it after each run too. It routes each
record whose prefix a rendered config delegates on the record's link, of the pool's length, to a
link-local address, while its lease lasts:

```text
2001:db8:100::/56 via fe80::5054:ff:fe12:3456 dev cust0 table 500 proto 65 expires 86394sec
```

The route takes protocol 65, which `chalkctl node routes` names `dhcp-pd`. It expires with the
lease, and no later than the subnet's lease time from the run, so the kernel removes it should the
lease end without a renewal. chalkd removes the IPv6 routes of protocol 65 of no record, so the
routes of a config that drops its IPv6 subnets go at the next run. Without the output `kea`, as in
maintenance mode, a run changes nothing.

chalkd removes no record and trusts none, as `kea` writes them. It opens the directory and each
record without following links and refuses a directory that every user may write. It reads
regular files alone, without waiting for a writer, and 128 bytes of each. It routes 65 536 records
that have not ended at most and, beyond those, removes no route. The renderer keeps the subnets
within that limit: it refuses subnets that delegate more than 65 536 prefixes altogether, in all
VRFs. A record that ended counts toward no limit, but stays in the directory, as chalkd removes
none; the hook leaves one behind only when it misses an expiry. The records live on `/var`, so a
boot routes the prefixes again before their routers renew them. A server facing the LAN never changes routes itself: chalkd
checks each record against the configs before it routes it.

A route of a delegated prefix replaces a route of the same prefix and shadows longer ones in the
VRF's table. So the renderer refuses a delegated range that holds one of the router's addresses
on the VRF's links, the destination of a `Network`'s route there or a NAT64's prefix in the VRF.

## Relays run dnsmasq

`chalkos-render dhcp-relay` writes `dnsmasq-relay.conf` for the default VRF and
`dnsmasq-relay-<vrf>.conf` for each VRF that a relay's links are in:

```text
port=0
bind-dynamic
no-resolv
no-hosts
quiet-dhcp
quiet-dhcp6
interface=lan0
dhcp-relay=192.0.2.1,203.0.113.53
dhcp-relay=2001:db8:1::1,2001:db8:ff::53
```

`port=0` turns dnsmasq's DNS off, and the config holds no range, so dnsmasq relays and serves
nothing. Each `dhcp-relay` line relays the requests of the link that holds the first address to
the server, and the server tells the client's subnet by that address. dnsmasq reads the lines at
start alone. The effect `dhcp-relay` reloads `chalkos-dhcp-relays.service` when `dhcp-relay` or
`networkd` changes, and that unit restarts the relays that run, starts those that do not and
stops those without a config. A relay keeps no state and finds its links' addresses as requests
come, so it starts before its links exist. A VRF's relay waits up to 30 seconds for the VRF's
device.

dnsmasq binds `0.0.0.0:67` and `[::]:547`. `bind-dynamic` gives the sockets `SO_REUSEADDR`, so
the relays of one family in the default VRF and in another VRF bind beside each other, and each
relays its own links alone. Unlike `bind-interfaces`, `bind-dynamic` lets a relay start before its
links exist. The firewall opens the ports on the relays' links:

```text
iifname { "lan0" } ip saddr { 0.0.0.0/0 } udp dport { 67 } accept comment "DHCPRelay office"
iifname { "lan0" } ip6 saddr { fe80::/10 } udp dport { 547 } accept comment "DHCPRelay office"
```

The servers answer the address the relay sent from, so their answers pass as replies to the
relay's requests. A VRF's relay, `dnsmasq-relay@<vrf>.service`, is bound to the VRF's device with
`BindNetworkInterface=`, as Kea's servers of a VRF are, and records the device's index in
`/run/dnsmasq-relay/<vrf>.ifindex`.

Each relay runs as the user `dnsmasq` with `CAP_NET_BIND_SERVICE`, `CAP_NET_RAW` and
`CAP_NET_ADMIN`, which dnsmasq demands of a relay: a DHCPv6 relay opens the ICMPv6 socket of
router advertisements, and a DHCPv4 relay writes the ARP entry of a client without an address to
answer it. dnsmasq keeps the same capabilities when it starts as root anywhere else.

## A family's server and relay share no VRF

Kea's sockets and dnsmasq's of a family use the same port, and dnsmasq binds the wildcard
addresses. For DHCPv4, Kea binds a UDP socket to port 67 of each link's address without
`SO_REUSEADDR`. That socket and dnsmasq's bind side by side only when each is bound to the device
of a different VRF, and a socket of the default VRF is bound to none. For DHCPv6, Kea's sockets on
port 547 and dnsmasq's would bind side by side. In one VRF, each would then take some of the
link's requests, so a request could reach the relay instead of the server or the reverse.

So the output `dhcp-relay` refuses a family that a `DHCPRelay` relays and a `DHCPSubnet` serves in
one VRF, or when one of the two is in the default VRF, for both families alike. Two relays bind
side by side in any two VRFs, as their sockets take `SO_REUSEADDR`, and so do two DHCPv6 servers.
Different families never collide.

## chalkos-dhcp runs the servers

Kea refuses a config that names a link the system lacks, at start and at a reload, and stops in
the second case. So no server starts by itself at boot.
`chalkos-dhcp.service`, a oneshot unit after `chalkos-config.service`, `chalkos-vrf.service`,
`systemd-networkd.service` and `chalkos-firewall.service`, runs the servers of both families, and
the effect `dhcp` reloads it whenever the output `kea` or `networkd` changes. A start and a reload
do the same:

1. It stops and resets the servers that have no config, so that a subnet moved to another VRF
   leaves its links before that VRF's server starts on them. A router that serves no subnet has
   no config and runs no server, the default VRF's included.
2. For each rendered config, it waits up to 30 seconds altogether for the config's links and its
   VRF's device to exist, as networkd creates VLANs and VRFs without waiting. A server whose links
   do not appear is neither started nor reloaded, and the unit fails naming it:
   `chalkos-dhcp: failed to run kea-dhcp4@guest.service, whose links vlan20 have no device`.
3. It starts a server that does not run and reloads one that does. A reload sends Kea `SIGHUP`,
   and Kea reads its config again and keeps its leases. A VRF's server whose VRF chalkos-vrf
   removed and networkd created anew restarts instead, as described below.
4. It waits up to 30 seconds for the servers it started to run.
5. It starts `chalkos-dhcp6-routes.service`, which routes the delegated prefixes of the configs
   it now has.

`stop` stops every server. In maintenance mode the node renders no output, so it runs no server.

Kea opens the sockets of a link that is down or has no address yet once it is up, trying every
5 seconds for as long as it runs. A server that stops by itself starts again every 5 seconds.

## The servers of the VRFs

A VRF's servers are `kea-dhcp4@<vrf>.service` and `kea-dhcp6@<vrf>.service`, the instance name
escaped as `systemd-escape` does. A DHCPv4 server's raw sockets read and send on the VRF's links,
but its UDP socket binds the links' addresses, which the kernel finds in the VRF's table alone.
systemd binds the unit's IP sockets to the VRF's device with `BindNetworkInterface=`, as
`unbound@` does for a VRF's resolver; without it, Kea cannot bind the address and fails. The
unit requires the device's unit and records the device's index in `/run/kea/dhcp4-<vrf>.ifindex`
or `/run/kea/dhcp6-<vrf>.ifindex` before Kea starts. A VRF that networkd made anew has a new
index, so chalkos-dhcp restarts its server instead of reloading it.

Every server runs as the user `kea` with NixOS's usual sandbox: a read-only system, private
devices and temporary files, and no other address families than Unix, netlink, IPv4 and IPv6. A
DHCPv4 server also has `CAP_NET_RAW` and packet sockets, for its raw sockets, and
`CAP_NET_BIND_SERVICE`, for port 67. A DHCPv6 server has `CAP_NET_BIND_SERVICE` alone, for port
547, and may write `/var/lib/chalkos-dhcp6-pd`, the records of its delegated prefixes.

## Leases live in /var/lib/kea

Kea takes control sockets in `/run/kea` and lease files in `/var/lib/kea` alone. The servers share
both directories, each with names of its own:

| File | Server of the default VRF | Server of a VRF |
| --- | --- | --- |
| Control socket | `/run/kea/dhcp4.sock`, `/run/kea/dhcp6.sock` | `/run/kea/dhcp4-<vrf>.sock`, `/run/kea/dhcp6-<vrf>.sock` |
| Leases | `/var/lib/kea/dhcp4.leases`, `/var/lib/kea/dhcp6.leases` | `/var/lib/kea/dhcp4-<vrf>.leases`, `/var/lib/kea/dhcp6-<vrf>.leases` |

`/run/kea` stays when a server stops, so the other servers keep their sockets. Both directories
admit the user and group `kea` and root. The lease file is a CSV file that Kea appends each lease
to, and every hour `kea-lfc` rewrites it with the current leases alone. `/var` survives reboots
and upgrades, so a lease outlives both.

The DHCPv6 servers share their server identifier, a DUID that Kea keeps in
`/var/lib/kea/kea-dhcp6-serverid`. DHCPv6 asks for one identifier per server, and the router's
servers serve different links. The records of delegated prefixes live apart from the leases, in
`/var/lib/chalkos-dhcp6-pd`, as
[Delegated prefixes become routes](#delegated-prefixes-become-routes) describes.

## chalkd reads the servers' state

For each rendered config, chalkd asks systemd whether the server's unit is active and, for a
VRF's server, whether it is bound to the VRF's current device. It then sends Kea
`statistic-get-all` on the control socket and reads each subnet's leased and pool addresses, by
the subnet's number, which the rendered config maps to its document. The questions to all servers
share a timeout of 5 seconds. `chalkctl status` prints a server per VRF:

```text
dhcp4 default: running
  SUBNET  RANGE         LEASED  POOL
  office  192.0.2.0/24  12      100
```

A DHCPv6 server's counts are its subnets' addresses (`assigned-nas`, `total-nas`) and prefixes
(`assigned-pds`, `total-pds`). Kea counts them in 128 bits; chalkd gives a count past 64 bits as
the largest number of 64 bits, which chalkctl prints as `>=2^64`. For each rendered config of
`dhcp-relay`, chalkd asks systemd whether the relay's unit is active and, for a VRF's relay,
bound to the VRF's current device:

```text
dhcp6 default: running
  SUBNET   RANGE            LEASED  POOL  DELEGATED  DELEGABLE
  office6  2001:db8:1::/64  9       256   0          0
dhcp-relay guest: running
```

The diagnostic `dhcp-leases` runs as a member of the group `kea`. It asks each server for its
config and its leases with `config-get` and `lease4-get-all` or `lease6-get-all` by the server's
family, commands of the lease commands' hook, and prints a line per lease. Kea logs each
command on the control socket at a level its config keeps out of the journal.

## Limits

- Kea serves its own links. Requests that another relay forwards from other networks reach no
  subnet of its servers, and a prefix delegated through a relay is not routed.
- A family relayed and served on one router needs two VRFs other than the default one.
- The renderer finds a link's address and router advertisements in its `Network` document alone.
- Kea updates no DNS records and shares its leases with no other server.

## Related pages

- [Lease addresses with DHCP](../../router/dhcp.md) writes the documents.
- [VRFs on a router](vrfs.md) describes how links join a VRF.
- [The router's firewall](firewall.md) describes the table that Kea's raw sockets come before.
- [NAT64 and DNS64 on a router](nat64-and-dns.md) describes `unbound@`, which binds a VRF's
  resolver as `kea-dhcp4@` binds its server.
- [Ports and firewall](../../reference/ports-and-firewall.md) lists the ports of DHCP and the
  relays.
