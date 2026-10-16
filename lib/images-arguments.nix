# The arguments of chalkos.lib.mkImages, checked as module options, so a misspelled or mistyped
# argument fails with the option's name and the options reference documents each one.
{ lib, defaultNixpkgs, ... }:
let
  inherit (lib) mkOption types;
  # The version of secrets.pub.json chalkos reads; chalkctl gen secrets writes no other.
  publicVersion = 3;
  # A file that exists at evaluation, such as a path in the flake, is read and checked then. One a
  # derivation builds would need import from derivation; the image build checks it instead.
  readable =
    p:
    builtins.isPath p
    || !lib.any (c: c ? outputs || c.allOutputs or false) (
      lib.attrValues (builtins.getContext (toString p))
    );
  checkPublic =
    p:
    let
      version = (builtins.fromJSON (builtins.readFile p)).version or null;
    in
    if p == null || !readable p || version == publicVersion then
      p
    else
      throw "mkImages: osCA ${toString p} is version ${toString version}; chalkos reads version ${toString publicVersion} only, so generate new secrets with chalkctl gen secrets";
  # A variant's and a platform's name become CHALKOS_VARIANT and CHALKOS_PLATFORM in the images'
  # os-release, and together the image's name, <variant>-<platform>.
  namePattern = "[a-z0-9][a-z0-9-]*";
  checkNames =
    argument: attrs:
    let
      bad = lib.filter (n: builtins.match namePattern n == null) (lib.attrNames attrs);
    in
    if bad == [ ] then
      attrs
    else
      throw "mkImages: ${argument}.${lib.head bad}: a name must match ${namePattern}, lower-case letters, digits and dashes not at the start, as it names the images and their os-release";
  # chalkos's own platforms, whose modules every image of the platform carries first.
  builtinPlatforms = {
    metal = [ ../modules/platforms/metal.nix ];
    kvm = [ ../modules/platforms/kvm.nix ];
  };
  modules = types.listOf types.deferredModule;
in
{
  options = {
    nixpkgs = mkOption {
      type = types.raw;
      default = defaultNixpkgs;
      defaultText = lib.literalMD "the nixpkgs chalkos is tested against";
      description = "nixpkgs whose `lib.nixosSystem` evaluates the images and whose packages they are built from.";
    };
    system = mkOption {
      type = types.str;
      default = "x86_64-linux";
      description = "Platform the images are built for, as Nix names it.";
    };
    osCA = mkOption {
      type = types.nullOr types.path;
      default = null;
      example = lib.literalExpression "./secrets.pub.json";
      apply = checkPublic;
      description = ''
        Public part of the cluster's secrets (`secrets.pub.json` from `chalkctl gen secrets`,
        version ${toString publicVersion}), whose OS CA every image and the installer carry, so
        chalkd in maintenance mode accepts only clients with a certificate from it. null builds
        images that accept any client until they are installed.
      '';
    };
    modules = mkOption {
      type = modules;
      default = [ ];
      description = ''
        NixOS modules every variant carries, after its platform's modules and before its own. The
        installer does not carry them: it holds no config, so the extensions that read config
        have nothing to do there; `installer.modules` are its own.
      '';
    };
    specialArgs = mkOption {
      type = types.attrsOf types.raw;
      default = { };
      example = lib.literalExpression "{ inherit inputs; }";
      description = "Arguments every image's NixOS modules receive besides NixOS's own, such as a flake's inputs.";
    };
    platforms = mkOption {
      type = types.either (types.listOf (types.strMatching namePattern)) (types.attrsOf modules);
      default = [
        "metal"
        "kvm"
      ];
      example = lib.literalExpression ''
        {
          metal = [ ./firmware.nix ];
          dell-r650 = [ ./r650.nix ];
        }
      '';
      apply =
        platforms:
        lib.mapAttrs (name: modules: builtinPlatforms.${name} or [ ] ++ modules) (
          checkNames "platforms" (
            if builtins.isList platforms then lib.genAttrs platforms (_: [ ]) else platforms
          )
        );
      description = ''
        Platforms every variant is built for: a list of names, or by name the NixOS modules a
        platform's images carry besides chalkos's own. chalkos defines `metal` and `kvm`; any
        other name is a platform of the modules given. An image carries one platform's drivers,
        agents and console; a node refuses an image of another platform than its own. A
        platform's name becomes `CHALKOS_PLATFORM` in its images' os-release.
      '';
    };
    variants = mkOption {
      type = types.attrsOf (
        types.submodule {
          options = {
            modules = mkOption {
              type = modules;
              default = [ ];
              description = "NixOS modules of the variant's images, after those of its platform and of every variant.";
            };
            kubernetes = mkOption {
              type = types.bool;
              default = true;
              description = ''
                Whether the variant's images run Kubernetes. One image serves control planes and
                workers: each node's Node document makes it one or the other.
              '';
            };
          };
        }
      );
      default.node = { };
      apply = checkNames "variants";
      description = ''
        Variants of the cluster's images, by name: each is built for every platform as
        `images.<variant>-<platform>`. A variant's name becomes `CHALKOS_VARIANT` in its images'
        os-release and `variant` in their `metadata.json`.
      '';
    };
    installer.modules = mkOption {
      type = modules;
      default = [ ];
      description = ''
        NixOS modules of the installer: systemd-networkd with static addresses, VLANs and bonds
        for the machines it boots on, further kernel module groups and firmware, consoles. Secure
        Boot fixes the kernel command line, so what differs between machines goes into the image,
        matched by MAC address or interface name.
      '';
    };
  };
}
