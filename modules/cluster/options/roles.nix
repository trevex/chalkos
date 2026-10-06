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
  roleDefaults = name: ../../roles + "/${name}.nix";

  roleModule =
    { name, config, ... }:
    {
      options = {
        nixosModules = lib.mkOption {
          type = lib.types.listOf lib.types.deferredModule;
          default = [ ];
          description = "NixOS modules added to this role's image.";
        };
        nixos = lib.mkOption {
          type = lib.types.raw;
          readOnly = true;
          internal = true;
          description = "The evaluated NixOS system of this role.";
        };
        image = lib.mkOption {
          type = lib.types.package;
          readOnly = true;
          description = ''
            Unsigned disk image of this role. Cluster settings flow into every role image, so they
            must not be derived from `chalkos.roles`.
          '';
        };
      };
      config = {
        nixos = nixpkgs.lib.nixosSystem {
          modules = [
            ../../node
            module
            { nixpkgs.hostPlatform = settings.cluster.system; }
          ]
          ++ lib.optional (builtins.pathExists (roleDefaults name)) (roleDefaults name)
          ++ config.nixosModules;
        };
        image = config.nixos.config.system.build.chalkosImage;
      };
    };
in
{
  options.chalkos.roles = lib.mkOption {
    type = lib.types.attrsOf (lib.types.submodule roleModule);
    default = { };
    description = "Node roles. Each role is built into one image shared by all its nodes.";
  };
}
