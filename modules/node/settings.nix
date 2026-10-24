# What an image is: its variant and its platform, which mkImages sets. One image serves every
# node of its variant and platform, so nothing here is a node's: a node's values reach it as
# config documents, which units read with the kinds declared under chalkos.kinds.
{ lib, ... }:
{
  options.chalkos.variant.name = lib.mkOption {
    type = lib.types.nullOr lib.types.str;
    default = null;
    description = ''
      The image's variant, which its os-release names as `CHALKOS_VARIANT` and its image
      directory's `metadata.json` as `variant`. mkImages sets it to the variant's name. null
      on the installer, which is no variant. No check compares it: a node may change its
      variant through an upgrade, and the image's schemas decide which config it takes.
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
