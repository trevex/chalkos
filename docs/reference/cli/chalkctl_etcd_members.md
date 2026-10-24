---
title: "chalkctl etcd members"
description: "List etcd's members and their health"
---

# chalkctl etcd members

```sh
chalkctl etcd members [flags]
```

Lists etcd's members the way one control plane's own member sees them: name, ID, peer URLs,
whether a member votes or is a learner, and its health. The first control plane that answers is
asked, or --via. The command needs a reader client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |
| `--via`{ #flag-via } | string | the first one that answers | Control plane to ask. |

## Examples

```sh
chalkctl etcd members --via cp2
```

## See also

- [chalkctl etcd](chalkctl_etcd.md): Manage the cluster's etcd members
