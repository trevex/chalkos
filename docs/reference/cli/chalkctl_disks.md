---
title: "chalkctl disks"
description: "List a node's disks"
---

# chalkctl disks

List a node's disks

## Synopsis

Lists a node's disks with their use, the partitions on them, and the properties a storage
definition selects disks by: size, type, model, serial number and WWN. An installed node is
reached with a reader client file or the secrets file; a node in maintenance mode with
--fingerprint or --insecure as well. A node that the inventory does not name yet is reached at
--address, in maintenance mode: an image or installer built with an OS CA accepts only a client
with the secrets file or a client file, one built without accepts any client.

```
chalkctl disks [<node>] [flags]
```

## Examples

```
  # The disks of an installed node.
  chalkctl disks w1

  # The disks of a machine that booted the installer and is not in the inventory yet.
  chalkctl disks --address 192.168.1.50 --insecure --secrets secrets.age
```

## Options

```
      --address string       address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --config string        client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)
      --fingerprint string   SHA-256 fingerprint the node prints on its console in maintenance mode
  -h, --help                 help for disks
      --identity strings     age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --insecure             accept any certificate and print the node's fingerprint
      --inventory string     inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --secrets string       secrets file, encrypted with age or not, or - to read it from standard input
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Sign, install and operate chalkos clusters

