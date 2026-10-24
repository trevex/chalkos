---
title: "chalkbuild oci"
description: "Write an image directory as an OCI image layout"
---

# chalkbuild oci

```sh
chalkbuild oci <image dir> <layout dir>
```

Writes an image directory, as nix build makes it, as an OCI image layout: one manifest of the
artifact type application/vnd.chalkos.image.v1. Its config is the image's header: its ID, root
hash, architecture, platform, revision and tag. Its layers, uncompressed, are the store data the
root hash covers, the hash tree, the UKI, the boot loader and the contract, a tar of metadata.json,
schemas/, repart.d/ and image.json. The same image directory always makes the same layout, and
mkImages's oci outputs run this command. The layout directory must not exist; when anything
fails, or Ctrl-C stops the command, it is removed again. The command needs no secrets file or
client file.

## Examples

```sh
chalkbuild oci images/node-metal oci/node-metal
```

## See also

- [chalkbuild](chalkbuild.md): Prepare the images Nix builds for a chalkos cluster's nodes
