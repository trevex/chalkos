---
title: "chalkctl logs"
description: "Show a node's journal"
---

# chalkctl logs

Show a node's journal

## Synopsis

Prints a node's journal of the current boot, of every unit or of --unit alone; with --follow it
keeps printing new entries until interrupted. The command needs a reader client file or the
secrets file. A node in maintenance mode is reached with --fingerprint or --insecure as well.

```
chalkctl logs <node> [flags]
```

## Examples

```
  # Follow chalkd's log on cp1.
  chalkctl logs cp1 --unit chalkd.service -f
```

## Options

```
      --address string       address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --config string        client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when no secrets file is beside the inventory)
      --fingerprint string   SHA-256 fingerprint the node prints on its console in maintenance mode
  -f, --follow               keep printing new entries
  -h, --help                 help for logs
      --identity strings     age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --insecure             accept any certificate and print the node's fingerprint
      --inventory string     inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --manifest string      read the cluster's nodes from this cluster definition's manifest instead of an inventory
      --secrets string       secrets file, - for standard input (default the inventory's, else secrets.age, else secrets.json, beside the inventory)
      --unit string          show only this unit's entries
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Sign, install and operate chalkos clusters

