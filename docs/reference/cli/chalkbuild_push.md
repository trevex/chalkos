---
title: "chalkbuild push"
description: "Push an OCI image layout to a registry"
---

# chalkbuild push

```sh
chalkbuild push <layout dir> <reference>
```

Pushes the chalkos image of an OCI image layout, as chalkbuild oci or chalkbuild sign writes
it, to a registry, and prints the reference of its manifest by digest,
&lt;repository>@sha256:&lt;digest>, which an inventory names as the image's oci source. The reference to
push to names its registry and a tag or a digest. chalkbuild authenticates with the credentials
docker login stores in docker's config, or with its credential helpers. It reaches a registry over
plain HTTP at localhost with a port, at a name ending in .localhost, at 127.0.0.1, at ::1, or at a
private (RFC 1918) address, and any other registry over HTTPS. Some registries, Quay among them,
reject a manifest of an artifact type they do not recognize unless an operator allow-lists it; the
registry's own documentation names the setting. The command needs no secrets file or client file.

## Examples

```sh
chalkbuild push oci/node-metal ghcr.io/example/node-metal:v1.5.0
```

## See also

- [chalkbuild](chalkbuild.md): Prepare the images Nix builds for a chalkos cluster's nodes
