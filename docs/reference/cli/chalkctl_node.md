---
title: "chalkctl node"
description: "Manage one node: access, image, reboot and diagnostics"
---

# chalkctl node

Manages one node: renews its node certificate, makes the image staged on it boot next, reboots it
held by its layers, and lists and runs the diagnostics its image declares. Layers add commands of
their own.

node renew needs the secrets file; node revoke and node unrevoke need an admin client file or the
secrets file; node activate and node reboot need an operator client file or the secrets file; node
diag and node routes need a reader client file or the secrets file.

## Examples

```sh
chalkctl node renew w1
chalkctl node activate w1
chalkctl node reboot w1
chalkctl node diag w1 route-get address=2001:db8::1
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalkctl node activate](chalkctl_node_activate.md): Make the image staged on a node boot next
- [chalkctl node diag](chalkctl_node_diag.md): List or run a node's diagnostics
- [chalkctl node reboot](chalkctl_node_reboot.md): Reboot a node, held by its layers, and wait until it is healthy again
- [chalkctl node renew](chalkctl_node_renew.md): Issue a node a new node certificate, also once its own expired
- [chalkctl node revoke](chalkctl_node_revoke.md): Cut a node off the cluster by name
- [chalkctl node routes](chalkctl_node_routes.md): Show a router's routes
- [chalkctl node unrevoke](chalkctl_node_unrevoke.md): Let a revoked node's name join the cluster again
