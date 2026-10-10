---
title: "chalkctl storage reset"
description: "Wipe and recreate one volume"
---

# chalkctl storage reset

Wipe and recreate one volume

## Synopsis

Wipes one volume of a node and creates it again as the node's identity defines it, empty. Its
data is lost. The volume is unlocked as before: by the TPM, and by the second keyslot's recovery
key or password. The command needs the cluster definition, which defines the volume.

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
      --cluster string         cluster to use when the flake defines several
      --config string          client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when the flake directory holds no secrets file)
      --endpoint string        address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --flake string           directory of the flake that defines the cluster (default ".")
  -h, --help                   help for reset
      --identity strings       age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --manifest string        read the cluster's manifest from this file instead of evaluating the flake
      --password-file string   file holding the password of a node whose fallback is a password
      --secrets string         secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
```

## SEE ALSO

* [chalkctl storage](chalkctl_storage.md)	 - Manage a node's volumes

