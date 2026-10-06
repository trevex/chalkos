{
  config,
  lib,
  nixpkgs,
  ...
}:
let
  # Everything a role image may read: the cluster definition minus per-node data and outputs.
  settings = removeAttrs config.chalkos [
    "nodes"
    "roles"
    "manifest"
  ];
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
          description = "The evaluated NixOS system of this role.";
        };
        image = lib.mkOption {
          type = lib.types.package;
          readOnly = true;
          description = "Unsigned disk image of this role.";
        };
      };
      config = {
        nixos = nixpkgs.lib.nixosSystem {
          modules = [
            ../../node
            {
              nixpkgs.hostPlatform = settings.cluster.system;
              chalkos = settings;
            }
          ]
          ++ lib.optional (builtins.pathExists (roleDefaults name)) (roleDefaults name)
          ++ config.nixosModules;
        };
        image = config.nixos.config.system.build.image;
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
