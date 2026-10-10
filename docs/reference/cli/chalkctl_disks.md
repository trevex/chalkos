---
title: "chalkctl disks"
description: "List a node's disks"
---

# chalkctl disks

List a node's disks

## Synopsis

Lists the disks of a node with their size, type, model, serial number, WWN and use, and the
partitions on them, as a node's storage definition names its disks. An installed node is reached
with the cluster's credentials; a node in maintenance mode with --fingerprint or --insecure. A
node that the cluster definition does not name yet is reached at --endpoint, in maintenance mode.

```
chalkctl disks [<node>] [flags]
```

## Examples

```
  # The disks of an installed node.
  chalkctl disks w1

  # The disks of a machine that booted the installer and is not in the cluster definition yet.
  chalkctl disks --endpoint 192.168.1.50 --insecure
```

## Options

```
      --cluster string       cluster to use when the flake defines several
      --config string        client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when the flake directory holds no secrets file)
      --endpoint string      address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --fingerprint string   SHA-256 fingerprint the node prints on its console in maintenance mode
      --flake string         directory of the flake that defines the cluster (default ".")
  -h, --help                 help for disks
      --identity strings     age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --insecure             accept any certificate and print the node's fingerprint
      --manifest string      read the cluster's manifest from this file instead of evaluating the flake
      --secrets string       secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

