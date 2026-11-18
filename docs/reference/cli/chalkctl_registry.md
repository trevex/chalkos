---
title: "chalkctl registry"
description: "Manage the images of the cluster registry"
---

# chalkctl registry

Manages the images of the cluster registry, which chalkos-registry serves on the nodes that a
ClusterRegistry document's endpoints name. Nothing copies images between the endpoints, so each
command acts on every one of them, one after the other, and reports each.

## Examples

```sh
chalkctl registry push busybox.tar docker.io/library/busybox:1.37.0
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalkctl registry push](chalkctl_registry_push.md): Push an image to every endpoint of the cluster registry
