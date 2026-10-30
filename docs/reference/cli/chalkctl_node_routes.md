---
title: "chalkctl node routes"
description: "Show a router's routes"
---

# chalkctl node routes

```sh
chalkctl node routes <node> [<prefix>] [flags]
```

Shows the routes of a node of a router image: the routes in its kernel, of the main table unless
--vrf names a VRF, whose table it shows, or --table names another table or all, and the paths
the BGP instance of that VRF selected, the best of a prefix marked *. A path the instance
imports from another VRF names that VRF as its peer. With a prefix, it shows the routes to that
range and to ranges within it; with --protocol, the kernel's routes of that protocol alone, and
BGP's paths only for bgp. --vrf and --table exclude each other. A node of another image answers
that the router layer is not in its image. The command needs a reader client file or the
secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--protocol`{ #flag-protocol } | string | — | Only the kernel's routes of this protocol, such as kernel, static, ra, dhcp or bgp. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--table`{ #flag-table } | string | main | The kernel's routing table: main, local, default, all or a number. |
| `--vrf`{ #flag-vrf } | string | — | A VRF, by the name of its VRF document, or default: its table and its BGP instance's paths. |

## Examples

```sh
# Every route of r1's main table and BGP's paths.
chalkctl node routes r1

# Every IPv6 route, in the kernel and in BGP.
chalkctl node routes r1 ::/0

# The routes of the VRF tenant: its table and its BGP instance's paths.
chalkctl node routes r1 --vrf tenant

# The routes zebra installed from BGP in every table, and BGP's paths.
chalkctl node routes r1 --protocol bgp --table all
```

## See also

- [chalkctl node](chalkctl_node.md): Manage a node's access and show its diagnostics and routes
