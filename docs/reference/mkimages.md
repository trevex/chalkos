---
title: "mkImages"
description: "The arguments of chalkos.lib.mkImages and what it returns"
---

# mkImages

This page is for image authors who call `chalkos.lib.mkImages` in a flake. mkImages builds a
cluster's images from its arguments: each variant on each platform, and the installer. The
options the images' NixOS modules set are on [Image options](image-options.md) and
[Extension API](extension-api.md).

A flake calls mkImages and exposes the images to build:

```nix title="flake.nix"
{
  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { chalkos, ... }:
    let
      images = chalkos.lib.mkImages {
        osCA = ./secrets.pub.json;
        platforms = [ "metal" "kvm" ];
        # Every variant's images carry these.
        modules = [ ./site.nix ];
        variants = {
          node = { };
          storage = {
            kubernetes = false;
            modules = [ ./storage.nix ];
          };
        };
        installer.modules = [ ./installer-network.nix ];
      };
    in
    {
      packages.x86_64-linux = images.images // { inherit (images) installer; };
    };
}
```

## Return values { #return-values }

mkImages returns an attribute set:

- `images.<variant>-<platform>`: the image directory of a variant on a platform, such as
  `images.node-metal`, each built only when asked for.
- `oci.<variant>-<platform>`: the image directory as an OCI image layout, as
  [`chalkbuild oci`](cli/chalkbuild_oci.md) writes it, each built only when asked for;
  [Publish images to a registry](../build/publish-images.md) signs and pushes it.
- `installer`: the installer, a raw image and a hybrid ISO in one directory, which installs
  images of every platform.
- `nixos.<variant>-<platform>` and `nixos.installer`: the evaluated NixOS systems, to read an
  image's options, such as `nixos.node-metal.config.chalkos.kubernetes.package.version`.

## mkImages arguments { #mkimages-arguments }

The arguments `chalkos.lib.mkImages` takes, each checked as a module option.

### `installer.modules` { #installermodules }

Type: list of module · Default: `[ ]` · Declared in: [lib/images-arguments.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)

NixOS modules of the installer: systemd-networkd with static addresses, VLANs and bonds
for the machines it boots on, further kernel module groups and firmware, consoles. Secure
Boot fixes the kernel command line, so what differs between machines goes into the image,
matched by MAC address or interface name.

### `modules` { #modules }

Type: list of module · Default: `[ ]` · Declared in: [lib/images-arguments.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)

NixOS modules every variant carries, after its platform's modules and before its own. The
installer does not carry them: it holds no config, so the extensions that read config
have nothing to do there; `installer.modules` are its own.

### `nixpkgs` { #nixpkgs }

Type: any · Default: the nixpkgs chalkos is tested against · Declared in: [lib/images-arguments.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)

nixpkgs whose `lib.nixosSystem` evaluates the images and whose packages they are built from.

### `osCA` { #osca }

Type: path or null · Default: `null` · Declared in: [lib/images-arguments.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)

Public part of the cluster's secrets (`secrets.pub.json` from `chalkctl gen secrets`,
version 3), whose OS CA every image and the installer carry, so
chalkd in maintenance mode accepts only clients with a certificate from it. null builds
images that accept any client until they are installed.

Example: `./secrets.pub.json`

### `platforms` { #platforms }

Type: list of string matching `[a-z0-9][a-z0-9-]*` or map of list of module · Default: `[ "metal" "kvm" ]` · Declared in: [lib/images-arguments.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)

The platforms every variant is built for, exactly those named: a list of names, or an
attribute set of each platform's NixOS modules. Either form replaces the default, so
`{ dell-r650 = [ ./r650.nix ]; }` builds dell-r650 alone, and `metal` and `kvm` are built
only when named. chalkos has modules of its own for `metal` and `kvm`, which their images
carry before the modules given; any other name is a platform of the modules given alone.
An image carries one platform's drivers, agents and console; a node refuses an image of
another platform than its own. A platform's name becomes `CHALKOS_PLATFORM` in its
images' os-release.

Example:

```nix
{
  metal = [ ./firmware.nix ];
  dell-r650 = [ ./r650.nix ];
}
```

### `revision` { #revision }

Type: string or null · Default: `null` · Declared in: [lib/images-arguments.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)

The git revision the images are built from, which every image directory's and the
installer's `metadata.json` record and chalkctl shows beside an image's root hash. It never
reaches an image's store, so it changes no root hash, and nothing orders or refuses images
by it: a node identifies an image by its root hash alone.

Example: `self.rev or self.dirtyRev or null`

### `specialArgs` { #specialargs }

Type: map of any · Default: `{ }` · Declared in: [lib/images-arguments.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)

Arguments every image's NixOS modules receive besides NixOS's own, such as a flake's inputs.

Example: `{ inherit inputs; }`

### `system` { #system }

Type: string · Default: `"x86_64-linux"` · Declared in: [lib/images-arguments.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)

Platform the images are built for, as Nix names it.

### `tag` { #tag }

Type: string or null · Default: `null` · Declared in: [lib/images-arguments.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)

The git tag the images are built from, such as a release's, recorded and shown like
`revision`. Nix cannot read git tags, so whatever builds a release passes it.

Example: `"v1.5.0"`

### `variants` { #variants }

Type: map of submodule · Default: `{ node = { }; }` · Declared in: [lib/images-arguments.nix](https://github.com/trevex/chalkos/blob/main/lib/images-arguments.nix)

Variants of the cluster's images, by name: each is built for every platform as
`images.<variant>-<platform>`. A variant's name becomes `CHALKOS_VARIANT` in its images'
os-release and `variant` in their `metadata.json`.

Example:

```nix
{
  node = { };
  storage = {
    kubernetes = false;
    modules = [ ./storage.nix ];
  };
}
```

The options within each, by their path below `variants`:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `<name>.kubernetes`{ #variantskubernetes } | boolean | `true` | Whether the variant's images carry the Kubernetes layer and run Kubernetes. One image serves control planes and workers: each node's Kubernetes Node document makes it one or the other. |
| `<name>.modules`{ #variantsmodules } | list of module | `[ ]` | NixOS modules of the variant's images, after those of its platform and of every variant. |
| `<name>.router`{ #variantsrouter } | boolean | `false` | Whether the variant's images carry the router layer, which forwards packets between the node's interfaces. An image carries one layer at most, so a router variant also sets `kubernetes = false`. |

Sources: the options of `lib/images-arguments.nix`, which check mkImages's arguments,
through nixosOptionsDoc.
