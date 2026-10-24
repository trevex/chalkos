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
