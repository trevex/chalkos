---
title: "chalkctl etcd"
description: "Manage the cluster's etcd members"
---

# chalkctl etcd

Manage the cluster's etcd members

## Synopsis

Manages the members of the cluster's etcd, which runs on the control planes. etcd members
needs a reader client file, and etcd remove-member and etcd leave need an admin client file; the
secrets file works for all three.

## Examples

```
  chalkctl etcd members
```

## Options

```
  -h, --help   help for etcd
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Build, sign, install and operate chalkos clusters
* [chalkctl etcd leave](chalkctl_etcd_leave.md)	 - Take a control plane out of etcd
* [chalkctl etcd members](chalkctl_etcd_members.md)	 - List etcd's members and their health
* [chalkctl etcd remove-member](chalkctl_etcd_remove-member.md)	 - Remove a node's etcd member, such as a stale one

