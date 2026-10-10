# What an image needs to be upgraded to and from. The image describes itself in os-release, which
# its UKI carries signed: chalkd checks an upgrade's cluster and role against its own, and takes
# the boot tries from the image it installs. Versions name the UKI and the store partitions, so
# they are restricted to what GPT labels and systemd-boot's entry IDs hold unchanged. A boot the
# boot loader counts is blessed only once chalkd found the node healthy.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.chalkos.upgrade;
  chalkd = pkgs.callPackage ../../nix/chalkd.nix { };
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
        assertion = validVersion;
        message = "system.image.version must be 1 to 23 characters of a-z, 0-9, '.', '~', '^' and '-', starting with a letter or digit; it is ${builtins.toJSON version}";
      }
    ];
    # boot-complete.target is pulled in on boots the boot loader counts, by
    # systemd-bless-boot.service, which marks the boot good once the target is reached: only once
    # the node is healthy. A counted boot that hangs before this unit starts, as at a prompt in the
    # initrd to unlock a disk or in chalkos-identity, is never decided by the check: the node stays
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
      CHALKOS_CLUSTER = config.chalkos.cluster.name;
      CHALKOS_BOOT_TRIES = toString cfg.bootTries;
    }
    // lib.optionalAttrs (config.chalkos.role.name != null) {
      CHALKOS_ROLE = config.chalkos.role.name;
    }
    // lib.optionalAttrs (config.chalkos.platform.name != null) {
      CHALKOS_PLATFORM = config.chalkos.platform.name;
    };
  };
}
