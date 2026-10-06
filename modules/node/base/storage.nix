# Opens STATE and the volumes of the node's storage section. In the initrd, chalkos-storage
# unlocks and mounts STATE, then creates the volumes recorded on it and mounts VAR; in the
# booted system its generator writes units for the remaining volumes.
{
  config,
  lib,
  pkgs,
  utils,
  ...
}:
let
  chalkos-storage = pkgs.callPackage ../../../nix/chalkos-storage.nix { };
  exe = lib.getExe chalkos-storage;
  initrdSystemd = config.boot.initrd.systemd.package;
  # Tools chalkos-storage and repart run in the initrd, besides mount, udevadm, systemd-repart
  # and systemd-cryptsetup, which other modules provide.
  initrdTools = {
    e2fsck = "${pkgs.e2fsprogs}/bin/e2fsck";
    blkid = "${initrdSystemd.util-linux}/bin/blkid";
    mkswap = "${initrdSystemd.util-linux}/bin/mkswap";
    sfdisk = "${initrdSystemd.util-linux}/bin/sfdisk";
    systemd-growfs = "${initrdSystemd}/lib/systemd/systemd-growfs";
  };
  oneshot = {
    unitConfig.DefaultDependencies = false;
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      # Unlock prompts and failures must reach the console.
      StandardOutput = "journal+console";
      StandardError = "journal+console";
    };
  };
  stateDevice = "${utils.escapeSystemdPath "/dev/disk/chalk-boot/state"}.device";
in
{
  boot.initrd = {
    # systemd-cryptsetup and the TPM2 token plugin without a crypttab: STATE and VAR are
    # unlocked at runtime because the same image serves encrypted and unencrypted nodes.
    luks.forceLuksSupportInInitrd = true;
    # repart formats volumes in the initrd.
    supportedFilesystems = {
      ext4 = true;
      xfs = true;
      btrfs = true;
    };
    systemd = {
      extraBin = initrdTools;
      storePaths = [ exe ] ++ lib.attrValues initrdTools;

      services.chalkos-state = lib.mkMerge [
        oneshot
        {
          description = "Unlock and mount STATE";
          requires = [ stateDevice ];
          after = [
            stateDevice
            "systemd-repart.service"
            "initrd-root-fs.target"
            # systemd starts tpm2.target once a TPM the firmware reported is usable. Pulling it
            # in would wait for a TPM on nodes that have none.
            "tpm2.target"
          ];
          before = [ "initrd-fs.target" ];
          requiredBy = [ "initrd-fs.target" ];
          serviceConfig.ExecStart = "${exe} state";
        }
      ];

      services.chalkos-storage = lib.mkMerge [
        oneshot
        {
          description = "Create volumes and mount VAR";
          requires = [ "chalkos-state.service" ];
          after = [
            "chalkos-state.service"
            "tpm2.target"
          ];
          before = [ "initrd-fs.target" ];
          requiredBy = [ "initrd-fs.target" ];
          # repart keeps large temporary files in /var/tmp by default, which is not there yet.
          environment.TMPDIR = "/tmp";
          serviceConfig.ExecStart = "${exe} initrd";
        }
      ];
    };
  };

  # The booted system mounts the remaining volumes of other file systems and swap too.
  boot.supportedFilesystems = {
    xfs = true;
    btrfs = true;
  };

  systemd.generators.chalkos-storage = pkgs.writeShellScript "chalkos-storage-generator" ''
    exec ${exe} generate --cryptsetup ${config.systemd.package}/bin/systemd-cryptsetup "$@"
  '';
}
