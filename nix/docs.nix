# The documentation site, built with Zensical from zensical.toml and docs/, and the reference
# pages generated from the code. The flake's source holds tracked files alone, so
# docs/superpowers, which git ignores, never reaches the site.
{ pkgs, self }:
let
  inherit (pkgs) lib;
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../zensical.toml
      ../docs
    ];
  };

  docgen = pkgs.callPackage ./go-module.nix { } {
    pname = "docgen";
    paths = [
      ../cmd
      ../pkg
    ];
    subPackages = [ "cmd/docgen" ];
    # The go-unit check runs its tests.
    doCheck = false;
  };

  # Declarations link to the repository rather than to the store path of the flake's source.
  root = toString ../.;
  repository = "https://github.com/trevex/chalkos/blob/main";
  linkDeclarations =
    opt:
    opt
    // {
      declarations = map (
        d:
        let
          relative = lib.removePrefix "${root}/" (toString d);
          # A module imported as a directory is its default.nix.
          path = if lib.pathIsDirectory d then "${relative}/default.nix" else relative;
        in
        {
          name = path;
          url = "${repository}/${path}";
        }
      ) opt.declarations;
    };
  declaredIn = dir: opt: lib.all (d: lib.hasPrefix "${root}/${dir}/" (toString d)) opt.declarations;

  # The arguments of chalkos.lib.mkImages, from the module that checks them.
  arguments = lib.evalModules {
    modules = [ ../lib/images-arguments.nix ];
    specialArgs.defaultNixpkgs = self.inputs.nixpkgs;
  };
  argumentOptions =
    (pkgs.nixosOptionsDoc {
      options = removeAttrs arguments.options [ "_module" ];
      transformOptions = linkDeclarations;
    }).optionsCommonMark;

  # An image's own options, those the node modules declare.
  imageSystem = (self.lib.mkImages { variants.docs = { }; }).nixos.docs-metal;
  imageOptions =
    (pkgs.nixosOptionsDoc {
      options = { inherit (imageSystem.options) chalkos; };
      transformOptions =
        opt:
        linkDeclarations opt
        // {
          visible = opt.visible && opt.name != "chalkos" && declaredIn "modules/node" opt;
        };
    }).optionsCommonMark;

  optionsHead = pkgs.writeText "options-head.md" ''
    ---
    title: "mkImages and image options"
    description: "The arguments of chalkos.lib.mkImages and the options of the images it builds"
    ---

    # mkImages and image options

    `chalkos.lib.mkImages` builds a cluster's images from its arguments: each variant on each
    platform, and the installer. The NixOS modules of a variant set the options of its images,
    NixOS systems with chalkos's node modules. This page is generated from those modules with
    nixosOptionsDoc; each option links to its declaration.

    A flake calls mkImages and exposes the images to build:

    ```nix title="flake.nix"
    {
      inputs.chalkos.url = "github:trevex/chalkos";

      outputs =
        { chalkos, ... }:
        let
          images = chalkos.lib.mkImages {
            osCA = ./secrets.pub.json;
            platforms = [ "metal" "kvm" ];
            # Every variant's images carry these.
            modules = [ ./site.nix ];
            variants = {
              node = { };
              storage = {
                kubernetes = false;
                modules = [ ./storage.nix ];
              };
            };
            installer.modules = [ ./installer-network.nix ];
          };
        in
        {
          packages.x86_64-linux = images.images // { inherit (images) installer; };
        };
    }
    ```

    It returns:

    - `images.<variant>-<platform>`: the image directory of a variant on a platform, such as
      `images.node-metal`, each built only when asked for.
    - `installer`: the installer, a raw image and a hybrid ISO in one directory, which installs
      images of every platform.
    - `nixos.<variant>-<platform>` and `nixos.installer`: the evaluated NixOS systems, to read an
      image's options, such as `nixos.node-metal.config.chalkos.kubernetes.package.version`.

    ## mkImages arguments

  '';
  imageHead = pkgs.writeText "options-image-head.md" ''

    ## Image options

    The options the node modules declare in every image and in the installer.

  '';
