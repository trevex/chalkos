---
title: "chalkctl registry"
description: "Manage the images of the cluster registry"
---

# chalkctl registry

Manages the images of the cluster registry, which chalkos-registry serves on the nodes that a
ClusterRegistry document's endpoints name. Nothing copies images between the endpoints, so each
command acts on every one of them, one after the other, and reports each.

registry delete, registry gc and registry push need an admin client file or the secrets file;
registry list needs a reader client file or the secrets file.

## Examples

```sh
chalkctl registry push busybox.tar docker.io/library/busybox:1.37.0
chalkctl registry list
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalkctl registry delete](chalkctl_registry_delete.md): Delete a tag or digest on every endpoint of the cluster registry
- [chalkctl registry gc](chalkctl_registry_gc.md): Collect garbage on every endpoint of the cluster registry
- [chalkctl registry list](chalkctl_registry_list.md): List the images of every endpoint of the cluster registry and mark those that differ
- [chalkctl registry push](chalkctl_registry_push.md): Push an image to every endpoint of the cluster registry
