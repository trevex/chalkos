---
title: "chalkbuild"
description: "Prepare the images Nix builds for a chalkos cluster's nodes"
---

# chalkbuild

chalkbuild prepares the images Nix builds for a chalkos cluster's nodes, on the machine that
holds the Secure Boot db key. Nix builds images unsigned, so the key never enters the Nix store or
a binary cache. chalkbuild sign signs a copy of an image directory or of an OCI image layout, which
chalkbuild oci and mkImages's oci outputs write, and chalkbuild push pushes a signed layout to a
registry. The inventory names a signed image directory, or the reference push prints. chalkbuild
never talks to nodes, and its commands need no secrets file or client file.

## Examples

```sh
nix build .#node-metal -o images/node-metal
chalkbuild sign --key db.key --cert db.crt images/node-metal images/node-metal-signed

# Publish the image to a registry.
nix build .#node-metal-oci -o oci/node-metal
chalkbuild sign --key db.key --cert db.crt oci/node-metal oci/node-metal-signed
chalkbuild push oci/node-metal-signed ghcr.io/example/node-metal:v1
```

## See also

- [chalkbuild completion](chalkbuild_completion.md): Write a shell completion script
- [chalkbuild oci](chalkbuild_oci.md): Write an image directory as an OCI image layout
- [chalkbuild push](chalkbuild_push.md): Push an OCI image layout to a registry
- [chalkbuild sign](chalkbuild_sign.md): Sign a copy of an image directory or an OCI image layout for Secure Boot
