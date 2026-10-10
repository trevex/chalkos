---
title: "chalkctl etcd leave"
description: "Take a control-plane node out of etcd"
---

# chalkctl etcd leave

Take a control-plane node out of etcd

## Synopsis

Takes a control-plane node out of etcd: the node releases its virtual IPs, removes its own
member with the same quorum guard as remove-member, stops its control plane, deletes its etcd
data and unpins its addresses. It joins the cluster again only once it is reinstalled. A node
whose own member does not answer, as after it lost its pinned address, leaves only with --force,
through the other members.

```
chalkctl etcd leave <node> [flags]
```

## Examples

```
  chalkctl etcd leave cp3
```

## Options

```
      --cluster string     cluster to use when the flake defines several
      --config string      client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG unless --secrets is given; else ~/.config/chalkos/config when the flake directory holds no secrets file)
      --endpoint string    address of the node's chalkd, host or host:port (default the node's first static address, or the address a client file that prefers its addresses names)
      --flake string       directory of the flake that defines the cluster (default ".")
      --force              leave through the other members also when the node's own etcd member does not answer, as when it lost its pinned address
  -h, --help               help for leave
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --manifest string    read the cluster's manifest from this file instead of evaluating the flake
      --secrets string     secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)
```

## SEE ALSO

* [chalkctl etcd](chalkctl_etcd.md)	 - Manage the cluster's etcd members

