---
title: "DHCP on a router"
description: "How a router image leases addresses with Kea: the config the output kea renders per VRF, the raw sockets ahead of the firewall, chalkos-dhcp and the servers of each VRF, the lease files and the state chalkd reads"
---

# DHCP on a router

A router image leases IPv4 addresses with Kea's DHCPv4 server, `kea-dhcp4`. The output `kea`
renders Kea's whole config of the `DHCPSubnet` documents, one config per VRF whose links they
serve. The default VRF's server runs as `kea-dhcp4.service` and each other VRF's as
`kea-dhcp4@<vrf>.service`. `chalkos-dhcp.service` starts, reloads and stops them. The image
carries Kea without its MySQL, PostgreSQL and Kerberos backends, its other hooks and its Python
shell, about 8 MiB of the image's compressed store.

The image option
[`chalkos.router.dhcp.enable`](../../reference/image-options.md#chalkosrouterdhcpenable), `true` by
default, decides whether a router image carries DHCP. An image built with it `false` has no Kea,
no output `kea`, no effect `dhcp`, no servers or `chalkos-dhcp.service`, no diagnostic
`dhcp-leases` and no kind `DHCPSubnet`, so its nodes refuse `DHCPSubnet` documents. Its build
then needs no local build of Kea, which the binary cache does not hold in this form.

## The output kea renders a server per VRF

`chalkos-render kea` writes `kea-dhcp4.json` for the default VRF and `kea-dhcp4-<vrf>.json` for
each VRF that a subnet's links are in. For the office of
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

Each subnet's `interfaces` are the links it is served on, by their full names. The schema and the
renderer refuse a glob. Kea reads and answers DHCPv4 through raw packet sockets on exactly these
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

The renderer refuses what spans documents or takes an order of addresses, which the kind's rules
cannot check within their cost: subnets that overlap in a VRF, a link of two subnets, links of two
VRFs, an interface of a VRF's device or a NAT64's link, and a pool that ends before it starts or
overlaps another. It also refuses a link without a static address in the subnet, from its
`Network`, as Kea answers a link's clients from that address and serves no link without one. A
pool or reservation that holds the subnet's network or broadcast address is refused, and so is
one that holds the router's own address, the gateway, a DNS server or the boot server, as Kea
would lease a client an address that a server of its clients uses. A gateway at the subnet's
network or broadcast address and a reservation in a pool are refused too.

The output's check runs `kea-dhcp4 -t` on each config. Kea refuses a config that names a link
the system lacks, and the check runs in a network namespace of its own, so it checks a copy
without the links.

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

## chalkos-dhcp runs the servers

Kea refuses a config that names a link the system lacks, at start and at a reload, and stops in
the second case. So no server starts by itself at boot.
`chalkos-dhcp.service`, a oneshot unit after `chalkos-config.service`, `chalkos-vrf.service`,
`systemd-networkd.service` and `chalkos-firewall.service`, runs them, and the effect `dhcp`
reloads it whenever the output `kea` or `networkd` changes. A start and a reload do the same:

1. For each rendered config, it waits up to 30 seconds altogether for the config's links and its
   VRF's device to exist, as networkd creates VLANs and VRFs without waiting. A server whose links
   do not appear is neither started nor reloaded, and the unit fails naming it:
   `chalkos-dhcp: failed to run kea-dhcp4@guest.service, whose links vlan20 have no device`.
2. It starts a server that does not run and reloads one that does. A reload sends Kea `SIGHUP`,
   and Kea reads its config again and keeps its leases.
3. A VRF's server whose VRF chalkos-vrf removed and networkd created anew restarts, as described
   below.
4. It waits up to 30 seconds for the servers it started, and stops and resets the servers that
   have no config.

`stop` stops every server. In maintenance mode the node renders no output, so it runs no server.

Kea opens the sockets of a link that is down or has no address yet once it is up, trying every
5 seconds for as long as it runs. A server that stops by itself starts again every 5 seconds.

## The servers of the VRFs

A VRF's server is `kea-dhcp4@<vrf>.service`, the instance name escaped as `systemd-escape` does.
Its raw sockets read and send on the VRF's links, but its UDP socket binds the links' addresses,
which the kernel finds in the VRF's table alone. systemd binds the unit's IP sockets to the VRF's
device with `BindNetworkInterface=`, as `unbound@` does for a VRF's resolver; without it, Kea
cannot bind the address and fails. The unit requires the device's unit and records the device's
index in `/run/kea/dhcp4-<vrf>.ifindex` before Kea starts. A VRF that networkd made anew has a
new index, so chalkos-dhcp restarts its server instead of reloading it.

Every server runs as the user `kea` with `CAP_NET_RAW`, for its raw sockets, and
`CAP_NET_BIND_SERVICE`, for port 67, alone, with NixOS's usual sandbox: a read-only system,
private devices and temporary files, and no other address families than Unix, netlink, IPv4,
IPv6 and packet sockets.

## Leases live in /var/lib/kea

Kea takes control sockets in `/run/kea` and lease files in `/var/lib/kea` alone. The servers share
both directories, each with names of its own:

| File | Server of the default VRF | Server of a VRF |
| --- | --- | --- |
| Control socket | `/run/kea/dhcp4.sock` | `/run/kea/dhcp4-<vrf>.sock` |
| Leases | `/var/lib/kea/dhcp4.leases` | `/var/lib/kea/dhcp4-<vrf>.leases` |

`/run/kea` stays when a server stops, so the other servers keep their sockets. Both directories
admit the user and group `kea` and root. The lease file is a CSV file that Kea appends each lease
to, and every hour `kea-lfc` rewrites it with the current leases alone. `/var` survives reboots
and upgrades, so a lease outlives both.

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

The diagnostic `dhcp-leases` runs as a member of the group `kea`. It asks each server for its
config and its leases with `config-get` and `lease4-get-all`, a command of the lease commands'
hook, and prints a line per lease. Kea logs each command on the control socket at a level its
config keeps out of the journal.

## Limits

- The router serves DHCPv4 alone.
- Kea serves its own links. Requests that a relay forwards from other networks reach its UDP
  socket, and the input chain drops them.
- The renderer finds a link's address in its `Network` document alone.
- Kea updates no DNS records and shares its leases with no other server.

## Related pages

- [Lease addresses with DHCP](../../router/dhcp.md) writes the documents.
- [VRFs on a router](vrfs.md) describes how links join a VRF.
- [The router's firewall](firewall.md) describes the table that Kea's raw sockets come before.
- [NAT64 and DNS64 on a router](nat64-and-dns.md) describes `unbound@`, which binds a VRF's
  resolver as `kea-dhcp4@` binds its server.
