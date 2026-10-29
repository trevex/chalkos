---
title: "chalkctl status"
description: "Show an installed node's status"
---

# chalkctl status

```sh
chalkctl status <node> [flags]
```

Shows an installed node's status: the config it runs and whether it is the one the inventory
gives; its platform; the image it booted, by its store's root hash and the tag and revision it
was built from as far as the node knows them, the boot entry and whether that boot was found
healthy; an upgrade that is installed and boots next; an upgrade that failed and was rolled back,
with the journal of the boot that failed; the previous config and image a rollback restores;
whether its store is the image the inventory names for it; its volumes and their state;
Kubernetes, with the nodes revoked in the cluster on a control plane; a router's BGP sessions;
the certificates it serves and when they expire; the CAs it trusts; its clock; and the units that
failed. The command needs a reader client file or the secrets file.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--address`{ #flag-address } | string | the node's address in the inventory | Address of the node's chalkd, host or host:port. |
| `--config`{ #flag-config } | string | $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given | Client file to authenticate with instead of the secrets file. |
| `--identity`{ #flag-identity } | list of string | ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa | File holding an age identity to decrypt the secrets with; may be repeated. |
| `--inventory`{ #flag-inventory } | string | chalkos.yaml in the current directory, when it exists | Inventory naming the cluster's nodes, their addresses and their config files. |
| `--secrets`{ #flag-secrets } | string | — | Secrets file, encrypted with age or not, or - to read it from standard input. |

## Examples

```sh
chalkctl status cp1

# With the inventory and the client file a lab writes.
chalkctl status cp1 --inventory ~/.local/state/chalklab/lab/chalkos.yaml \
  --config ~/.local/state/chalklab/lab/chalkctl.json
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
