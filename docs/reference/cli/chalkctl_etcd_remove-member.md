---
title: "chalkctl etcd remove-member"
description: "Remove a node's etcd member, such as a stale one"
---

# chalkctl etcd remove-member

```sh
chalkctl etcd remove-member <node|id> [flags]
```

Removes another node's etcd member, named by its node or its member ID: for example the member
of a node that is gone. The removal is refused when the voters left would have fewer healthy
members than their quorum, unless --force is given. A control plane other than the removed one
does the removal: --via, or the first one that answers. The command needs an admin client file
or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--force`{ #flag-force } | boolean | `false` | Remove the member even when the voters left would have no healthy quorum. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--via`{ #flag-via } | string | the first other one that answers | Control plane that removes the member. |

## Examples

```sh
# Remove the member of cp3, whose machine failed.
chalkctl etcd remove-member cp3

# Remove a member by its ID.
chalkctl etcd remove-member 8e9e05c52164694d --via cp1
```

## See also

- [chalkctl etcd](chalkctl_etcd.md): Manage the cluster's etcd members
