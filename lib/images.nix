# The functions of the flake's lib that build images: mkImage builds one image, mkInstaller the
# installer, which installs images of every platform, and mkImages both from a list of images.
# Their arguments are the options of images-arguments.nix. The images' modules get chalkosLib as a
# module argument. The helpers select and map over mkImages's result, skipping the installer.
{
  defaultNixpkgs,
  overlay,
  bombon,
}:
let
  inherit (defaultNixpkgs) lib;
  inherit (import ./refuse.nix { inherit lib; }) refuse;
  arguments = import ./images-arguments.nix;

  # The arguments given to the function named, checked against the options of the modules.
  check =
    function: modules: given:
    (lib.evalModules {
      modules = modules ++ [
        {
          _file = "chalkos.lib.${function}";
          config = given;
        }
      ];
      specialArgs = { inherit defaultNixpkgs; };
    }).config;

  # A NixOS system of chalkos's node modules and the modules given, from the shared arguments.
  # chalkosLib, which every module gets as a module argument, is built from the images' nixpkgs's
  # lib, so its option types are those of the module system that evaluates them. It is an argument
  # of its own, as nixpkgs reserves lib for its library.
  nixosSystem =
    function: cfg: modules:
    cfg.nixpkgs.lib.nixosSystem {
      specialArgs =
        if cfg.specialArgs ? chalkosLib then
          throw "${function}: specialArgs.chalkosLib is chalkos's own module argument; give yours another name"
        else
          cfg.specialArgs // { chalkosLib = import ./chalkos.nix { inherit (cfg.nixpkgs) lib; }; };
      modules = [
        ../modules/node
        # chalkos's package scope in the images' nixpkgs, which builds chalkos's programs with
        # chalkos's Go toolchain whatever nixpkgs the images come from. It comes before the
        # images' own overlays, so they can override chalkos's packages with
        # pkgs.chalkos.overrideScope.
        {
          _file = "chalkos.lib.${function}";
          nixpkgs.overlays = lib.mkBefore [ overlay ];
        }
        {
          nixpkgs.hostPlatform = cfg.system;
          chalkos.maintenance.osCA = cfg.osCA;
          chalkos.provenance = { inherit (cfg) revision tag; };
        }
      ]
      ++ modules;
    };

  # chalkos's own platforms, whose modules every image of the platform carries first.
  builtinPlatforms = {
    metal = [ ../modules/platforms/metal.nix ];
    kvm = [ ../modules/platforms/kvm.nix ];
  };

  # An image: chalkos's node modules, its platform's modules, then its own. A NixOS value merges by
  # priority, not by module order: an image overrides a value its platform defines plainly with
  # lib.mkForce, and one the platform forces, such as kvm's ExecStart of the guest agent, with
  # lib.mkOverride below 50; lib.mkBefore and lib.mkAfter order a list's entries.
  mkImage =
    given:
    let
      cfg = check "mkImage" [
        arguments.shared
        arguments.image
      ] given;
      system = nixosSystem "mkImage" cfg (
        [
          {
            chalkos.image.name = cfg.name;
            chalkos.platform.name = cfg.platform;
          }
        ]
        ++ builtinPlatforms.${cfg.platform} or [ ]
        ++ cfg.modules
      );
      inherit (system) pkgs;
      image = system.config.system.build.chalkosImage;
    in
    image
    // {
      inherit (cfg) name platform;
      # The layer the image carries; modules/node/layers.nix refuses more than one.
      layer = lib.findFirst (_: true) null (lib.attrNames system.config.chalkos.layers);
      nixos = system;
      # The image directory as an OCI image layout, which the image's own nixpkgs builds as it
      # builds chalkd.
      oci = pkgs.runCommand "chalkos-${cfg.name}-oci" {
        nativeBuildInputs = [ pkgs.chalkos.chalkbuild ];
      } "chalkbuild oci ${image} $out";
      # The image's SPDX 2.3 SBOM, which the image's own nixpkgs builds: the packages of its system
      # and its kernel. Its version is the image's tag, or untagged, and its time the Unix epoch,
      # so a rebuild writes the same file; sbom.override { name; tag; created; } sets them, as a
      # release does.
      sbom =
        pkgs.lib.makeOverridable
          (import ../nix/sbom.nix {
            inherit pkgs;
            bombon = import bombon { inherit pkgs; };
          }).image
          {
            inherit (cfg) name;
            tag = if cfg.tag != null then cfg.tag else "untagged";
            nixos = system;
          };
      # The image's chalkos.tests, which a flake exports as its checks so nix flake check runs them.
      tests = system.config.system.build.chalkosTests;
    }
    # A KVM image's raw disk as a KubeVirt containerDisk, an OCI image layout, which the image's
    # own nixpkgs builds.
    // lib.optionalAttrs (cfg.platform == "kvm") {
      containerDisk = pkgs.chalkos.containerDisk {
        inherit image;
        inherit (cfg) name;
        raw = system.config.image.fileName;
        size = system.config.chalkos.disk.systemRegionSize;
      };
    };

  # The installer: a raw image and a hybrid ISO that boot chalkd in maintenance mode.
  mkInstaller =
    given:
    let
      cfg = check "mkInstaller" [
        arguments.shared
        arguments.installer
      ] given;
      system = nixosSystem "mkInstaller" cfg ([ ../modules/installer ] ++ cfg.modules);
    in
    system.config.system.build.chalkosInstaller // { nixos = system; };

  # Each image of the list by its name, and the installer.
  mkImages =
    given:
    let
      cfg = check "mkImages" [
        arguments.shared
        arguments.images
      ] given;
      shared = {
        inherit (cfg)
          nixpkgs
          osCA
          revision
          specialArgs
          system
          tag
          ;
      };
      names = map (i: i.name) cfg.images;
      twice = lib.unique (lib.filter (n: lib.count (m: m == n) names > 1) names);
    in
    refuse
      (
        map (n: "mkImages: two images are named ${n}; give each its own name") twice
        ++ lib.optional (lib.elem "installer" names) "mkImages: an image is named installer, which names the installer; give it another name"
      )
      (
        lib.listToAttrs (
          map (
            i:
            lib.nameValuePair i.name (
              mkImage (
                shared
                // {
                  inherit (i) name platform;
                  modules = cfg.modules ++ i.modules;
                }
              )
            )
          ) cfg.images
        )
        // {
          installer = mkInstaller (shared // { inherit (cfg.installer) modules; });
        }
      );

  # Whether x is an image of mkImage, not the installer.
  isImage = x: x ? platform && x ? nixos;
  imagesOf = lib.filterAttrs (_: isImage);
in
{
  inherit mkImage mkImages mkInstaller;

  # The images matching every field given, platform or layer.
  selectImages =
    selection: images:
    refuse
      (map (k: "selectImages: an image has no field ${k} to select by; select by platform or layer") (
        lib.attrNames (
          removeAttrs selection [
            "layer"
            "platform"
          ]
        )
      ))
      (
        lib.filterAttrs (_: i: lib.all (k: i.${k} == selection.${k}) (lib.attrNames selection)) (
          imagesOf images
        )
      );

  # Each image's OCI image layout, evaluated NixOS system and tests.
  ociOf = images: lib.mapAttrs (_: i: i.oci) (imagesOf images);
  nixosOf = images: lib.mapAttrs (_: i: i.nixos) (imagesOf images);
  testsOf = images: lib.mapAttrs (_: i: i.tests) (imagesOf images);
}
