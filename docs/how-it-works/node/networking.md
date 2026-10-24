---
title: "Networking"
description: "How a node configures its interfaces with systemd-networkd, filters traffic with nftables and keeps its clock"
---

# Networking

The homelab example's control plane has one static address on its uplink. Here it also has a route
through a second router:

```yaml
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-uplink}
spec:
  match: {name: enp1s0}
  network: {address: [10.0.0.11/24], gateway: 10.0.0.1}
  route:
    - {destination: 10.1.0.0/16, gateway: 10.0.0.254}
```

A chalkos node configures its interfaces with systemd-networkd from documents like this one,
filters traffic with nftables, and keeps its clock with chrony. All three come from the node's
config and the image, and none of them needs Kubernetes.

!!! note "On the shipped variant, Kubernetes picks its addresses from these interfaces"

    A node with Kubernetes picks one address per IP family of the cluster from the addresses its
    interfaces hold, and control planes can share a VIP. flannel connects pods across nodes with
    VXLAN. [Kubernetes networking](../kubernetes/networking.md) describes all three.

## Network documents map one to one onto networkd units

A node's network is a set of config documents of three kinds,
[`Network`](../../reference/kinds.md#network), [`NetDev`](../../reference/kinds.md#netdev) and
[`Link`](../../reference/kinds.md#link). They mirror systemd-networkd's `.network`, `.netdev` and
`.link` files one to one:

- A document's `metadata.name` is the unit file's name without its suffix, so `10-uplink` becomes
  `/run/systemd/network/10-uplink.network` and networkd's order by file name holds.
- Its `spec` holds networkd's sections and keys in camelCase: `[Match]` is `match`,
  `MACAddress=` is `macAddress`, `DHCP=` is `dhcp`.
- Any section may be a list, for a section a unit repeats, such as `route` above. Any key may be a
  list, written once per element.

The schemas of these kinds are generated from the gperf tables of the image's own systemd. They
accept exactly the sections and keys its networkd accepts, so
[`chalkctl validate`](../../reference/cli/chalkctl_validate.md) refuses a misspelled key, or a value
that spans lines, before the node sees it. [Config kinds](../../reference/kinds.md#network) lists
every section with its networkd name.

Each key records its networkd spelling. From it, the built-in
[output](../../reference/builtins.md#output-networkd) `networkd` renders the units into
`/run/systemd/network`, where they take precedence over the image's. chalkd renders them for every
delivery before it changes anything, and `chalkos-config.service` renders them at every boot before
networkd starts. A changed network reaches a running node with
[`chalkctl apply-config`](../../reference/cli/chalkctl_apply-config.md), which makes networkd reload
its units.

## Ethernet interfaces without a unit get DHCP

The image brings one unit of its own, NixOS's `99-ethernet-default-dhcp`. It runs DHCP on every
physical Ethernet interface that no earlier unit matches. A node whose config holds no `Network`
document still comes up with DHCP, and a node that configures one interface keeps DHCP on
the others.

## The firewall drops what it does not accept

Everything on a node runs on nftables. The NixOS firewall is configured with
`flushRuleset = false`, so reloading it replaces its own table and leaves the tables of other
services alone. Its input chain drops what it does not accept, whatever other tables do. On every
node it accepts chalkd's port 50000, established and related connections, the loopback interface,
ICMP echo requests, ICMPv6 other than redirects and node information queries, and DHCPv6 replies
to link-local addresses. A reverse-path check drops packets from a source address the node has no
route back to.
[Ports and firewall](../../reference/ports-and-firewall.md) lists each port and who needs it.

!!! note "On the shipped variant, Kubernetes opens more ports"

    A node with Kubernetes also accepts the kubelet's port, the NodePort range and VXLAN sent to its
    own addresses. A control plane accepts the API server's and etcd's ports.
    [Kubernetes networking](../kubernetes/networking.md#the-control-plane-ports-open-only-on-control-planes)
    explains how.

## chrony keeps the clock with authenticated servers

A node keeps its clock with chrony. By default it synchronises with the
Physikalisch-Technische Bundesanstalt's servers `ptbtime1.ptb.de` to `ptbtime3.ptb.de`,
authenticated with NTS. A [`TimeSync`](../../reference/kinds.md#timesync) document names other
servers, each with NTS unless `nts: false`:

```yaml
apiVersion: time.chalkos.dev/v1alpha1
kind: TimeSync
metadata: {name: default}
spec:
  servers:
    - {host: nts.example.org}
    - {host: ntp.example.org, nts: false}
```

The built-in output `chrony` renders the servers into a sources file that chrony reads, so one
image serves nodes with different servers. `dhcp: true` adds the servers that DHCP announces,
which are not authenticated. chrony keeps its NTS cookies on VAR, so after a reboot it needs no new
key exchange with each server. A node runs no NTP server for other machines.

The clock decides which certificates a node accepts, as
[The clock decides whether a certificate is valid](certificates.md#the-clock-decides-whether-a-certificate-is-valid)
explains.

## Limits

- The network kinds accept the keys of the image's own networkd. A key that only a newer systemd
  knows is refused until the image carries that systemd.
- The firewall opens only the ports chalkos's modules open. A service a variant adds opens its
  ports in the variant's NixOS modules.
- Time servers that DHCP announces are not authenticated.

## Related pages

- [Kubernetes networking](../kubernetes/networking.md): node addresses, dual stack, the VIP and
  flannel.
- [Config and the inventory](config.md): how the network documents reach a node.
- [Write a node's config](../../operate/node-config.md#network-documents-configure-the-interfaces)
  for bonds, VLANs and static addresses.
- [Ports and firewall](../../reference/ports-and-firewall.md).
- [Config kinds](../../reference/kinds.md) for every field of `Network`, `NetDev`, `Link` and
  `TimeSync`.
