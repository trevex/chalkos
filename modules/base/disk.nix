# Disk layout: ESP, then store slot A (verity hash + erofs data).
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
in
{
  imports = [ "${modulesPath}/image/repart.nix" ];

  options.chalkos.disk = {
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
  };
}
