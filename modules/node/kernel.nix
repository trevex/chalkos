# The kernel modules of a role's image: a filtered copy of nixpkgs's prebuilt module tree, so the
# kernel and the binary cache stay as they are. Groups name directories of the tree where they
# can, so a kernel update brings a subsystem's new drivers along; module names fill the gaps.
# Modules need no signatures: they live in the dm-verity store, whose root hash the signed UKI
# pins. The initrd's modules come from the same tree.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.chalkos.kernel;
  inherit (pkgs.stdenv.hostPlatform) isx86_64;
  tool = pkgs.callPackage ../../nix/kernel-modules.nix { };
  full = lib.getOutput "modules" config.boot.kernelPackages.kernel;

  groups = {
    storage = {
      directories = [
        "drivers/ata"
        "drivers/nvme"
        "drivers/scsi"
        "drivers/message/fusion"
        "drivers/block"
        "drivers/md"
        "drivers/usb/storage"
        "drivers/cdrom"
        "drivers/mmc"
      ];
    };
    network = {
      directories = [
        "drivers/net/ethernet"
        "drivers/net/phy"
        "drivers/net/mdio"
        "drivers/net/pcs"
        "drivers/net/usb"
        "drivers/net/bonding"
        "drivers/net/team"
        "drivers/net/vxlan"
        "drivers/net/wireguard"
        "drivers/net/ipvlan"
        "net/8021q"
        "net/bridge"
        "net/ipv4"
        "net/ipv6"
        "net/xfrm"
        "net/key"
        "net/tls"
      ];
      modules = [
        # networkd's DHCP client and chalkd's address announcements use packet sockets.
        "af_packet"
        "macvlan"
        "macvtap"
        "tun"
        "veth"
        "dummy"
        "geneve"
        "ifb"
        "vrf"
        "netconsole"
      ];
    };
    virtualisation = {
      directories = [
        "drivers/virtio"
        "drivers/hv"
        "drivers/net/hyperv"
        "drivers/net/vmxnet3"
        "drivers/xen"
        "drivers/virt"
        "drivers/misc/vmw_vmci"
        "net/vmw_vsock"
      ];
      modules = [
        "virtio_net"
        "virtio_blk"
        "virtio_scsi"
        "virtio_console"
        "virtio_rng"
        "xen_netfront"
        "xen_blkfront"
        "hv_storvsc"
        "hyperv_keyboard"
        "vmw_pvscsi"
        "qemu_fw_cfg"
        # AWS's ENA and Google's gVNIC.
        "ena"
        "gve"
      ]
      ++ lib.optional isx86_64 "ptp_kvm";
    };
    filesystems = {
      directories = [
        "fs/ext4"
        "fs/xfs"
        "fs/btrfs"
        "fs/fat"
        "fs/nls"
        "fs/erofs"
        "fs/overlayfs"
        "fs/fuse"
        "fs/nfs"
        "fs/isofs"
        # systemd mounts these: efivars, which chalkd writes, the ESP's automount and configfs.
        "fs/efivarfs"
        "fs/autofs"
        "fs/configfs"
      ];
      modules = [ "ext2" ];
    };
    kubernetes = {
      directories = [
        "net/netfilter"
        "net/ipv4/netfilter"
        "net/ipv6/netfilter"
        "net/bridge"
        "net/sched"
        "net/sctp"
        "net/openvswitch"
      ];
      modules = [
        "overlay"
        "veth"
        "vxlan"
        "geneve"
        "ipip"
        "dummy"
      ];
    };
    platform = {
      directories = [
        "drivers/char/tpm"
        "drivers/char/ipmi"
        "drivers/char/hw_random"
        "drivers/watchdog"
        "drivers/edac"
        "drivers/hwmon"
        "drivers/acpi"
        "drivers/firmware"
        "drivers/cpufreq"
        "drivers/powercap"
        "drivers/thermal"
        "drivers/dma"
        "drivers/pci"
        "drivers/crypto"
        "crypto"
        "fs/pstore"
        # The console's keyboard.
        "drivers/hid"
        "drivers/input/keyboard"
        "drivers/input/serio"
        "drivers/usb/host"
      ]
      ++ lib.optionals isx86_64 [
        "arch/x86/crypto"
        "arch/x86/events"
        "arch/x86/kernel"
      ];
      modules = [
        "evdev"
        "input_leds"
      ];
    };
    gpu.directories = [
      "drivers/gpu"
      "drivers/accel"
      "drivers/video"
    ];
    sound.directories = [
      "sound"
      "drivers/soundwire"
    ];
    media.directories = [ "drivers/media" ];
    wireless.directories = [
      "drivers/net/wireless"
      "net/wireless"
      "net/mac80211"
      "net/rfkill"
      "drivers/bluetooth"
      "net/bluetooth"
    ];
    infiniband.directories = [ "drivers/infiniband" ];
    can.directories = [
      "drivers/net/can"
      "net/can"
    ];
    industrial.directories = [ "drivers/iio" ];
  };
  baseGroups = [
    "storage"
    "network"
    "virtualisation"
    "filesystems"
    "kubernetes"
    "platform"
  ];

  selected = map (name: groups.${name}) (lib.unique cfg.moduleGroups);
  directories = lib.unique (lib.concatMap (g: g.directories or [ ]) selected);
  names = lib.unique (lib.concatMap (g: g.modules or [ ]) selected ++ cfg.extraModules);

  filtered =
    pkgs.runCommand "${config.boot.kernelPackages.kernel.name}-modules-filtered"
      {
        directories = lib.concatLines directories;
        names = lib.concatLines names;
        passAsFile = [
          "directories"
          "names"
        ];
        passthru = { inherit directories names; };
      }
      ''
        ${lib.getExe tool} filter ${full} $out $directoriesPath $namesPath
      '';

  # What the image loads by name, which the tree must hold.
  loaded = lib.unique (
    config.boot.kernelModules
    ++ config.boot.initrd.kernelModules
    ++ config.boot.initrd.availableKernelModules
  );
  check =
    pkgs.runCommand "kernel-modules-check"
      {
        loaded = lib.concatLines loaded;
        passAsFile = [ "loaded" ];
      }
      ''
        ${lib.getExe tool} check ${config.system.modulesTree} $loadedPath
        touch $out
      '';
in
{
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.kernel = {
        moduleGroups = lib.mkOption {
          type = lib.types.listOf (lib.types.enum (lib.attrNames groups));
          description = ''
            Groups of kernel modules the image carries. The base groups are set by default and
            a role's definitions add to them, for example `[ "gpu" ]`; `lib.mkForce` replaces
            them. Base: ${lib.concatStringsSep ", " baseGroups}. Further:
            ${lib.concatStringsSep ", " (lib.subtractLists baseGroups (lib.attrNames groups))}.
            Modules from `boot.extraModulePackages` are always included whole.
          '';
        };
        extraModules = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
          example = [ "kvm_amd" ];
          description = ''
            Kernel modules the image carries besides its groups, with what they depend on. A name
            the kernel does not know fails the build.
          '';
        };
        allModules = lib.mkOption {
          type = lib.types.bool;
          default = false;
          description = "Carry every module of the kernel instead of the groups and names.";
        };
      };
    };
  };

  config = {
    chalkos.kernel.moduleGroups = baseGroups;
    system.modulesTree = lib.mkIf (!cfg.allModules) (
      lib.mkForce ([ filtered ] ++ config.boot.extraModulePackages)
    );
    # A module the image loads that its tree lacks fails the build, not the boot.
    system.checks = [ check ];
    system.build.chalkosKernelModules = if cfg.allModules then full else filtered;
  };
}
