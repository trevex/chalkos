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
      --cluster string     cluster to use when the flake defines several
      --endpoint string    address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --flake string       directory of the flake that defines the cluster (default ".")
  -h, --help               help for renew
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --manifest string    read the cluster's manifest from this file instead of evaluating the flake
      --secrets string     secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
```

## SEE ALSO

* [chalkctl node](chalkctl_node.md)	 - Manage a node's certificate

