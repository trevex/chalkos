---
title: "Set up a router"
description: "Configure a router node's interfaces, its Router document and the interfaces of its node API, deliver the config and check the result"
---

# Set up a router

This page shows how to configure a node of a router image: its interfaces, its `Router` document
and the interfaces on which it answers the node API.

## Before you begin

You need a router image, which an image author builds as
[Variants and platforms](../build/variants-and-platforms.md) describes, the secrets file, and a
node installed as [Install a node](../operate/install.md) describes. The examples use the router
`r1`.

## Configure the interfaces

A router's interfaces, addresses and static routes are networkd documents, as on any node.
[Write a node's config](../operate/node-config.md#network-documents-configure-the-interfaces)
describes them. A router `r1` with its uplink on `eth0` and a LAN on `eth1`:

```yaml
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 10-wan}
spec:
  match: {name: eth0}
  network: {address: [203.0.113.2/24, "2001:db8:ffff::2/64"], gateway: [203.0.113.1, "2001:db8:ffff::1"]}
---
apiVersion: networkd.chalkos.dev/v1alpha1
kind: Network
metadata: {name: 20-lan}
spec:
  match: {name: eth1}
  network: {address: [192.0.2.1/24, "2001:db8:1::1/64"]}
```

## Write the Router document

A router without a `Router` document forwards both families and answers the node API on every
interface. Add one to the node's file to answer it on the LAN alone:

```yaml
apiVersion: router.chalkos.dev/v1alpha1
kind: Router
metadata: {name: router}
spec:
  management: {interfaces: [eth1]}
```

The firewall then accepts chalkd's port 50000 on `eth1` alone. Name the interface that your
workstation reaches the router through, or chalkctl loses the router once the delivery is
applied. A glob, a prefix followed by `*` such as `mgmt*`, names several interfaces.

The other fields keep their defaults here:

| Field | Default | Sets |
| --- | --- | --- |
| `forwarding.ipv4`, `forwarding.ipv6` | `true` | Whether the router forwards the family. |
| `rpFilter` | `loose` | The reverse-path filter: `strict`, `loose` or `off`. |
| `ecmpHash` | `l4` | What picks one of several equal-cost routes: `l3`, the addresses, or `l4`, also the protocol and the ports. |
| `conntrackMax` | null | The size of the connection table; null keeps the kernel's. |

[Config kinds](../reference/kinds.md#router) describes each field.

## Deliver and check the config

Validate the files and deliver them as for any node:

```sh
chalkctl validate
chalkctl apply-config r1 --secrets secrets.age
```

Show the ruleset the router loaded, and the path to an address:

```sh
chalkctl node diag r1 --secrets secrets.age nft-ruleset
chalkctl node diag r1 --secrets secrets.age traceroute address=2001:db8:2::1
```

`nft-ruleset` prints the table `inet chalkos`, whose chains
[The router's firewall](../how-it-works/router/firewall.md) explains. `traceroute` prints a line
for each router on the path, 30 at most. Both need an operator client.

## Move the node API to another interface

chalkctl refuses a delivery that removes the last `Network` of a management interface, because
the router would stop configuring the interface that you reach it through. The refusal reads:

```text
chalkctl: r1: the config is refused: Router "router" spec.management.interfaces: the config leaves the management interfaces eth1 without a Network document that matches them, so the router would no longer configure them; keep a Network that matches each, or change spec.management.interfaces in the same delivery
```

To move the node API, change `spec.management.interfaces` in the delivery that changes the
`Network` documents. The connection of that delivery stays up, because the firewall accepts
established connections. The commands after it reach the router through the new interface alone.

The guard compares the names in each `Network`'s `[Match]` with the management interfaces, and a
`Network` glob such as `eth*` covers `eth1`. It catches a removed `Network`. It cannot tell
whether the interface you reach the router through is among the management interfaces, so check
that before the delivery.

## What next

- [Filter and translate traffic](firewall-and-nat.md) adds zones, policies and NAT.
- [How a router works](../how-it-works/router/overview.md) explains the outputs and the boot
  order.
