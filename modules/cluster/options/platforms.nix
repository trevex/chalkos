# Platforms: what a role image needs on the machines it runs on. Each role is built once per
# platform, so an image carries one platform's drivers and agents, not every platform's.
{ lib, ... }:
let
  platformModule = {
    options.nixosModules = lib.mkOption {
      type = lib.types.listOf lib.types.deferredModule;
      default = [ ];
      description = ''
        NixOS modules every role image of this platform carries, before the role's own: kernel
        module groups, firmware, agents, the kernel's console and command line.
      '';
    };
  };
in
{
  options.chalkos.platforms = lib.mkOption {
    type = lib.types.attrsOf (lib.types.submodule platformModule);
    description = ''
      Platforms the cluster's role images are built for, by name: `chalkos.roles.<role>.images.<platform>`
      is a role's image for one. chalkos defines `metal` and `kvm`; a definition adds modules to one
      of them or a platform of its own. A node runs the image of its role and its
      `chalkos.nodes.<node>.platform`.
    '';
  };

  config.chalkos.platforms = {
    metal.nixosModules = [ ../../platforms/metal.nix ];
    kvm.nixosModules = [ ../../platforms/kvm.nix ];
  };
}
