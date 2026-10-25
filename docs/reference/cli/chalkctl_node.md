---
title: "chalkctl node"
description: "Manage a node's access and show its diagnostics and routes"
---

# chalkctl node

Manages a node's access to the cluster and shows its state: renews its node certificate, revokes
and unrevokes it by name, lists and runs the diagnostics its image declares, and shows a router's
routes. node renew needs the secrets file; node revoke and node unrevoke need an admin client
file or the secrets file; node diag and node routes need a reader client file or the secrets
file.

## Examples

```sh
chalkctl node renew w1
chalkctl node revoke w1
chalkctl node diag w1 route-get address=2001:db8::1
chalkctl node routes r1 ::/0
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalkctl node diag](chalkctl_node_diag.md): List or run a node's diagnostics
- [chalkctl node renew](chalkctl_node_renew.md): Issue a node a new node certificate, also once its own expired
- [chalkctl node revoke](chalkctl_node_revoke.md): Cut a node off the cluster by name
- [chalkctl node routes](chalkctl_node_routes.md): Show a router's routes
- [chalkctl node unrevoke](chalkctl_node_unrevoke.md): Let a revoked node's name join the cluster again
