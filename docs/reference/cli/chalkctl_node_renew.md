---
title: "chalkctl node renew"
description: "Issue a node a new node certificate, also once its own expired"
---

# chalkctl node renew

Issue a node a new node certificate, also once its own expired

## Synopsis

Issues the node a new node certificate and key from the node CA and delivers them, also once the
node's own certificate expired. Such a node is verified by the OS CA as of its certificate's
start, so chalkctl trusts the node's old key: someone holding a leaked, expired key of the node
and sitting in its network path could receive the new certificate in its place. That is inherent
to recovering a node; renewing node certificates before they expire avoids it. The command needs
the secrets file.

```
chalkctl node renew <node> [flags]
```

## Examples

```
  chalkctl node renew w1
```

## Options

```
      --address string     address of the node's chalkd, host or host:port (default the node's address in the inventory)
  -h, --help               help for renew
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string   inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --secrets string     secrets file, encrypted with age or not, or - to read it from standard input
```

## SEE ALSO

* [chalkctl node](chalkctl_node.md)	 - Manage a node's certificate

