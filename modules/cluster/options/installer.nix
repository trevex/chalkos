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
in
{
  options.chalkos.installer = lib.mkOption {
    type = lib.types.package;
    readOnly = true;
    description = ''
      Installer of the cluster: a raw image and an ISO that boot chalkd in maintenance mode, from
      which chalkctl install writes a node's role image to its system disk.
    '';
  };

  config.chalkos.installer =
    (nixpkgs.lib.nixosSystem {
      modules = [
        ../../node
        ../../installer
        module
        { nixpkgs.hostPlatform = settings.cluster.system; }
      ];
    }).config.system.build.chalkosInstaller;
}
