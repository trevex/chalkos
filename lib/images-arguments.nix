# The arguments of chalkos.lib.mkImage, mkInstaller and mkImages, checked as module options, so a
# misspelled or mistyped argument fails with the option's name and the mkImages reference
# documents each one. shared holds the settings all three take; image, installer and images add
# each function's own.
let
  # A name becomes CHALKOS_IMAGE or CHALKOS_PLATFORM in the image's os-release.
  namePattern = "[a-z0-9][a-z0-9-]*";
  # The options of one image, mkImage's and those of each of mkImages's images.
  imageOptions =
    lib:
    let
      inherit (import ./refuse.nix { inherit lib; }) refuse;
      nameOption =
        what: description:
        lib.mkOption {
          type = lib.types.str;
          apply =
            n:
            refuse (lib.optional (builtins.match namePattern n == null)
              "${what}: ${n} is no name: a name must match ${namePattern}, lower-case letters, digits and dashes not at the start, as it names the image and its os-release"
            ) n;
          inherit description;
        };
    in
    {
      name = nameOption "name" ''
        The image's name, matching `${namePattern}`, which is its attribute in mkImages's result,
        `CHALKOS_IMAGE` in its os-release and `name` in its `metadata.json`.
        `chalkctl upgrade --image` without node names upgrades the nodes running an image of this
        name.
      '';
      platform = nameOption "platform" ''
        The platform the image is built for, matching `${namePattern}`, which becomes
        `CHALKOS_PLATFORM` in its os-release. chalkos has modules of its own for `metal` and
        `kvm`, which the image carries before its `modules`; any other name is a platform of
        the image's modules alone. An image carries one platform's drivers, agents and console;
        a node refuses an image of another platform than its own.
      '';
      modules = lib.mkOption {
        type = lib.types.listOf lib.types.deferredModule;
        default = [ ];
        example = lib.literalExpression "[ chalkos.nixosModules.kubernetes ./site.nix ]";
        description = ''
          NixOS modules of the image, after chalkos's modules of its platform. A layer, such as
          `chalkos.nixosModules.kubernetes` or `chalkos.nixosModules.router`, is one of them; an
          image lists one layer at most.
        '';
      };
    };
in
{
  shared =
    { lib, defaultNixpkgs, ... }:
    let
      inherit (lib) mkOption types;
      publicVersion = import ./secrets-version.nix;
      # A file that exists at evaluation, such as a path in the flake, is read and checked then.
      # One a derivation builds would need import from derivation; the image build checks it
      # instead.
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
          throw "osCA ${toString p} is version ${toString version}; chalkos reads version ${toString publicVersion} only, so generate new secrets with chalkctl gen secrets";
    in
    {
      options = {
        nixpkgs = mkOption {
          type = types.raw;
          default = defaultNixpkgs;
          defaultText = lib.literalMD "the nixpkgs chalkos is tested against";
          description = "nixpkgs whose `lib.nixosSystem` evaluates the images and whose packages they are built from. chalkos's own programs are built with chalkos's Go toolchain, whichever nixpkgs this is.";
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
        revision = mkOption {
          type = types.nullOr types.str;
          default = null;
          example = lib.literalExpression "self.rev or self.dirtyRev or null";
          description = ''
            The git revision the images are built from, which every image directory's and the
            installer's `metadata.json` record and chalkctl shows beside an image's root hash. It
            never reaches an image's store, so it changes no root hash, and nothing orders or
            refuses images by it: a node identifies an image by its root hash alone.
          '';
        };
        tag = mkOption {
          type = types.nullOr types.str;
          default = null;
          example = "v1.5.0";
          description = ''
            The git tag the images are built from, such as a release's, recorded and shown like
            `revision`. Nix cannot read git tags, so whatever builds a release passes it.
          '';
        };
        specialArgs = mkOption {
          type = types.attrsOf types.raw;
          default = { };
          example = lib.literalExpression "{ inherit inputs; }";
          description = "Arguments every image's NixOS modules receive besides NixOS's own, such as a flake's inputs. `chalkosLib` is chalkos's own and refused.";
        };
      };
    };

  # mkImage's own arguments.
  image =
    { lib, ... }:
    {
      options = imageOptions lib;
    };

  # mkInstaller's own argument.
  installer =
    { lib, ... }:
    {
      options.modules = lib.mkOption {
        type = lib.types.listOf lib.types.deferredModule;
        default = [ ];
        description = "NixOS modules of the installer, as mkImages's `installer.modules`.";
      };
    };

  # mkImages's own arguments.
  images =
    { lib, ... }:
    let
      inherit (lib) mkOption types;
      modules = types.listOf types.deferredModule;
    in
    {
      options = {
        modules = mkOption {
          type = modules;
          default = [ ];
          description = ''
            NixOS modules every image carries, after its platform's modules and before its own.
            The installer does not carry them: it holds no config, so the extensions that read
            config have nothing to do there; `installer.modules` are its own.
          '';
        };
        images = mkOption {
          type = types.listOf (
            types.submodule {
              options = imageOptions lib;
            }
          );
          default = [ ];
          example = lib.literalExpression ''
            [
              {
                name = "node-metal";
                platform = "metal";
                modules = [ chalkos.nixosModules.kubernetes ];
              }
              {
                name = "router-metal";
                platform = "metal";
                modules = [ chalkos.nixosModules.router ];
              }
            ]
          '';
          description = ''
            The images, each built with mkImage from the arguments shared by every image and its
            own `name`, `platform` and `modules`. Two images of one name are refused.
          '';
        };
        installer.modules = mkOption {
          type = modules;
          default = [ ];
          description = ''
            NixOS modules of the installer: systemd-networkd with static addresses, VLANs and
            bonds for the machines it boots on, further kernel module groups and firmware,
            consoles. Secure Boot fixes the kernel command line, so what differs between machines
            goes into the image, matched by MAC address or interface name.
          '';
        };
      };
    };
}
