---
title: "Publish images to a registry"
description: "Build an image as an OCI layout, sign it if the nodes need it, push it and name its digest in the inventory"
---

# Publish images to a registry

This page is for an image author who ships images through an OCI registry instead of a directory
on the operator's machine. At the end the image is in the registry, and the inventory names it by
digest, so nodes pull it themselves on an install or an upgrade.

The registry holds the image as one OCI artifact, with the same parts as an image directory: the
store, its hash tree, the UKI, the boot loader and the contract, which describes the config the
image accepts. [`chalkctl validate`](../reference/cli/chalkctl_validate.md),
[`chalkctl apply-config`](../reference/cli/chalkctl_apply-config.md) and
[`chalkctl status`](../reference/cli/chalkctl_status.md) read the image's header and contract from
the registry. [`chalkctl install`](../reference/cli/chalkctl_install.md) on a machine that runs the
installer and [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) have the node pull the
rest.

## Before you begin

You need:

- The flake of [Your first image flake](first-image.md).
- chalkbuild, from `nix shell github:trevex/chalkos#chalkbuild` or from
  `chalkos.packages.${system}.chalkbuild` in the cluster flake's dev shell.
- Push access to a registry, with credentials that `docker login` stored in docker's config or a
  credential helper. chalkbuild reads them from there.
- For machines that enforce Secure Boot, the db key and certificate of
  [Sign images for Secure Boot](sign-images.md).

## Expose the layout

An OCI image layout is the directory form of an OCI image, which registries and tools such as
skopeo copy. `mkImages` returns, beside each image directory `images.<variant>-<platform>`, its
layout as `oci.<variant>-<platform>`. Each layout is built only when something asks for it.
Expose the one to publish as a package of the cluster's flake, where `images` is the result of
`mkImages`:

```nix
packages.x86_64-linux.node-metal-oci = images.oci.node-metal;
```

## Build the layout

Build the layout and link it next to the image directory:

```sh
nix build .#node-metal-oci -o oci/node-metal
```

The `oci` outputs run [`chalkbuild oci`](../reference/cli/chalkbuild_oci.md) on the image
directory. The same image directory always makes the same layout, so the command gives the same
result outside Nix, from a directory that `nix build` already wrote:

```sh
chalkbuild oci images/node-metal oci/node-metal
```

The layout directory must not exist, as for every chalkbuild command that writes one.

## Sign the layout

Skip this step when the nodes do not enforce Secure Boot. Otherwise sign a copy of the layout with
the db key:

```sh
rm -rf oci/node-metal-signed
chalkbuild sign --key db.key --cert db.crt oci/node-metal oci/node-metal-signed
```

[`chalkbuild sign`](../reference/cli/chalkbuild_sign.md) signs copies of the UKI and boot-loader
layers, checks them against `--cert` and writes a new layout with them and a new manifest. The
store, hash tree and contract layers are copied as they are. The output directory must not exist,
so the `rm -rf` removes the signed layout of the previous build.

## Push the layout

[`chalkbuild push`](../reference/cli/chalkbuild_push.md) pushes a layout to a reference that names
the registry and a tag or a digest:

```sh
chalkbuild push oci/node-metal-signed ghcr.io/example/node-metal:v1.5.0
```

Push `oci/node-metal` instead when you skipped signing. The command prints the reference of the
pushed manifest by digest, `ghcr.io/example/node-metal@sha256:<digest>`.

chalkbuild reaches a registry over plain HTTP at `localhost` with a port, at a name ending in
`.localhost`, at `127.0.0.1`, at `::1`, or at a private (RFC 1918) address, and any other registry
over HTTPS. Some registries, Quay among them, reject a manifest of an artifact
type they do not recognise until an operator allow-lists `application/vnd.chalkos.image.v1`. The
layout is a standard OCI image layout, so any tool that copies one, such as skopeo or oras, can
push it too.

## Name the digest in the inventory

