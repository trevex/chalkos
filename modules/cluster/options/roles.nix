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
    "warnings"
  ];
  roleDefaults = name: ../../roles + "/${name}.nix";

  # Declaring each setting as a read-only option keeps role modules from overriding cluster
  # settings and lets the module system reject unknown names under `chalkos`.
  settingsModule = {
    options.chalkos = lib.mkOption {
      type = lib.types.submodule {
        options = lib.mapAttrs (
          _: _:
          lib.mkOption {
            type = lib.types.anything;
            readOnly = true;
          }
        ) settings;
      };
    };
    config.chalkos = settings;
  };

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
            settingsModule
            { nixpkgs.hostPlatform = settings.cluster.system; }
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
