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
  inherit (pkgs.chalkos) chalkos-storage;
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
  # chalkos-storage under another name with arguments of its own, as a compiled wrapper: nodes
  # carry no shell.
  wrap =
    name: args:
    pkgs.runCommand name { nativeBuildInputs = [ pkgs.buildPackages.makeBinaryWrapper ]; } ''
      makeBinaryWrapper ${exe} $out ${args}
    '';
  # chalkos-storage facts runs in chalkd's sandbox, whose PATH need not hold cryptsetup.
  facts = wrap "chalkos-storage-facts" "--set PATH ${
    lib.makeBinPath [ pkgs.cryptsetup ]
  } --add-flags facts";
  # The generator's --exec-path flags, one per exec path (exec-paths.nix).
  execPathFlags = lib.mapAttrsToList (
    path: p:
    assert lib.assertMsg (
      builtins.match "/[^ =']+" path != null
    ) "chalkos.storage.execPaths: ${path} is no absolute path without spaces, = or '";
    "--exec-path ${path}=exec,${lib.optionalString (!p.suid) "no"}suid,${
      lib.optionalString (!p.dev) "no"
    }dev"
  ) config.chalkos.storage.execPaths;
in
{
  # What the storage kinds' rules read of the machine: the storage section STATE records, what
  # the delivered disk references find, and whether the fallback secret opens the node's keyslot.
  # The rules are in kinds/builtin/storage.nix.
  chalkos.facts = {
    recorded.file = "/state/storage/storage.json";
    disks.command = "${facts} disks";
    fallback = {
      command = "${facts} fallback";
      inputs = [ "fallbackSecret" ];
    };
  };

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
          before = [
            "initrd-fs.target"
            # initrd-parse-etc.service reloads systemd and restarts initrd-fs.target; once it ends,
            # initrd-cleanup.service stops udevd for the switch to the booted system. STATE opens
            # before either: systemd-cryptsetup waits for udevd, and a reload during the open has
            # let the initrd reach initrd.target without STATE, leaving the unlock waiting forever.
            "initrd-parse-etc.service"
          ];
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
          before = [
            "initrd-fs.target"
            # Unlocks volumes too; see chalkos-state.
            "initrd-parse-etc.service"
          ];
          requiredBy = [ "initrd-fs.target" ];
          # repart keeps large temporary files in /var/tmp by default, which is not there yet.
          environment.TMPDIR = "/tmp";
          serviceConfig.ExecStart = "${exe} initrd";
        }
      ];
    };
  };

  # btrfs support puts btrfs-progs in the initrd, the system and the fsck and makefs units; this is
  # a copy without fsck.btrfs, a bash script that checks nothing. systemd-fsck skips a file system
  # whose fsck.<type> is missing. The tools reference nothing of the original and btrfsck links to
  # btrfs within the copy, so btrfs-progs is not rebuilt. containerd builds against btrfs-progs's
  # headers and references nothing of it: built against the original, it stays the binary
  # cache's. The overlay goes first, as Kubernetes's module overlays containerd with a copy of its
  # binaries.
  nixpkgs.overlays = lib.mkBefore [
    (final: prev: {
      btrfs-progs = final.chalkos.trimPackage {
        from = prev.btrfs-progs;
        drop = [ "bin/fsck.btrfs" ];
      };
      containerd = prev.containerd.override { inherit (prev) btrfs-progs; };
    })
  ];

  # The booted system mounts the remaining volumes of other file systems and swap too.
  boot.supportedFilesystems = {
    xfs = true;
    btrfs = true;
  };

  systemd.generators.chalkos-storage = wrap "chalkos-storage-generator" "--add-flags '${
    lib.concatStringsSep " " (
      [
        "generate"
        "--cryptsetup"
        "${config.systemd.package}/bin/systemd-cryptsetup"
      ]
      ++ execPathFlags
    )
  }'";
}
