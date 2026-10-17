# What an image needs to be upgraded to and from. The image describes itself in os-release, which
# its UKI carries signed: chalkd checks an upgrade's platform against its own, and takes the boot
# tries from the image it installs. An image has no version: its store's root hash identifies
# it, and chalkd numbers the UKIs it installs by generation, the highest of which systemd-boot
# boots first. A boot the boot loader counts is blessed only once chalkd found the node healthy.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.chalkos.upgrade;
  chalkd = pkgs.callPackage ../../nix/chalkd.nix { };
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
        healthTimeout = lib.mkOption {
          type = lib.types.ints.positive;
          default = 300;
          description = ''
            Seconds a boot of this image that systemd-boot counts has to become healthy. Then
            chalkd reboots the node, so the boot loader tries again or falls back.
          '';
        };
        healthIgnoreUnits = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
          example = [ "example-sync.service" ];
          description = ''
            Units of the boot whose failure leaves a node without Kubernetes healthy. Only units
            multi-user.target and sysinit.target pull in count; jobs that timers and sockets start
            never do.
          '';
        };
      };
    };
  };

  config = {
    assertions = [
      {
        assertion = config.system.image.version == null;
        message = "chalkos orders boot entries by generation; pass revision and tag to mkImages for provenance";
      }
      # The image ID starts the UKIs' names, <image ID>_<generation>.efi. systemd-boot lowercases
      # only the IDs of entries without a tries counter, and takes the last "+" of a name to start
      # the counter, so an ID in other characters would name one image's entries in two ways.
      {
        assertion =
          config.system.image.id != null && builtins.match "[a-z0-9._-]+" config.system.image.id != null;
        message = "system.image.id must consist of lower-case letters, digits, \".\", \"_\" and \"-\", as it names the image's boot entries";
      }
    ];
    # boot-complete.target is pulled in on boots the boot loader counts, by
    # systemd-bless-boot.service, which marks the boot good once the target is reached: only once
    # the node is healthy. A counted boot that hangs before this unit starts, as at a prompt in the
    # initrd to unlock a disk or in chalkos-config, is never decided by the check: the node stays
    # in that boot until it is reset, and only then does the boot loader try the entry again or
    # fall back.
    systemd.services.chalkos-health = {
      description = "Decide whether this boot is healthy";
      requiredBy = [ "boot-complete.target" ];
      before = [ "boot-complete.target" ];
      after = [ "chalkd.service" ];
      path = [ config.systemd.package ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${lib.getExe chalkd} health --wait ${toString cfg.healthTimeout}${
          lib.concatMapStrings (unit: " --ignore-unit ${lib.escapeShellArg unit}") cfg.healthIgnoreUnits
        }";
        TimeoutStartSec = cfg.healthTimeout + 60;
        # A check that ended before it decided, stopped by the timeout above or crashed, is handled
        # as an unhealthy boot: rebooted only when that leads the boot loader on. FailureAction=reboot would also
        # reboot a used-up entry booted for lack of another, which systemd-boot counts and boots
        # again, in a loop.
        ExecStopPost = "${lib.getExe chalkd} health --stopped";
      };
    };

    system.nixos.extraOSReleaseArgs = {
      # systemd-boot orders the UKIs of one image ID by IMAGE_VERSION before their IDs, and by
      # VERSION when IMAGE_VERSION is missing, as NixOS leaves it without a version. An empty one
      # leaves the order to the generations in the UKIs' names.
      IMAGE_VERSION = "";
      CHALKOS_BOOT_TRIES = toString cfg.bootTries;
    }
    // lib.optionalAttrs (config.chalkos.variant.name != null) {
      CHALKOS_VARIANT = config.chalkos.variant.name;
    }
    // lib.optionalAttrs (config.chalkos.platform.name != null) {
      CHALKOS_PLATFORM = config.chalkos.platform.name;
    };
  };
}