in
rec {
  inherit docgen;

  # The site, built strictly, so a broken link fails the build. Zensical skips a page the
  # navigation names but that is missing, so the navigation is checked first.
  site =
    pkgs.runCommand "chalkos-docs"
      {
        nativeBuildInputs = [
          pkgs.zensical
          pkgs.python3
        ];
      }
      ''
        export HOME=$TMPDIR
        cp -r ${src} work
        chmod -R u+w work
        cd work
        python3 ${./docs-nav.py} zensical.toml
        zensical build --strict --clean
        cp -r site $out
      '';

  # The generated pages of docs/reference and zensical.toml with their navigation, laid out as
  # in the repository: a page per command of chalkctl and chalklab, the arguments of mkImages and
  # the image options, and the node API.
  reference =
    pkgs.runCommand "chalkos-docs-reference"
      {
        nativeBuildInputs = [
          docgen
          pkgs.buf
          pkgs.protoc-gen-go
          pkgs.protoc-gen-connect-go
          pkgs.protoc-gen-doc
        ];
        api = lib.fileset.toSource {
          root = ../.;
          fileset = lib.fileset.unions [
            ../buf.yaml
            ../buf.gen.yaml
            ../api
          ];
        };
      }
      ''
        export HOME=$TMPDIR
        mkdir -p $out/docs/reference
        install -m 644 ${../zensical.toml} $out/zensical.toml
        docgen cli $out/docs/reference/cli $out/zensical.toml
        {
          cat ${optionsHead}
          sed 's/^## /### /' ${argumentOptions}
          cat ${imageHead}
          sed 's/^## /### /' ${imageOptions}
        } > $out/docs/reference/options.md
        cp -r $api proto
        chmod -R u+w proto
        (cd proto && buf generate)
        cp proto/docs/reference/api.md $out/docs/reference/api.md
      '';

  # The site builds strictly, and the chalkctl and chalklab commands its pages show exist.
  check = pkgs.runCommand "chalkos-docs-check" { nativeBuildInputs = [ docgen ]; } ''
    cd ${src}
    docgen check docs
    test -d ${site}
    touch $out
  '';

  # Fails when the generated pages in the repository differ from what the code generates.
  generated = pkgs.runCommand "chalkos-docs-generated" { } ''
    status=0
    for path in docs/reference/cli docs/reference/options.md docs/reference/api.md zensical.toml; do
      diff -ru ${src}/$path ${reference}/$path || status=1
    done
    if [ $status != 0 ]; then
      echo "error: the generated documentation is out of date; run nix run .#docgen and commit what it changes" >&2
      exit 1
    fi
    touch $out
  '';

  # Writes the generated pages into the working tree.
  write = pkgs.writeShellApplication {
    name = "chalkos-docgen";
    text = ''
      if [ ! -f zensical.toml ] || [ ! -d docs ]; then
        echo "chalkos-docgen: run it in the repository's root, where zensical.toml is" >&2
        exit 1
      fi
      rm -rf docs/reference/cli
      mkdir -p docs/reference
      cp -r --no-preserve=mode ${reference}/docs/reference/cli docs/reference/cli
      cp --no-preserve=mode ${reference}/docs/reference/options.md ${reference}/docs/reference/api.md docs/reference/
      cp --no-preserve=mode ${reference}/zensical.toml zensical.toml
    '';
  };

  # Serves the working tree's docs while they are edited. Zensical has no way to exclude a
  # directory, so it serves a copy without docs/superpowers, which is kept in sync every second.
  serve = pkgs.writeShellApplication {
    name = "chalkos-docs-serve";
    runtimeInputs = [
      pkgs.zensical
      pkgs.rsync
    ];
    text = ''
      if [ ! -f zensical.toml ] || [ ! -d docs ]; then
        echo "chalkos-docs-serve: run it in the repository's root, where zensical.toml is" >&2
        exit 1
      fi
      work=$(mktemp -d)
      syncer=
      # The copying loop ends before its copy is removed.
      trap 'if [ -n "$syncer" ]; then kill "$syncer" 2>/dev/null || true; wait "$syncer" 2>/dev/null || true; fi; rm -rf "$work"' EXIT
      copy() {
        rsync -a --delete --exclude /superpowers/ docs/ "$work/docs/"
        rsync -a zensical.toml "$work/zensical.toml"
      }
      copy
      while sleep 1; do copy; done &
      syncer=$!
      zensical serve -f "$work/zensical.toml" "$@"
    '';
  };
}
