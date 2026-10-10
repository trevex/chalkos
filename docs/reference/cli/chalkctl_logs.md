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
      --cluster string       cluster to use when the flake defines several
      --config string        client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when the flake directory holds no secrets file)
      --endpoint string      address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --fingerprint string   SHA-256 fingerprint the node prints on its console in maintenance mode
      --flake string         directory of the flake that defines the cluster (default ".")
  -f, --follow               keep printing new entries
  -h, --help                 help for logs
      --identity strings     age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --insecure             accept any certificate and print the node's fingerprint
      --manifest string      read the cluster's manifest from this file instead of evaluating the flake
      --secrets string       secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
      --unit string          show only this unit's entries
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters

