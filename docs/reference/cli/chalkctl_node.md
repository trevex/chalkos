---
title: "chalkctl node"
description: "Manage a node's certificate"
---

# chalkctl node

Manage a node's certificate

## Synopsis

Manages a node's access to the cluster: renews its node certificate, and revokes and unrevokes
it by name. node renew needs the secrets file; node revoke and node unrevoke need an admin client
file or the secrets file.

## Examples

```
  chalkctl node renew w1
  chalkctl node revoke w1
```

## Options

```
  -h, --help   help for node
```

## SEE ALSO

* [chalkctl](chalkctl.md)	 - Install and operate chalkos clusters
* [chalkctl node renew](chalkctl_node_renew.md)	 - Issue a node a new node certificate, also once its own expired
* [chalkctl node revoke](chalkctl_node_revoke.md)	 - Cut a node off the cluster by name
* [chalkctl node unrevoke](chalkctl_node_unrevoke.md)	 - Let a revoked node's name join the cluster again

