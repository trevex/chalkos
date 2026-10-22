---
title: "chalkbuild"
description: "Prepare the images Nix builds for a chalkos cluster's nodes"
---

# chalkbuild

Prepare the images Nix builds for a chalkos cluster's nodes

## Synopsis

chalkbuild prepares the images Nix builds for a chalkos cluster's nodes, on the machine that
holds the Secure Boot db key. Nix builds images unsigned, so the key never enters the Nix store or
a binary cache. chalkbuild sign signs a copy of an image directory or of an OCI image layout, which
chalkbuild oci and mkImages's oci outputs write, and chalkbuild push pushes a signed layout to a
registry. The inventory names a signed image directory, or the reference push prints. chalkbuild
never talks to nodes, and its commands need no secrets file or client file.

## Examples

```
  nix build .#node-metal -o build/node-metal
  chalkbuild sign --key db.key --cert db.crt build/node-metal images/node-metal

  # Publish the image to a registry.
  nix build .#node-metal-oci -o build/node-metal-oci
  chalkbuild sign --key db.key --cert db.crt build/node-metal-oci oci/node-metal
  chalkbuild push oci/node-metal ghcr.io/example/node-metal:v1
```

## Options

```
  -h, --help   help for chalkbuild
```

## SEE ALSO

* [chalkbuild completion](chalkbuild_completion.md)	 - Generate the autocompletion script for the specified shell
* [chalkbuild oci](chalkbuild_oci.md)	 - Write an image directory as an OCI image layout
* [chalkbuild push](chalkbuild_push.md)	 - Push an OCI image layout to a registry
* [chalkbuild sign](chalkbuild_sign.md)	 - Sign a copy of an image directory or an OCI image layout for Secure Boot

