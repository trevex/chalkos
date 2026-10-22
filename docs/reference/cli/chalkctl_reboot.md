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
      --config string        client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)
      --fingerprint string   SHA-256 fingerprint the node prints on its console in maintenance mode
  -h, --help                 help for reboot
      --identity strings     age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --insecure             accept any certificate and print the node's fingerprint
      --inventory string     inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --secrets string       secrets file, encrypted with age or not, or - to read it from standard input
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Install and operate chalkos clusters

