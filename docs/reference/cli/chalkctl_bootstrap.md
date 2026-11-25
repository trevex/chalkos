---
title: "chalkctl bootstrap"
description: "Initialise the cluster on a control plane"
---

# chalkctl bootstrap

```sh
chalkctl bootstrap <node> [flags]
```

Initialises the cluster on one control plane: the node starts etcd as its first member, starts
the Kubernetes control-plane components and applies chalkos's add-ons and the objects of its Addon
documents. chalkctl waits until they are applied, up to --timeout. The other control planes join
the first.

With an inventory, chalkctl first asks the inventory's other control planes for their status and
refuses while one is an etcd member, is joining or does not answer. --force skips this check, as
when recovering a cluster whose other control planes are lost. The node itself refuses when it
is bootstrapped already or holds etcd data, and when an API server answers at the cluster
endpoint, with a certificate of the cluster's CA or of another CA, so a second cluster is not
initialised behind the endpoint. The command needs an admin client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of a node's chalkd: ADDR for the node the command names, or NODE=ADDR for any node, where ADDR is host or host:port; may be repeated. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--force`{ #flag-force } | boolean | `false` | Bootstrap although another control plane of the inventory is an etcd member or is joining, as when recovering a cluster whose other control planes are lost. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |
| `--timeout`{ #flag-timeout } | duration | `20m0s` | How long to wait for the control plane to apply its add-ons and Addon objects. |

## Examples

```sh
chalkctl bootstrap cp1
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
