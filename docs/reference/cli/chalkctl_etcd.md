---
title: "chalkctl etcd"
description: "Manage the cluster's etcd members"
---

# chalkctl etcd

Manages the members of the cluster's etcd, which runs on the control planes.

etcd leave and etcd remove-member need an admin client file or the secrets file; etcd members needs
a reader client file or the secrets file.

## Examples

```sh
chalkctl etcd members
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalkctl etcd leave](chalkctl_etcd_leave.md): Take a control plane out of etcd
- [chalkctl etcd members](chalkctl_etcd_members.md): List etcd's members and their health
- [chalkctl etcd remove-member](chalkctl_etcd_remove-member.md): Remove a node's etcd member, such as a stale one
