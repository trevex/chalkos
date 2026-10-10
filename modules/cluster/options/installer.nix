# The cluster's installer, built with the cluster's settings, so it carries the cluster's OS CA.
{
  config,
  lib,
  nixpkgs,
  ...
}:
let
  inherit
    (import ../settings.nix {
      inherit lib;
      inherit (config) chalkos;
    })
    settings
    module
    ;
  cfg = config.chalkos.installer;
in
{
  options.chalkos.installer = {
    nixosModules = lib.mkOption {
      type = lib.types.listOf lib.types.deferredModule;
      default = [ ];
      description = ''
        NixOS modules added to the installer: systemd-networkd with static addresses, VLANs and
        bonds for the machines it boots on, further kernel module groups and firmware, consoles.
        Secure Boot fixes the kernel command line, so what differs between machines goes into the
        image, matched by MAC address or interface name.
      '';
    };
    nixos = lib.mkOption {
      type = lib.types.raw;
      readOnly = true;
      internal = true;
      description = "The evaluated NixOS system of the installer.";
    };
    image = lib.mkOption {
      type = lib.types.package;
      readOnly = true;
      description = ''
        Installer of the cluster: a raw image and a hybrid ISO that boot chalkd in maintenance
        mode, from which chalkctl install writes a node's role image of any platform to its
        system disk.
      '';
    };
  };

  config.chalkos.installer = {
    nixos = nixpkgs.lib.nixosSystem {
      modules = [
        ../../node
        ../../installer
        module
        { nixpkgs.hostPlatform = settings.cluster.system; }
      ]
      ++ cfg.nixosModules;
    };
    image = cfg.nixos.config.system.build.chalkosInstaller;
  };
}
