---
title: "chalkctl image"
description: "Pull chalkos images from a registry"
---

# chalkctl image

Fetches chalkos images from a registry to the local disk, for sites, labs and tools that read
an image from a directory. Its command needs no secrets file or client file.

## Examples

```sh
chalkctl image pull ghcr.io/example/node-metal:v1.2.0 images/node-metal
```

## See also

- [chalkctl](chalkctl.md): Install and operate chalkos clusters
- [chalkctl image pull](chalkctl_image_pull.md): Pull an image from a registry into an image directory or an OCI layout
