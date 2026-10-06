# Disk layout: ESP, store slots A and B (verity hash + erofs data), STATE, VAR.
{
  config,
  lib,
  pkgs,
  modulesPath,
  ...
}:
let
  cfg = config.chalkos.disk;
  inherit (pkgs.stdenv.hostPlatform) efiArch;
  inherit (config.image.repart.verityStore) partitionIds;
  fixed = size: {
    SizeMinBytes = size;
    SizeMaxBytes = size;
  };
  arch =
    {
      x86_64 = "x86-64";
      arm64 = "arm64";
    }
    .${pkgs.stdenv.hostPlatform.linuxArch};
  luksTPM = name: {
    device = "/dev/disk/by-partlabel/${name}";
    crypttabExtraOpts = [ "tpm2-device=auto" ];
  };
in
{
  imports = [ "${modulesPath}/image/repart.nix" ];

  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.disk = {
        espSize = lib.mkOption {
          type = lib.types.str;
          default = "1G";
          description = "Size of the EFI system partition, which holds the UKIs of both slots.";
        };
        storeSize = lib.mkOption {
          type = lib.types.str;
          default = "3G";
          description = "Size of each store slot's erofs data partition.";
        };
        storeVeritySize = lib.mkOption {
          type = lib.types.str;
          default = "128M";
          description = "Size of each store slot's dm-verity hash partition.";
        };
        stateSize = lib.mkOption {
          type = lib.types.str;
          default = "128M";
          description = "Size of the STATE partition holding node identity and secrets.";
        };
        varMinSize = lib.mkOption {
          type = lib.types.str;
          default = "4G";
          description = "Minimum size of the VAR partition; it grows to fill the disk.";
        };
      };
    };
  };

  config = {
    image.repart = {
      enable = true;
      name = "chalkos";
      verityStore.enable = true;
      # repart formats erofs with the 512-byte sector size, and libblkid rejects checksummed erofs
      # with blocks of 1 KiB or less; without a detected filesystem, udev never marks the verity
      # device ready and the initrd times out waiting for /dev/mapper/usr.
      mkfsOptions.erofs = [ "-b 4096" ];
      partitions = {
        ${partitionIds.esp} = {
          contents."/EFI/BOOT/BOOT${lib.toUpper efiArch}.EFI".source =
            "${config.systemd.package}/lib/systemd/boot/efi/systemd-boot${efiArch}.efi";
          repartConfig = {
            Type = "esp";
            Format = "vfat";
          }
          // fixed cfg.espSize;
        };
        ${partitionIds.store-verity}.repartConfig = {
          Minimize = "off";
        }
        // fixed cfg.storeVeritySize;
        ${partitionIds.store}.repartConfig = {
          Minimize = "off";
        }
        // fixed cfg.storeSize;
      };
    };

    # First boot: add slot B, STATE, and VAR behind the partitions the image ships with.
    # Definitions match existing partitions by type, in file-name order.
    systemd.repart.partitions = {
      "00-esp" = {
        Type = "esp";
      }
      // fixed cfg.espSize;
      "10-store-verity-a" = {
        Type = "usr-${arch}-verity";
      }
      // fixed cfg.storeVeritySize;
      "20-store-a" = {
        Type = "usr-${arch}";
      }
      // fixed cfg.storeSize;
      "30-store-verity-b" = {
        Type = "usr-${arch}-verity";
        Label = "_empty";
      }
      // fixed cfg.storeVeritySize;
      "40-store-b" = {
        Type = "usr-${arch}";
        Label = "_empty";
      }
      // fixed cfg.storeSize;
      "50-state" = {
        Type = "linux-generic";
        Label = "state";
        Format = "ext4";
        Encrypt = "tpm2";
      }
      // fixed cfg.stateSize;
      "60-var" = {
        Type = "linux-generic";
        Label = "var";
        Format = "ext4";
        Encrypt = "tpm2";
        SizeMinBytes = cfg.varMinSize;
      };
    };

    boot.initrd = {
      # The root is tmpfs, so name the disk systemd-boot was loaded from for repart.
      services.udev.rules = ''
        SUBSYSTEM=="block", ENV{DEVTYPE}=="disk", ENV{ID_PART_GPT_AUTO_ROOT_DISK}=="1", SYMLINK+="disk/chalk-boot"
      '';
      systemd.repart = {
        enable = true;
        device = "/dev/disk/chalk-boot";
        extraArgs = [ "--tpm2-pcrs=7" ];
      };
      # Volumes must exist before cryptsetup tries to open them on first boot.
      systemd.services.systemd-repart.before = [
        "systemd-cryptsetup@state.service"
        "systemd-cryptsetup@var.service"
      ];
      availableKernelModules = [ "dm_crypt" ];
      luks.devices = {
        state = luksTPM "state";
        var = luksTPM "var";
      };
    };

    fileSystems."/state" = {
      device = "/dev/mapper/state";
      fsType = "ext4";
      neededForBoot = true;
    };
    fileSystems."/var" = {
      device = "/dev/mapper/var";
      fsType = "ext4";
      neededForBoot = true;
    };
  };
}
