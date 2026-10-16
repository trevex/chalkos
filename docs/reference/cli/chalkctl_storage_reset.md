---
title: "chalkctl storage reset"
description: "Wipe and recreate one volume"
---

# chalkctl storage reset

Wipe and recreate one volume

## Synopsis

Wipes one volume of a node and creates it again, empty, the way the node's config defines it.
Its data is lost. A volume the config encrypts is sealed to the TPM again and, unless the
node's fallback is none, gets the second keyslot back: the node's recovery key or its password.
A volume the config leaves unencrypted is created without encryption. The command needs the
inventory, which defines the volume, and an admin client file or the secrets file. A
node whose fallback is its recovery key needs the secrets file, from which the key is derived.

```
chalkctl storage reset <node> <volume> [flags]
```

## Examples

```
  # Recreate the data volume of w1.
  chalkctl storage reset w1 data
```

## Options

```
      --address string         address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --config string          client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when no secrets file is beside the inventory)
  -h, --help                   help for reset
      --identity strings       age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string       inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --manifest string        read the cluster's nodes from this cluster definition's manifest instead of an inventory
      --password-file string   file holding the password of a node whose fallback is a password
      --secrets string         secrets file, - for standard input (default the inventory's, else secrets.age, else secrets.json, beside the inventory)
```

## SEE ALSO

* [chalkctl storage](chalkctl_storage.md)	 - Manage a node's volumes

