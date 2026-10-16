---
title: "chalkctl etcd remove-member"
description: "Remove a node's etcd member, such as a stale one"
---

# chalkctl etcd remove-member

Remove a node's etcd member, such as a stale one

## Synopsis

Removes another node's etcd member, named by its node or its member ID: for example the member
of a node that is gone. The removal is refused when the voters left would have fewer healthy
members than their quorum, unless --force is given. A control plane other than the removed one
does the removal: --via, or the first one that answers. The command needs an admin client file
or the secrets file.

```
chalkctl etcd remove-member <node|id> [flags]
```

## Examples

```
  # Remove the member of cp3, whose machine failed.
  chalkctl etcd remove-member cp3

  # Remove a member by its ID.
  chalkctl etcd remove-member 8e9e05c52164694d --via cp1
```

## Options

```
      --address string     address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --config string      client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when no secrets file is beside the inventory)
      --force              remove the member even when the voters left would have no healthy quorum
  -h, --help               help for remove-member
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string   inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --manifest string    read the cluster's nodes from this cluster definition's manifest instead of an inventory
      --secrets string     secrets file, - for standard input (default the inventory's, else secrets.age, else secrets.json, beside the inventory)
      --via string         control plane that removes the member (default the first other one that answers)
```

## SEE ALSO

* [chalkctl etcd](chalkctl_etcd.md)	 - Manage the cluster's etcd members

