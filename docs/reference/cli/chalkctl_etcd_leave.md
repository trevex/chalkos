---
title: "chalkctl etcd leave"
description: "Take a control plane out of etcd"
---

# chalkctl etcd leave

Take a control plane out of etcd

## Synopsis

Takes a control plane out of etcd: the node releases its virtual IPs, removes its own member
with the same quorum guard as remove-member, stops its static pods (etcd and the Kubernetes
control-plane components), deletes its etcd data and unpins its addresses. It joins the cluster
again only once it is reinstalled. A node whose own member does not answer (for example after it
lost its pinned address) leaves only with --force, through the other members. The command needs
an admin client file or the secrets file.

```
chalkctl etcd leave <node> [flags]
```

## Examples

```
  chalkctl etcd leave cp3
```

## Options

```
      --address string     address of the node's chalkd, host or host:port (default the node's address in the inventory)
      --config string      client file to authenticate with instead of the secrets file (default $CHALKOSCONFIG, else ~/.config/chalkos/config, unless --secrets is given)
      --force              leave through the other members also when the node's own etcd member does not answer (for example when it lost its pinned address)
  -h, --help               help for leave
      --identity strings   age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)
      --inventory string   inventory naming the cluster's nodes, their addresses and their config files (default chalkos.yaml in the current directory, when it exists)
      --secrets string     secrets file, encrypted with age or not, or - to read it from standard input
```

## SEE ALSO

* [chalkctl etcd](chalkctl_etcd.md)	 - Manage the cluster's etcd members

