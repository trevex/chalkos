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
the registry. [`chalkctl install`](../reference/cli/chalkctl_install.md) on a machine that runs
the installer and [`chalkctl upgrade`](../reference/cli/chalkctl_upgrade.md) both have the node
pull the image from it. This guide builds the artifact with `mkImages`, signs it, pushes it with
[chalkbuild](../reference/glossary.md#chalkbuild), names it in the inventory, gives the nodes the
registry's credentials, and installs and upgrades them from it.

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

## Give the nodes the registry's credentials

A node authenticates its pull with a [`Registry`](../reference/kinds.md#registry) document of its
config, whose `host` is the registry's host as the image reference names it. A public registry
that serves the image anonymously needs no `Registry`. The password comes from a `Secret`, so add
both documents to a file every node gets:

```yaml title="cluster/registry.yaml"
apiVersion: oci.chalkos.dev/v1alpha1
kind: Registry
metadata: {name: ghcr}
spec:
  host: ghcr.io
  username: example-bot
  password: {secretRef: {name: registry, key: token}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: registry}
spec:
  data: {token: <token>}
```

The config holds one `Registry` per host. A `Registry` gives `username` and `password` together,
or neither to pull from the host anonymously. chalkd matches the host in any case and as image
references name registries, so `docker.io` matches `index.docker.io`. For an upgrade, it takes
the `Registry` documents of the config the upgrade delivers, or, when the upgrade carries no
config, those of the config the node runs. For an install, it takes only those of the config the
install delivers, because the installer has no config of its own. Keep the file in a list every
node gets, so installs and upgrades both find the credentials.

chalkd pulls over HTTPS only and trusts the certificate authorities of the image's system trust
store; a `Registry` adds no CA of its own. A registry that serves only plain HTTP, such as one in
a lab, needs `insecure: true` in its `Registry`, also when the registry has a private or loopback
address. With it, the credentials and the image travel unencrypted, so anyone on the network path
can read the credentials. The image stays protected, because the node checks every byte against
the digests chalkctl read and against the root hash. Set `insecure` only for a registry on a
trusted network, such as a lab.

## Install from the registry

`chalkctl install` without `--image`, on a machine that runs the
[installer](../reference/glossary.md#installer), sends the installer the image's reference by
digest in place of the image:

```sh
chalkctl install cp1 --secrets=secrets.age --address=<address> --fingerprint=<fingerprint>
```

It prints a line such as `installing cp1 onto serial "S7KHNJ0W100001": chalkos 81bd02c4a9e1
(0f1e2d3c4b5a), which the installer pulls from ghcr.io/example/node-metal@sha256:<digest>`. The
installer pulls the store, the hash tree, the UKI and the boot loader, and takes the image's
schemas, `image.json` and partition definitions from the artifact's contract layer. The machine
that runs the installer therefore needs a route and DNS to the registry, as
[Ports and firewall](../reference/ports-and-firewall.md#connections-a-node-makes) lists.

The installer checks the pulled layers as it checks a streamed image: their digests, the store
against the root hash, and the UKI and boot loader against the firmware's db and dbx when Secure
Boot is enforced. It reads the manifest and the contract before it touches the disk, so a registry
that is unreachable, refuses the credentials or lacks the digest fails the install with an error
that names the reference, and the disk stays as it was. An install that stops partway continues
when the command runs again, by a pull or with `--image`.

## Upgrade from the registry

`chalkctl upgrade` resolves the inventory's reference to a digest once and sends each node the
reference by that digest, so every node pulls the same image even when the tag moves during the
run:

```sh
chalkctl upgrade
```

chalkctl reads only the manifest, the header and the contract from the registry; each node pulls
the store, the hash tree and the UKI itself, so a node needs a connection to the registry, as
[Ports and firewall](../reference/ports-and-firewall.md#connections-a-node-makes) lists. chalkd
checks the pulled header and layer digests against the ones chalkctl sent, then checks the layers
as it checks a streamed image: their SHA-256 sums, the store against the root hash, and the UKI
against the firmware's db and dbx when Secure Boot is enforced. A layer whose registry sends no
data for two minutes stops that node's upgrade. A node that has the image staged already gets it
without contacting the registry.

## What next

- [Install on bare metal](install-bare-metal.md) boots the installer and installs a machine from
  it.
- [Sign images for Secure Boot](secure-boot-signing.md) creates the db key and gets the firmware
  to trust it.
- [`chalkbuild oci`](../reference/cli/chalkbuild_oci.md) writes a layout from an image directory
  outside Nix.
- [Upgrade a cluster](upgrade-cluster.md) rolls an image through the nodes, from a registry or a
  directory.
- [Upgrades](../concepts/upgrades.md) explains what each node checks before the image boots.
