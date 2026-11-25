---
title: "chalkctl status"
description: "Show an installed node's status"
---

# chalkctl status

```sh
chalkctl status <node> [flags]
```

Shows an installed node's status: the config stored for the running image and whether it is the
one the inventory gives; the last completed live activation and any current or failed activation
step; its platform; the image it booted, by its store's root hash and the tag and revision it was
built from as far as the node knows them, the boot entry and whether that boot was found healthy;
an upgrade that is staged, or active and boots next; an upgrade that failed and was rolled back,
with the journal of the boot that failed; a config on trial and when it rolls back; the config it
holds pending for its next upgrade, unchecked, and since when; the previous config and image a
rollback restores; whether its store is the image the inventory names for it; its volumes and
their state; Kubernetes, with the nodes revoked in the cluster on a control plane; a router's BGP
sessions; the certificates it serves and when they expire; the CAs it trusts; its clock; and the
units that failed. The command needs a reader client file or the secrets file.

status decrypts no sops:// or age:// source of a Secret document, so it asks for no key. When the
config the inventory gives the node takes values from such sources, status does not know its
version: it says so in place of comparing it, and chalkctl config apply --diff &lt;node> compares it.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | list of [NODE=]ADDR | each node's address in the inventory | Address of a node's chalkd: ADDR for the node the command names, or NODE=ADDR for any node, where ADDR is host or host:port; may be repeated. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | $CHALKOSINVENTORY, else chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Cluster secrets path (.age, .enc.*, or plaintext), provider URI, or - for standard input. |

## Examples

```sh
chalkctl status cp1

# With the inventory and the client file a lab writes.
export CHALKOSINVENTORY=~/.local/state/chalklab/lab/chalkos.yaml
export CHALKOSCONFIG=~/.local/state/chalklab/lab/chalkctl.json
chalkctl status cp1
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
