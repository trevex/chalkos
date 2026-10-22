# mkImages builds a cluster's images: each variant on each platform, an image directory each, and
# the installer, which installs images of every platform. Its arguments are the options of
# images-arguments.nix. The images' modules get lib.chalkos as part of lib.
{ defaultNixpkgs }:
arguments:
let
  inherit (defaultNixpkgs) lib;
  cfg =
    (lib.evalModules {
      modules = [
        ./images-arguments.nix
        {
          _file = "chalkos.lib.mkImages";
          config = arguments;
        }
      ];
      specialArgs = { inherit defaultNixpkgs; };
    }).config;

  # The lib of every image's modules: nixpkgs's, with lib.chalkos.
  imageLib = cfg.nixpkgs.lib.extend (final: _: { chalkos = import ./chalkos.nix { lib = final; }; });

  # A variant's image on a platform: chalkos's node modules, the platform's modules, those of every
  # variant, then the variant's own. A NixOS value merges by priority, not by module order: a
  # variant overrides a value its platform defines plainly with lib.mkForce, and one the platform
  # forces, such as kvm's ExecStart of the guest agent, with lib.mkOverride below 50; lib.mkBefore
  # and lib.mkAfter order a list's entries.
  variantSystem =
    variant: v: platform: platformModules:
    cfg.nixpkgs.lib.nixosSystem {
      inherit (cfg) specialArgs;
      lib = imageLib;
      modules = [
        ../modules/node
        {
          nixpkgs.hostPlatform = cfg.system;
          chalkos.variant.name = variant;
          chalkos.platform.name = platform;
          chalkos.kubernetes.enable = v.kubernetes;
          chalkos.maintenance.osCA = cfg.osCA;
          chalkos.provenance = { inherit (cfg) revision tag; };
        }
      ]
      ++ platformModules
      ++ cfg.modules
      ++ v.modules;
    };
  systems = lib.concatLists (
    lib.mapAttrsToList (
      variant: v:
      lib.mapAttrsToList (platform: modules: {
        name = "${variant}-${platform}";
        value = variantSystem variant v platform modules;
      }) cfg.platforms
    ) cfg.variants
  );
  names = map (s: s.name) systems;
  # A variant's name may hold a dash, so a-b on metal and a on b-metal would both be a-b-metal.
  collision = lib.findFirst (n: lib.count (m: m == n) names > 1) null names;

  installer = cfg.nixpkgs.lib.nixosSystem {
    inherit (cfg) specialArgs;
    lib = imageLib;
    modules = [
      ../modules/node
      ../modules/installer
      {
        nixpkgs.hostPlatform = cfg.system;
        chalkos.maintenance.osCA = cfg.osCA;
        chalkos.provenance = { inherit (cfg) revision tag; };
      }
    ]
    ++ cfg.installer.modules;
  };

  nixos =
    if collision != null then
      throw "mkImages: two variants on their platforms are both named ${collision}; rename a variant or a platform"
    else
      lib.listToAttrs systems // { inherit installer; };
in
{
  # The evaluated NixOS systems: <variant>-<platform> and installer.
  inherit nixos;
  # The image directories, <variant>-<platform>, each built only when asked for.
  images = lib.mapAttrs (_: n: n.config.system.build.chalkosImage) (
    removeAttrs nixos [ "installer" ]
  );
  # The installer: a raw image and a hybrid ISO that boot chalkd in maintenance mode.
  installer = installer.config.system.build.chalkosInstaller;
}
