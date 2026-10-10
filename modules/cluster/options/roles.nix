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
  inherit (config.chalkos) platforms;
  roleDefaults = name: ../../roles + "/${name}.nix";

  roleModule =
    { name, config, ... }:
    {
      options = {
        nixosModules = lib.mkOption {
          type = lib.types.listOf lib.types.deferredModule;
          default = [ ];
          description = "NixOS modules added to this role's images.";
        };
        nixos = lib.mkOption {
          type = lib.types.lazyAttrsOf lib.types.raw;
          readOnly = true;
          internal = true;
          description = "The evaluated NixOS system of this role, by platform.";
        };
        images = lib.mkOption {
          type = lib.types.lazyAttrsOf lib.types.package;
          readOnly = true;
          description = ''
            Unsigned disk image of this role by platform, such as `images.kvm`: the raw image with
            `repart-output.json` and `repart.d`, each built only when asked for. Cluster settings
            and the role's own flow into every platform's image, so they must not be derived from
            `chalkos.roles`.
          '';
        };
      };
      config = {
        # The platform's modules come before the role's, but a NixOS value merges by priority, not
        # by module order: a role overrides a value its platform defines plainly with lib.mkForce,
        # and one the platform forces, such as kvm's ExecStart of the guest agent, with
        # lib.mkOverride below 50; lib.mkBefore and lib.mkAfter order a list's entries.
        nixos = lib.mapAttrs (
          platform: p:
          nixpkgs.lib.nixosSystem {
            modules = [
              ../../node
              module
              {
                nixpkgs.hostPlatform = settings.cluster.system;
                chalkos.role.name = name;
                chalkos.role.kubernetes.kind = config.kubernetes.kind;
                chalkos.platform.name = platform;
              }
            ]
            ++ p.nixosModules
            ++ lib.optional (builtins.pathExists (roleDefaults name)) (roleDefaults name)
            ++ config.nixosModules;
          }
        ) platforms;
        images = lib.mapAttrs (_: nixos: nixos.config.system.build.chalkosImage) config.nixos;
      };
    };
in
{
  options.chalkos.roles = lib.mkOption {
    type = lib.types.attrsOf (lib.types.submodule roleModule);
    default = { };
    description = "Node roles. Each role is built into one image per platform, which all its nodes on that platform share.";
  };
}
