# mkImages builds a cluster's images: each variant on each platform, an image directory each, and
# the installer, which installs images of every platform. Its arguments are the options of
# images-arguments.nix. The images' modules get chalkosLib as a module argument.
{ defaultNixpkgs, overlay }:
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

  # chalkosLib, which every image's modules get as a module argument, built from the images'
  # nixpkgs's lib, so its option types are those of the module system that evaluates them. It is
  # an argument of its own, as nixpkgs reserves lib for its library.
  chalkosLib = import ./chalkos.nix { inherit (cfg.nixpkgs) lib; };
  specialArgs =
    if cfg.specialArgs ? chalkosLib then
      throw "mkImages: specialArgs.chalkosLib is chalkos's own module argument; give yours another name"
    else
      cfg.specialArgs // { inherit chalkosLib; };

  # chalkos's package scope in the images' nixpkgs, which builds chalkos's programs with
  # chalkos's Go toolchain whatever nixpkgs the images come from. It comes before the images' own
  # overlays, so they can override chalkos's packages with pkgs.chalkos.overrideScope.
  chalkosScope = {
    _file = "chalkos.lib.mkImages";
    nixpkgs.overlays = lib.mkBefore [ overlay ];
  };

  # A variant's image on a platform: chalkos's node modules, the platform's modules, those of every
  # variant, then the variant's own. A NixOS value merges by priority, not by module order: a
  # variant overrides a value its platform defines plainly with lib.mkForce, and one the platform
  # forces, such as kvm's ExecStart of the guest agent, with lib.mkOverride below 50; lib.mkBefore
  # and lib.mkAfter order a list's entries.
  variantSystem =
    variant: v: platform: platformModules:
    cfg.nixpkgs.lib.nixosSystem {
      inherit specialArgs;
      modules = [
        ../modules/node
        chalkosScope
        {
          nixpkgs.hostPlatform = cfg.system;
          chalkos.variant.name = variant;
          chalkos.platform.name = platform;
          chalkos.kubernetes.enable = v.kubernetes;
          chalkos.router.enable = v.router;
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
    inherit specialArgs;
    modules = [
      ../modules/node
      chalkosScope
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
  # The image directories, <variant>-<platform>.
  images = lib.mapAttrs (_: n: n.config.system.build.chalkosImage) (
    removeAttrs nixos [ "installer" ]
  );
in
{
  # The evaluated NixOS systems: <variant>-<platform> and installer.
  inherit nixos;
  # The image directories, <variant>-<platform>, each built only when asked for.
  inherit images;
  # The OCI image layouts of the image directories, <variant>-<platform>, each built only when
  # asked for by chalkbuild oci, which the image's own nixpkgs builds as it builds chalkd.
  oci = lib.mapAttrs (
    name: image:
    let
      inherit (nixos.${name}) pkgs;
    in
    pkgs.runCommand "chalkos-${name}-oci" {
      nativeBuildInputs = [ pkgs.chalkos.chalkbuild ];
    } "chalkbuild oci ${image} $out"
  ) images;
  # The installer: a raw image and a hybrid ISO that boot chalkd in maintenance mode.
  installer = installer.config.system.build.chalkosInstaller;
}
