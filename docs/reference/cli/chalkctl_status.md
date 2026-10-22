---
title: "chalkctl status"
description: "Show an installed node's status"
---

# chalkctl status

Show an installed node's status

## Synopsis

Shows an installed node's status: the config it runs and whether it is the one the inventory
gives; its platform; the image it booted, by its store's root hash and the tag and revision it
was built from as far as the node knows them, the boot entry and whether that boot was found
healthy; an upgrade that is installed and boots next; an upgrade that failed and was rolled back,
with the journal of the boot that failed; whether its store is the image the inventory names for
it; its volumes and their state; Kubernetes; the certificates it serves and when they expire; the
CAs it trusts; its clock; and the units that failed. The command needs a reader client file or
the secrets file.

```
chalkctl status <node> [flags]
```

## Examples

```
  chalkctl status cp1

  # With the inventory and the client file a lab writes.
  chalkctl status cp1 --inventory ~/.local/state/chalklab/lab/chalkos.yaml \
    --config ~/.local/state/chalklab/lab/chalkctl.json
```

## Options

```
      --address string     address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --config string      client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)
  -h, --help               help for status
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string   inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --secrets string     secrets file, encrypted with age or not, or - to read it from standard input
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Install and operate chalkos clusters

