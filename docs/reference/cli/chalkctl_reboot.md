---
title: "chalkctl reboot"
description: "Reboot a node"
---

# chalkctl reboot

Reboot a node

## Synopsis

Reboots a node, installed or in maintenance mode. An installed node does not drain its
Kubernetes pods first. The command needs an operator client file or the secrets file; a node in
maintenance mode is reached with --fingerprint or --insecure as well.

```
chalkctl reboot <node> [flags]
```

## Examples

```
  chalkctl reboot w1
```

## Options

```
      --address string       address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --config string        client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when no secrets file is beside the inventory)
      --fingerprint string   SHA-256 fingerprint the node prints on its console in maintenance mode
  -h, --help                 help for reboot
      --identity strings     age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --insecure             accept any certificate and print the node's fingerprint
      --inventory string     inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --manifest string      read the cluster's nodes from this cluster definition's manifest instead of an inventory
      --secrets string       secrets file, - for standard input (default the inventory's, else secrets.age, else secrets.json, beside the inventory)
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Sign, install and operate chalkos clusters

