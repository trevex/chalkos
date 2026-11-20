---
title: "chalkctl image pull"
description: "Pull an image from a registry into an image directory or an OCI layout"
---

# chalkctl image pull

```sh
chalkctl image pull <reference> <dir> [flags]
```

Pulls the chalkos image a reference names, by tag or by digest, from its registry into a
directory, and prints the reference by digest the pull resolved it to. A tag is resolved once,
and every fetch after it names the digest. The registry's credentials come from the Docker
config and its credential helpers, as for the inventory's oci sources, and a registry on a
loopback or private address is reached over plain HTTP.

Without --layout, the directory becomes an image directory as nix build makes it: the raw disk
image, repart-output.json, metadata.json, schemas, repart.d and facts. The inventory's directory
source, install, upgrade, chalklab and chalkbuild read it as a built image of the same digest,
and two pulls of an image write the same bytes. It writes the image's ESP with mtools'
mformat, mmd and mcopy, which must be on PATH: the chalkctl of the flake brings them, and the
release's static binary needs the distribution's mtools package.
With --layout, the directory becomes the OCI image layout of the manifest the registry serves,
byte for byte, which chalkbuild push or another OCI tool pushes to another registry under the
same digest. The layout holds no cosign signatures.

The pull checks the image as a node does: its header against its manifest and contract, every
part against its digest and the store against its hash tree. The directory appears only once
the whole image is written and checked. It must not exist or be empty; one that holds the same
image already is left as it is, and one that holds anything else is refused.

## Flags

| Flag | Type | Default | Description |
| --- | --- | --- | --- |
| `--layout`{ #flag-layout } | boolean | `false` | Write the OCI layout the registry serves, whose manifest keeps its digest, instead of an image directory. |

## Examples

```sh
# Pull an image for the inventory's directory source at a site without a registry.
chalkctl image pull ghcr.io/example/node-metal:v1.2.0 images/node-metal

# Carry an image's layout to an air-gapped site and push it to the site's registry there.
chalkctl image pull --layout ghcr.io/example/node-metal:v1.2.0 node-metal-oci
chalkbuild push node-metal-oci registry.site.example/node-metal:v1.2.0
```

## See also

- [chalkctl image](chalkctl_image.md): Pull chalkos images from a registry
