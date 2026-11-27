---
title: "chalkctl node prune-revoked"
description: "Drop the revoked nodes that nothing lets back in"
---

# chalkctl node prune-revoked

```sh
chalkctl node prune-revoked [flags]
```

Drops from the revoked nodes those that nothing lets back in: the nodes whose certificates have
all expired, and those revoked before both the OS CA and the Kubernetes CA that the control plane
trusts were made, as no CA it trusts vouches for their old certificates. chalkctl rotate os-ca
--finish and chalkctl rotate kubernetes-ca --finish prune the same way once every node applied the
finish; run this command when that prune failed. Each control plane drops the expired nodes by
itself once a day.

With the secrets file, the command refuses while the file records a rotation of the OS CA or the
Kubernetes CAs: a node that has not applied its finish may still trust the old CA. A client file
records no rotation, so make sure none runs. After a rotation of which a phase ran with
--ignore-revoked, a revoked node left in the inventory holds certificates of the new CA, and
pruning lets it back in: the finish of such a rotation keeps the revoked nodes, so do not prune
them by hand either.

A control plane prunes the revoked nodes: --via, or the first one that answers.

The command needs an admin client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of a node's chalkd: ADDR for the one node the command reaches, or NODE=ADDR for any node, where ADDR is host or host:port; may be repeated. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |
| `--via`{ #flag-via } | string | the first one that answers | Control plane that prunes the revoked nodes. |

## Examples

```sh
# Prune after chalkctl rotate os-ca --finish warned that its prune failed.
chalkctl node prune-revoked
```

## See also

- [chalkctl node](chalkctl_node.md): Manage one node: access, image, reboot and diagnostics
