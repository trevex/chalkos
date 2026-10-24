# chalkos's own programs, built with the image's nixpkgs, which the node modules take as the
# module argument chalkosPackages.
{ pkgs, ... }:
{
  _module.args.chalkosPackages = {
    chalkcfg = pkgs.callPackage ../../nix/chalkcfg.nix { };
    chalkd = pkgs.callPackage ../../nix/chalkd.nix { };
    chalkos-networkd-schema = pkgs.callPackage ../../nix/chalkos-networkd-schema.nix { };
    chalkos-render = pkgs.callPackage ../../nix/chalkos-render.nix { };
    chalkos-rules = pkgs.callPackage ../../nix/chalkos-rules.nix { };
    chalkos-storage = pkgs.callPackage ../../nix/chalkos-storage.nix { };
    image-size = pkgs.callPackage ../../nix/image-size.nix { };
    kernel-modules = pkgs.callPackage ../../nix/kernel-modules.nix { };
    relabel-partitions = pkgs.callPackage ../../nix/relabel-partitions.nix { };
  };
}
