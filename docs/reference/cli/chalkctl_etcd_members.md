---
title: "chalkctl etcd members"
description: "List etcd's members and their health"
---

# chalkctl etcd members

List etcd's members and their health

## Synopsis

Lists etcd's members the way one control plane's own member sees them: name, ID, peer URLs,
whether a member votes or is a learner, and its health. The first control plane that answers is
asked, or --via. The command needs a reader client file or the secrets file.

```
chalkctl etcd members [flags]
```

## Examples

```
  chalkctl etcd members --via cp2
```

## Options

```
      --address string     address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --config string      client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)
  -h, --help               help for members
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string   inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --manifest string    read the cluster's nodes from this cluster definition's manifest instead of an inventory
      --secrets string     secrets file, encrypted with age or not, or - to read it from standard input
      --via string         control plane to ask (default the first one that answers)
```

## SEE ALSO

* [chalkctl etcd](chalkctl_etcd.md)	 - Manage the cluster's etcd members

