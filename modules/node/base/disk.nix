# System region: ESP, store slots A and B (verity hash + erofs data), and STATE. VAR and further
# volumes come from the node's storage section (see storage.nix).
{
  config,
  lib,
  pkgs,
  modulesPath,
  utils,
  ...
}:
let
  cfg = config.chalkos.disk;
  inherit (import ../../cluster/storage.nix { inherit lib; }) partitionType;
  inherit (pkgs.stdenv.hostPlatform) efiArch;
  inherit (config.image.repart.verityStore) partitionIds;
  # The system region's repart definitions, built as NixOS builds them for the initrd.
  systemDefinitions = utils.systemdUtils.lib.definitions "repart.d" (pkgs.formats.ini {
    listsAsDuplicateKeys = true;
  }) (lib.mapAttrs (_: partition: { Partition = partition; }) config.systemd.repart.partitions);
  fixed =
    size:
    lib.optionalAttrs (size != null) {
      SizeMinBytes = size;
      SizeMaxBytes = size;
    };
  # A store partition of the image; null sizes it to its contents.
  storePartition = size: { Minimize = if size == null then "best" else "off"; } // fixed size;
  # A size as chalkos.disk takes it, in bytes: a number, with a fraction or not, and a unit of
  # powers of 1024 or none; null for anything else.
  bytes =
    size:
    let
      m = builtins.match "([0-9]+(\\.[0-9]+)?) *([BKMGT]?)" size;
      units = {
        "" = 1;
        B = 1;
        K = 1024;
        M = 1024 * 1024;
        G = 1024 * 1024 * 1024;
        T = 1024 * 1024 * 1024 * 1024;
      };
    in
    if m == null then null else builtins.fromJSON (builtins.elemAt m 0) * units.${builtins.elemAt m 2};
  # systemd-repart formats a vfat ESP with no less than 260 MiB, whatever its definition says, so
  # a smaller one would not match the definitions an installed disk is compared with.
  espBytes = bytes cfg.espSize;
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
  # An image leaves room for the next one: the build fails when the store's data takes more than
  # 80% of a slot, its hash tree more than 80% of the slot's verity partition, or the UKIs the ESP
  # holds more than 80% of it. That is three UKIs on an image that is upgraded, both slots' and an
  # upgrade's temporary file, and one otherwise.
  fits =
    let
      name = lib.defaultTo config.image.repart.name config.chalkos.role.name;
      image = config.system.build.image;
    in
    pkgs.runCommand "${name}-fits"
      {
        nativeBuildInputs = [ (pkgs.callPackage ../../../nix/image-size.nix { }) ];
        ukis = if cfg.storeSize == null then 1 else 3;
        storeSize = lib.defaultTo "-" cfg.storeSize;
        storeVeritySize = lib.defaultTo "-" cfg.storeVeritySize;
        inherit (cfg) espSize;
      }
      ''
        chalkos-image-size fits ${name} ${image}/${config.image.fileName} ${image}/repart-output.json \
          ${config.system.build.uki}/${config.system.boot.loader.ukiFile} "$ukis" "$storeSize" "$storeVeritySize" "$espSize"
        touch $out
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
          description = "Size of the EFI system partition, which holds the UKIs of both slots; at least 260M, the smallest systemd-repart formats as vfat.";
        };
        storeSize = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = "3G";
          description = ''
            Size of each store slot's erofs data partition; null sizes it to its contents, for
            images that are never upgraded.
          '';
        };
        storeVeritySize = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = "128M";
          description = ''
            Size of each store slot's dm-verity hash partition; null sizes it to its contents. The
            image build fails when the hash tree takes more than 80% of it. With 4 KiB blocks the
            tree needs up to about 1/128 of storeSize.
          '';
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
    assertions = [
      {
        assertion = espBytes != null && espBytes >= 260 * 1024 * 1024;
        message = "chalkos.disk.espSize must be at least 260M, the smallest ESP systemd-repart formats as vfat; it is ${builtins.toJSON cfg.espSize}";
      }
    ];

    image.repart = {
      enable = true;
      name = lib.mkDefault "chalkos";
      verityStore.enable = true;
      mkfsOptions.erofs = [
        # repart formats erofs with the 512-byte sector size, and libblkid rejects checksummed
        # erofs with blocks of 1 KiB or less; without a detected filesystem, udev never marks the
        # verity device ready and the initrd times out waiting for /dev/mapper/usr.
        "-b 4096"
        # zstd 9 with 64 KiB clusters comes within 3% of zstd 15's size at a sixth of its build
        # time. The kernel's erofs reads zstd and deflate, not LZMA. Upgrades send the compressed
        # store.
        "-zzstd,level=9"
        "-C65536"
      ];
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
        # A slot's labels name the version it holds; an upgrade labels the slot it writes the
        # same way. The boot finds the store by partition UUID, never by label.
        ${partitionIds.store-verity}.repartConfig = storePartition cfg.storeVeritySize // {
          Label = "store-verity_${config.system.image.version}";
          # dm-verity refuses blocks smaller than the disk's logical block size, so 512-byte ones
          # would not open on 4Kn disks. 4 KiB blocks also make the tree, which every upgrade
          # sends, an eighth of the size.
          VerityDataBlockSizeBytes = 4096;
          VerityHashBlockSizeBytes = 4096;
        };
        ${partitionIds.store}.repartConfig = storePartition cfg.storeSize // {
          Label = "store_${config.system.image.version}";
        };
      };
    };

    # First boot: add slot B and STATE behind the partitions the image ships with.
    # Definitions match existing partitions by type, in file-name order. The installer lays out a
    # whole disk with them, so they format the ESP, which repart never does to an existing one.
    systemd.repart.partitions = {
      "00-esp" = {
        Type = "esp";
        Format = "vfat";
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
      # The UKI carries the initrd to the ESP and in every upgrade: zstd 19 makes it a tenth
      # smaller than NixOS's level 10.
      compressorArgs = [
        "-19"
        "-T0"
      ];
      # The root is tmpfs, so name the disk systemd-boot was loaded from for repart.
      services.udev.rules = bootDiskRules;
      systemd.repart = {
        enable = true;
        device = "/dev/disk/chalk-boot-disk";
        extraArgs = [ "--tpm2-pcrs=7" ];
      };
    };
    services.udev.extraRules = bootDiskRules;

    # The ESP, where upgrades write UKIs and systemd-bless-boot marks a boot good. It is mounted
    # when used and unmounted again once idle, so a power cut rarely finds its FAT in use.
    fileSystems."/efi" = {
      device = "/dev/disk/chalk-boot/esp";
      fsType = "vfat";
      options = [
        "umask=0077"
        "nofail"
        "x-systemd.automount"
        "x-systemd.idle-timeout=1min"
      ];
    };
    # systemd-boot-random-seed.service refreshes the boot loader's random seed there, and
    # systemd-bless-boot.service marks a boot good there rather than searching for it.
    boot.loader.efi.efiSysMountPoint = "/efi";
    systemd.services.systemd-bless-boot.environment.SYSTEMD_ESP_PATH = "/efi";

    # Install recreates STATE from these definitions, and the installer writes a role image's
    # system region with them, so they travel with the image.
    environment.etc."chalkos/repart.d".source = systemDefinitions;
    system.build.chalkosImageFits = fits;
    system.build.chalkosImage = pkgs.runCommand "${config.image.repart.name}-image" { inherit fits; } ''
      mkdir $out
      ln -s ${config.system.build.image}/* $out/
      ln -s ${systemDefinitions} $out/repart.d
    '';
  };
}