The image's `source` in the inventory's `spec.images` takes the reference as `oci`, in place of
`directory`. Use the reference by digest that `chalkbuild push` printed:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
  images:
    node-metal: {source: {oci: "ghcr.io/example/node-metal@sha256:<digest>"}}
  all: {imageName: node-metal, files: [cluster/*.yaml]}
  nodes:
    cp1: {address: 10.0.0.11, files: [nodes/cp1.yaml]}
    w1: {address: 10.0.0.21, files: [nodes/w1.yaml]}
```

A reference by digest pins the image, so the inventory changes only when you push a new one. The
inventory may name a tag instead, such as `ghcr.io/example/node-metal:v1.5.0`. chalkctl resolves
a tag to a digest once per command, so one command reads one image even when the tag moves while
it runs.
[Config and the inventory](../how-it-works/node/config.md#how-does-the-inventory-name-images)
covers the rest of `spec.images`.

## The artifact holds the image and its contract

The artifact is one manifest of the artifact type `application/vnd.chalkos.image.v1`. Its config
is the image's header: its ID, root hash, architecture, platform, revision and tag. Its layers are
the image's parts, uncompressed.
[Image and partition layout](../reference/image-layout.md#oci-artifact)
lists the media types and the header's fields.

## Install from the registry

`chalkctl install` without `--image`, on a machine that runs the installer, sends the installer
the image's reference by digest in place of the image:

```sh
chalkctl install cp1 --secrets=secrets.age --address=<address> --fingerprint=<fingerprint>
```

It prints a line of this form, where `<serial>` is the system disk's serial, `<root-hash>` and
`<revision>` the first 12 hex digits of the image's root hash and git revision, and `<digest>` the
manifest's digest:

```text
installing cp1 onto serial "<serial>": chalkos <root-hash> (<revision>), which the installer pulls from ghcr.io/example/node-metal@sha256:<digest>
```

The installer pulls the store, the hash tree, the UKI and the boot loader, and takes the image's
schemas, `image.json` and partition definitions from the artifact's contract layer. The machine
that runs the installer therefore needs a route and DNS to the registry or to an endpoint of its
`Mirror`, as [Ports and firewall](../reference/ports-and-firewall.md#connections-a-node-makes)
lists.

The installer checks the pulled layers as it checks a streamed image: their digests, the store
against the root hash, and the UKI and boot loader against the firmware's db and dbx when Secure
Boot is enforced. It reads the manifest and the contract before it touches the disk. When no
source serves them, because each is unreachable, refuses the credentials or lacks the digest, the
install fails with an error that names the reference, and the disk stays as it was. An install
that stops partway continues when the command runs again, by a pull or with `--image`.

## Upgrade from the registry

`chalkctl upgrade` resolves the inventory's reference to a digest once and sends each node the
reference by that digest, so every node pulls the same image even when the tag moves during the
run:

```sh
chalkctl upgrade
```

chalkctl reads only the manifest, the header and the contract from the registry. Each node pulls
the store, the hash tree and the UKI itself, so a node needs a connection to the registry or to an
endpoint of its `Mirror`, as
[Ports and firewall](../reference/ports-and-firewall.md#connections-a-node-makes) lists. chalkd
checks the pulled header and layer digests against the ones chalkctl sent. Then it checks the
layers as it checks a streamed image: their SHA-256 sums, the store against the root hash, and
the UKI against the firmware's db and dbx when Secure Boot is enforced. A layer whose source stops
sending data for two minutes after it started stops that node's upgrade. A node that has the
image staged already gets it without contacting the registry.

A private registry needs credentials on each node, in a `Registry` document of its config.
[Registries and mirrors](../operate/registries.md) sets them up, and pulls through a mirror.

## Check that it worked

`chalkctl validate` reads the header and contract from the registry and checks each node's config
against them:

```sh
chalkctl validate
```

For each node whose config is valid it prints a line of this form, where `<root-hash>` is the
first 12 hex digits of the image's root hash and `<hash>` the hash of the node's config:

```text
cp1: valid for the image node-metal at ghcr.io/example/node-metal@sha256:<digest> (node on metal, root hash <root-hash>), config <hash>
```
 The digest is that of the manifest chalkctl read, so it names
the image that was checked even when the inventory names a tag.

## What next

- [Registries and mirrors](../operate/registries.md) gives the nodes the registry's credentials.
- [Test and roll out an image](test-and-roll-out.md) upgrades the nodes to the pushed image.
- [Upgrades](../how-it-works/node/upgrades.md) explains what each node checks before the image
  boots.
