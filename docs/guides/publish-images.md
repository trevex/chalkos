---
title: "Publish images to a registry"
description: "Build an image as an OCI artifact, sign it for Secure Boot, push it and name it in the inventory"
---

# Publish images to a registry

A chalkos image can be published to an OCI registry as one artifact, so a cluster's inventory names
it by its reference in the registry. The artifact holds the same
image as an [image directory](../reference/glossary.md#image-directory): the store, its hash tree,
the [UKI](../reference/glossary.md#uki), the boot loader and the contract, which describes the
config the image accepts. [`chalkctl validate`](../reference/cli/chalkctl_validate.md),
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) and
[`chalkctl status`](../reference/cli/chalkctl_status.md) read the image's header and contract from
the registry. This guide builds the artifact with `mkImages`, signs it, pushes it with
[chalkbuild](../reference/glossary.md#chalkbuild) and names it in the inventory.

!!! note "Install and upgrade take a directory"

    `chalkctl upgrade` and an install from the installer send an image from its directory, so they
    refuse a node whose image the inventory names in a registry. A node that runs its image
    already installs in place either way. Keep a directory source for the image, or pass `--image`
    with its directory, for those commands.

## Before you begin

You need:

- chalkbuild, from `nix shell github:trevex/chalkos#chalkbuild` or from
  `chalkos.packages.${system}.chalkbuild` in the cluster flake's dev shell.
- Push access to a registry, with credentials that `docker login` stored in docker's config or a
  credential helper. chalkbuild reads them from there.
- For machines with Secure Boot, the db key and certificate of
  [Sign images for Secure Boot](secure-boot-signing.md).

## Expose the layout

`mkImages` returns, beside each image directory `images.<variant>-<platform>`, its OCI image layout
as `oci.<variant>-<platform>`. An OCI image layout is the directory form of an OCI image that
registries and tools such as skopeo copy. Each layout is built only when asked for. Expose the one
to publish as a package of the cluster's flake, where `images` is the result of `mkImages`:

```nix
packages.x86_64-linux.node-metal-oci = images.oci.node-metal;
```

## Build and sign the layout

Build the layout, then sign it into a directory of its own with the db key:

```sh
nix build .#node-metal-oci -o build/node-metal-oci
rm -rf oci/node-metal
chalkbuild sign --key=db.key --cert=db.crt build/node-metal-oci oci/node-metal
```

[`chalkbuild sign`](../reference/cli/chalkbuild_sign.md) signs copies of the UKI and boot-loader
layers, checks them against `--cert` and writes a new layout with them and a new manifest. The
store, hash tree and contract layers are copied as they are. The output directory must not exist,
so the previous signed layout is removed first.

Machines without Secure Boot need no signature: skip `chalkbuild sign` and push
`build/node-metal-oci` itself.

## Push the layout

[`chalkbuild push`](../reference/cli/chalkbuild_push.md) pushes the layout to a reference that
names the registry and a tag or digest, and prints the reference of the pushed manifest by digest:

```sh
chalkbuild push oci/node-metal ghcr.io/example/node-metal:v1.5.0
```

It prints `ghcr.io/example/node-metal@sha256:<digest>`. chalkbuild reaches a registry at a
loopback or private (RFC 1918) address over plain HTTP, and any other registry over HTTPS. Some
registries, Quay among them, reject a manifest of an artifact type they do not recognize until an
operator allow-lists `application/vnd.chalkos.image.v1`. The layout is a standard OCI image layout,
so any tool that copies one, such as skopeo or oras, can push it too.

## Name the image in the inventory

The image's `source` in the inventory's `spec.images` takes the reference as `oci`, in place of
`directory`:

```yaml title="chalkos.yaml"
spec:
  images:
    node-metal: {source: {oci: ghcr.io/example/node-metal@sha256:<digest>}}
```

A reference by digest pins the image. A reference by tag is resolved to a digest once per chalkctl
command, so one command reads one image even when the tag moves while it runs.
[How does the inventory name images?](../concepts/config.md#how-does-the-inventory-name-images)
covers the rest of `spec.images`.

## What does the artifact hold?

The artifact is one manifest of the artifact type `application/vnd.chalkos.image.v1`. Its config is
the image's header and its layers are the image's parts, uncompressed. [Image and partition
layout](../reference/image-layout.md#oci-artifact) lists the media types and the header's fields.

## Check that it worked

`chalkctl validate` reads the header and contract from the registry and checks each node's config
against them:

```sh
chalkctl validate
```

It prints a line such as `cp1: valid for the image node-metal at
ghcr.io/example/node-metal@sha256:<digest> (node on metal, root hash 2ece03b0fa09), config <hash>`
for each node whose config is valid. The digest is that of the manifest chalkctl read, so it names
the image that was checked even when the inventory names a tag.

## What next

- [Sign images for Secure Boot](secure-boot-signing.md) creates the db key and gets the firmware
  to trust it.
- [`chalkbuild oci`](../reference/cli/chalkbuild_oci.md) writes a layout from an image directory
  outside Nix.
- [Upgrade a cluster](upgrade-cluster.md) rolls an image from its directory through the nodes.
