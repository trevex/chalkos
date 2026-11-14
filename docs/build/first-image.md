---
title: "Your first image flake"
description: "Write a flake that builds a cluster's image with mkImages, into the directory the inventory names"
---

# Your first image flake

This page is for an image author who builds a cluster's images with Nix. At the end you have a
flake that builds unsigned images into the directories the cluster's inventory names, such as
`images/node-metal`, and you know what the build writes there and what else `mkImages` returns.
The page follows the
[homelab example](https://github.com/trevex/chalkos/tree/main/examples/homelab): two images for
bare metal, one for its router and one that its three Kubernetes nodes run.

## Before you begin

You need Nix with flakes enabled on an x86-64 Linux machine, and a git repository for the flake.
A flake sees only the files git tracks, so `git add` a new module before you build.

Nix is needed only on the machine that builds images. chalkctl and chalklab never evaluate the
flake: the inventory names the directory a build was linked to, so the machine that runs chalkctl
needs that directory and no Nix.

## The flake calls mkImages

The homelab's flake builds two images. The nodes' carries an extension that adds two config
kinds:

```nix title="flake.nix"
--8<-- "examples/homelab/flake.nix"
```

The flake takes chalkos as an input and calls `chalkos.lib.mkImages`. `flake.lock` pins the
chalkos version, so the images change only when the lock file or the flake's own modules change.
The call sets three arguments:

- `revision` is the commit the image is built from. chalkctl shows it beside the image's root
  hash.
- `platforms` names the kinds of machine to build for, here `metal` alone.
- `variants` names two variants. `router` runs the router layer in place of Kubernetes, and
  `node.modules` adds the extension's NixOS modules to the variant `node`.

`mkImages` returns each image as `images.<variant>-<platform>`, such as `images.node-metal`, and
the flake exposes them as packages of the same names, beside the installer. `images.checks` holds
each image's tests, which the flake exports as its checks, so `nix flake check` runs them, as
[Test an extension](test-an-extension.md) describes. The flake's shell, which
`nix develop` enters, holds chalkctl and chalkbuild of the chalkos that `flake.lock` pins. One image
serves every node of its variant and platform. What differs between
nodes, from their addresses to the cluster's endpoint, is their config: YAML documents that
chalkctl delivers, which [Config and the inventory](../how-it-works/node/config.md) describes.

## mkImages checks its arguments as module options

`mkImages` takes these arguments:

| Argument | Default | What it sets |
| --- | --- | --- |
| `variants` | `{ node = { }; }` | The variants to build, each with its NixOS modules and whether its images run Kubernetes |
| `platforms` | `[ "metal" "kvm" ]` | The platforms every variant is built for, as a list of names or with each platform's modules |
| `modules` | `[ ]` | NixOS modules every variant's images carry |
| `osCA` | `null` | The public part of the cluster's secrets, whose OS CA the images and the installer trust in maintenance mode |
| `installer.modules` | `[ ]` | The installer's own NixOS modules |
| `nixpkgs` | the nixpkgs chalkos is tested against | The nixpkgs the images are evaluated and built from |
| `system` | `x86_64-linux` | The platform the images are built for, as Nix names it |
| `specialArgs` | `{ }` | Extra arguments for every image's modules, such as a flake's inputs |
| `revision` | `null` | The git revision the images are built from |
| `tag` | `null` | The git tag the images are built from, such as a release's |

The arguments are module options, so a misspelled argument fails the evaluation and names the
argument. The [mkImages reference](../reference/mkimages.md) documents each one.
[Variants and platforms](variants-and-platforms.md) covers `variants`, `platforms`, `modules` and
`osCA`.

`revision` and `tag` say where an image came from. Nix cannot read git tags, so whatever builds a
release passes the tag:

```nix
images = chalkos.lib.mkImages {
  revision = self.rev or self.dirtyRev or null;
  tag = "v1.5.0";
  platforms = [ "metal" ];
};
```

`self` must be among the arguments of the flake's `outputs`, as in the homelab's flake. Each image
directory's `metadata.json` records both values, and chalkctl shows them beside the root hash's
first 12 hex characters, as in `3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6)`. They never reach the
image's store, so they change no root hash, and nothing orders or refuses an image by them.

An image has no version to raise. Its identity is the root hash of its store: a changed module
changes the store, so the build is a new image. The build refuses `system.image.version`, because
systemd-boot would order boot entries by it.

## Build the image

Build each package and link the result where the inventory looks for it:

```console
$ nix build .#router-metal -o images/router-metal
$ nix build .#node-metal -o images/node-metal
$ ls images/node-metal
chalkos.raw
facts
metadata.json
repart.d
repart-output.json
schemas
```

`images/node-metal` is a link into `/nix/store`. The homelab's inventory names it as the image
`node-metal` and gives it to the nodes of the group `nodes`, and gives `router-metal` to the
router:

```yaml title="chalkos.yaml"
apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
  images:
    router-metal: {source: {directory: images/router-metal}}
    node-metal: {source: {directory: images/node-metal}}
  groups:
    router: {imageName: router-metal, files: [router/*.yaml]}
    nodes: {imageName: node-metal, files: [cluster/*.yaml, bgp/fabric.yaml, bgp/secrets.yaml]}
  nodes:
    router: {address: 192.168.1.2, groups: [router]}
    node1: {address: "fdc8:a1b2:c3d4:ff::11", groups: [nodes], files: [nodes/node1.yaml]}
    node2: {address: "fdc8:a1b2:c3d4:ff::12", groups: [nodes], files: [nodes/node2.yaml]}
    node3: {address: "fdc8:a1b2:c3d4:ff::13", groups: [nodes], files: [nodes/node3.yaml]}
```

A relative directory is relative to the inventory's directory. The next build replaces the link,
so the inventory names the newest image without an edit.

## The image directory holds the disk image and its contract

The build writes six entries:

- `chalkos.raw` is the raw disk image. Its slot A carries labels named after its root hash, which
  the build sets once systemd-repart has computed the hash.
- `repart-output.json` is systemd-repart's report of the partitions, with their offsets, sizes and
  the root hash. chalkctl reads the store, the hash tree and the UKI at those offsets.
- `repart.d` holds the partition definitions of the system region, which the installer lays out
  on a target disk.
- `metadata.json` names the image's root hash, variant and platform, the revision and tag it was
  built from, and the kinds it accepts.
- `schemas` holds the JSON Schema of each kind the image accepts.
- `facts` holds a `<layer>.json` for each of the image's layers that has facts. On an image with
  Kubernetes, `facts/kubernetes.json` records what the image fixes about Kubernetes: the release of
  its kubelet and its pod network. On an image without such layers, `facts` is empty.

chalkctl reads `metadata.json`, `schemas` and `facts` when it validates, installs or upgrades
a node whose image in the inventory is this directory.
[Image and partition layout](../reference/image-layout.md#outputs-of-an-image-build) lists the
files in detail.

The image is unsigned. A node that does not enforce Secure Boot installs it as it is. Nodes that
enforce Secure Boot need a signed copy, which
[Sign images for Secure Boot](sign-images.md) makes on a machine that holds the key, so
the key never enters `/nix/store`.

chalkos does not wrap image conversion. `qemu-img convert -f raw -O qcow2` makes a qcow2 disk for
KVM, `zstd` compresses the raw image for transport, and `dd` or a USB writer puts it on a disk.

## mkImages returns four sets of outputs

The homelab exposes two images and the installer, but `mkImages` returns four attribute sets.
Each is built only when something asks for it:

```mermaid
flowchart LR
    flake["flake.nix and flake.lock"] -->|calls| mk["mkImages"]
    mods["Your NixOS modules"] -->|arguments| mk
    mk --> images["images.node-metal"]
    mk --> oci["oci.node-metal"]
    mk --> installer["installer"]
    mk --> nixos["nixos.node-metal"]
    images -->|nix build| dir["Image directory"]
    oci -->|chalkbuild push| registry["Registry"]
```

The diagram shows the flake and its modules passing through `mkImages` into its four outputs.
The image directory is what the inventory names; the OCI layout goes to a registry.

- `images.<variant>-<platform>` is the image directory of a variant on a platform, such as
  `images.node-metal`.
- `oci.<variant>-<platform>` is the same image as an OCI image layout, as `chalkbuild oci` writes
  it. [Publish images to a registry](publish-images.md) pushes it.
- `installer` is one directory with a raw image and a hybrid ISO, which installs images of every
  platform. [Customise the installer](customise-installer.md) adds to it.
- `nixos.<variant>-<platform>` and `nixos.installer` are the evaluated NixOS systems. They read an
  image's options, such as `nixos.node-metal.config.chalkos.kubernetes.package.version`, and
  their `config.system.build` holds parts of the image that build on their own, such as its
  schemas, `chalkosSchemas`.

A flake exposes whichever it needs. The homelab's exposes its two image directories and the
installer by name. A flake can also expose every image directory and the installer at once:

```nix
packages.x86_64-linux = images.images // { inherit (images) installer; };
```

The homelab's flake also exports the NixOS systems, as `nixosConfigurations`:

```nix
nixosConfigurations = images.nixos;
```

The guides use them to build one part of an image without the rest, such as its schemas with
`nix build .#nixosConfigurations.node-metal.config.system.build.chalkosSchemas`.

## Check that it worked

`chalkctl validate` checks every node's config files against the schemas of the image the
inventory names for it. It reaches no node and needs no secrets:

```sh
chalkctl validate
```

For the homelab it prints a line per node, with the image's root hash and the hash of the node's
config:

```text
node1: valid for the image node-metal (node on metal, root hash d871203debfe), config c2bef3c1242a847b16346d3e1f6de732a86832fa8294ce90e923bad30c85e6bf
node2: valid for the image node-metal (node on metal, root hash d871203debfe), config afa765d5e8029dbe3aea1a589d2dafdf260de42fff21692656891ddde062d567
node3: valid for the image node-metal (node on metal, root hash d871203debfe), config 719604db3fd2c0634df350fc6b7760bba57de0ec82ac3fb9a9b2dcf532cfdc43
router: valid for the image router-metal (router on metal, root hash e9be19557973), config a8d132f3d5517185654d573c02c543497ab42d0068131d25bbfe8405d5761093
```

A document of a kind the image does not declare, or a misspelled field, fails here. The image is
then ready to install on a machine with
[Install a node](../operate/install.md), or to roll out to running
nodes with [Upgrade nodes](../operate/upgrade.md).

## What next

- [Variants and platforms](variants-and-platforms.md) builds images for nodes that run other
  software or other machines.
- [Add software, drivers and kernel modules](add-software.md) adds to the image.
- [The image](../how-it-works/node/image.md) explains what the raw image holds and how it boots.
