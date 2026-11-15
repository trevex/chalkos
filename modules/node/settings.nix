# What an image is: its name and its platform, which mkImages sets. One image serves every
# node that runs it, so nothing here is a node's: a node's values reach it as
# config documents, which units read with the kinds declared under chalkos.kinds.
{ lib, ... }:
{
  options.chalkos.image.name = lib.mkOption {
    type = lib.types.nullOr lib.types.str;
    default = null;
    description = ''
      The image's name, which mkImages sets, recorded as `CHALKOS_IMAGE` in its os-release and
      as `name` in its image directory's `metadata.json`. `chalkctl upgrade --image` upgrades
      the nodes running an image of this name. null on the installer, which is no node image.
      No check compares it: a node may change the image it runs through an upgrade, and the
      image's schemas decide which config it takes.
    '';
  };
  options.chalkos.platform.name = lib.mkOption {
    type = lib.types.nullOr (lib.types.strMatching "[a-z0-9][a-z0-9-]*");
    default = null;
    description = ''
      The platform the image is built for, a name of mkImages's `platforms`, which mkImages
      sets; null on the installer, which installs images of every platform. A node refuses an
      image of another platform, and translates its config for its own.
    '';
  };
}
