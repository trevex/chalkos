---
title: "chalkctl etcd leave"
description: "Take a control plane out of etcd"
---

# chalkctl etcd leave

```sh
chalkctl etcd leave <node> [flags]
```

Takes a control plane out of etcd: the node releases its virtual IPs, removes its own member
with the same quorum guard as remove-member, stops its static pods (etcd and the Kubernetes
control-plane components), deletes its etcd data and unpins its addresses. It joins the cluster
again only once it is reinstalled. A node whose own member does not answer (for example after it
lost its pinned address) leaves only with --force, through the other members. The command needs
an admin client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--force`{ #flag-force } | boolean | `false` | Leave through the other members also when the node's own etcd member does not answer (for example when it lost its pinned address). |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |

## Examples

```sh
chalkctl etcd leave cp3
```

## See also

- [chalkctl etcd](chalkctl_etcd.md): Manage the cluster's etcd members
