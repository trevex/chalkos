# System region: ESP, store slots A and B (verity hash + erofs data), and STATE. VAR and further
# volumes come from the node's storage section (see storage.nix).
{
  config,
  lib,
  pkgs,
  modulesPath,
  ...
}:
let
  cfg = config.chalkos.disk;
  inherit (import ../../cluster/storage.nix { inherit lib; }) partitionType;
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
  # Links to the disk systemd-boot was loaded from and to its partitions by label. A second disk
  # carrying the same image or the same labels never gets these links.
  bootDiskRules = ''
    SUBSYSTEM=="block", ENV{DEVTYPE}=="disk", ENV{ID_PART_GPT_AUTO_ROOT_DISK}=="1", SYMLINK+="disk/chalk-boot-disk"
    SUBSYSTEM=="block", ENV{DEVTYPE}=="partition", ENV{ID_PART_GPT_AUTO_ROOT_DISK}=="1", ENV{ID_PART_ENTRY_NAME}=="?*", SYMLINK+="disk/chalk-boot/$env{ID_PART_ENTRY_NAME}"
  '';
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

    # First boot: add slot B and STATE behind the partitions the image ships with.
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
        # A type of its own, like VAR and volumes, so no other definition takes STATE over.
        Type = partitionType "state";
        Label = "state";
        Format = "ext4";
        Encrypt = "tpm2";
      }
      // fixed cfg.stateSize;
    };

    boot.initrd = {
      # The root is tmpfs, so name the disk systemd-boot was loaded from for repart.
      services.udev.rules = bootDiskRules;
      systemd.repart = {
        enable = true;
        device = "/dev/disk/chalk-boot-disk";
        extraArgs = [ "--tpm2-pcrs=7" ];
      };
    };
    services.udev.extraRules = bootDiskRules;
  };
}
