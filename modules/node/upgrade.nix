# What an image needs to be upgraded to and from. The image describes itself in os-release, which
# its UKI carries signed: chalkd checks an upgrade's cluster and role against its own, and takes
# the boot tries from the image it installs. Versions name the UKI and the store partitions, so
# they are restricted to what GPT labels and systemd-boot's entry IDs hold unchanged.
{ config, lib, ... }:
let
  cfg = config.chalkos.upgrade;
  inherit (config.system.image) version;
  # GPT labels hold 36 characters, of which "store-verity_" takes 13. systemd-boot folds entry IDs
  # to lower case only when the name has no tries counter, and "+" starts that counter.
  validVersion = version != null && builtins.match "[a-z0-9][a-z0-9.~^-]{0,22}" version != null;
in
{
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.upgrade = {
        bootTries = lib.mkOption {
          type = lib.types.ints.positive;
          default = 3;
          description = ''
            How often systemd-boot boots this image after an upgrade installed it before it falls
            back to the image the node ran before, unless a boot was found healthy first.
          '';
        };
      };
    };
  };

  config = {
    assertions = [
      {
        assertion = validVersion;
        message = "system.image.version must be 1 to 23 characters of a-z, 0-9, '.', '~', '^' and '-', starting with a letter or digit; it is ${builtins.toJSON version}";
      }
    ];
    system.nixos.extraOSReleaseArgs = {
      CHALKOS_CLUSTER = config.chalkos.cluster.name;
      CHALKOS_BOOT_TRIES = toString cfg.bootTries;
    }
    // lib.optionalAttrs (config.chalkos.role.name != null) {
      CHALKOS_ROLE = config.chalkos.role.name;
    };
  };
}
