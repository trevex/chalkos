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
      ../includes
      # The homelab's flake, extensions, inventory and config files, which the pages include as
      # snippets.
      ../examples/homelab/flake.nix
      ../examples/homelab/extensions
      ../examples/homelab/chalkos.yaml
      ../examples/homelab/nodes
      ../examples/homelab/cluster
    ];
  };

  # The pages that title the links of command help to the site: every page but the generated
  # command pages, which would otherwise feed their own generation.
  helpLinkPages = lib.fileset.toSource {
    root = ../docs;
    fileset = lib.fileset.difference ../docs ../docs/reference/cli;
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
    }).optionsJSON;

  # The images the documentation is generated and checked against: the default variant, whose
  # options the reference lists, one without Kubernetes, whose built-ins are those of every image,
  # and one with the homelab's extensions, whose documents the pages show as an extension's.
  docsImages = self.lib.mkImages {
    platforms = [ "metal" ];
    variants = {
      docs = { };
      plain.kubernetes = false;
      rack.modules = [
        ../examples/homelab/extensions/rack.nix
        ../examples/homelab/extensions/hardware.nix
        ../examples/homelab/extensions/bgp.nix
      ];
    };
  };
  # An image's own options, those the node modules declare.
  imageSystem = docsImages.nixos.docs-metal;
  # The schemas of chalkctl's inventory and chalklab's lab file, which they are built with.
  inventorySchema = ../pkg/inventory/chalkctl.chalkos.dev_v1alpha1_Inventory.json;
  labSchema = ../pkg/chalklab/chalklab.chalkos.dev_v1alpha1_Lab.json;
  # The schemas the pages' config documents are checked against: the kinds of the image with the
  # Rack, Hardware and BGP extensions, the inventory and the lab file.
  snippetSchemas = pkgs.runCommand "chalkos-docs-schemas" { } ''
    mkdir $out
    ln -s ${docsImages.nixos.rack-metal.config.system.build.chalkosSchemas}/*.json $out/
    ln -s ${inventorySchema} $out/chalkctl.chalkos.dev_v1alpha1_Inventory.json
    ln -s ${labSchema} $out/chalklab.chalkos.dev_v1alpha1_Lab.json
  '';
  # The schemas of the default variant's kinds, which the page of config kinds lists.
  kindSchemas = imageSystem.config.system.build.chalkosSchemas;
  # The schemas, facts, outputs and effects of an image, which the page of built-ins lists.
  builtinsImage =
    name: system:
    pkgs.linkFarm "chalkos-docs-builtins-${name}" {
      schemas = system.config.system.build.chalkosSchemas;
      "facts.json" = system.config.system.build.chalkosFacts;
      "outputs.json" = system.config.system.build.chalkosOutputs;
      "effects.json" = system.config.system.build.chalkosEffects;
    };
  imageOptions =
    (pkgs.nixosOptionsDoc {
      # chalkos's options, and the grant of a unit, which the node modules add to systemd's.
      options = {
        inherit (imageSystem.options) chalkos;
        systemd.services = imageSystem.options.systemd.services;
      };
      transformOptions =
        opt:
        linkDeclarations opt
        // {
          visible = opt.visible && declaredIn "modules/node" opt;
        };
    }).optionsJSON;

  # The openings of the options pages, and the end of the extension API's, which docgen options
  # writes the options between.
  optionsTemplates = pkgs.linkFarm "chalkos-options-templates" {
    "mkimages.head.md" = pkgs.writeText "mkimages.head.md" ''
      ---
      title: "mkImages"
      description: "The arguments of chalkos.lib.mkImages and what it returns"
      ---

      # mkImages

      This page is for image authors who call `chalkos.lib.mkImages` in a flake. mkImages builds a
      cluster's images from its arguments: each variant on each platform, and the installer. The
      options the images' NixOS modules set are on [Image options](image-options.md) and
      [Extension API](extension-api.md).

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

      ## Return values { #return-values }

      mkImages returns an attribute set:

      - `images.<variant>-<platform>`: the image directory of a variant on a platform, such as
        `images.node-metal`, each built only when asked for.
      - `oci.<variant>-<platform>`: the image directory as an OCI image layout, as
        [`chalkbuild oci`](cli/chalkbuild_oci.md) writes it, each built only when asked for;
        [Publish images to a registry](../build/publish-images.md) signs and pushes it.
      - `installer`: the installer, a raw image and a hybrid ISO in one directory, which installs
        images of every platform.
      - `nixos.<variant>-<platform>` and `nixos.installer`: the evaluated NixOS systems, to read an
        image's options, such as `nixos.node-metal.config.chalkos.kubernetes.package.version`.
    '';
    "image-options.head.md" = pkgs.writeText "image-options.head.md" ''
      ---
      title: "Image options"
      description: "The options of chalkos's node modules that a variant's NixOS modules set"
      ---

      # Image options

      This page is for image authors who set the options of chalkos's node modules in a variant's
      NixOS modules, the `modules` of [mkImages](mkimages.md). Every image and the installer carry
      these modules. The options an extension uses to declare kinds, facts, outputs and effects
      are on [Extension API](extension-api.md).
    '';
    "extension-api.head.md" = pkgs.writeText "extension-api.head.md" ''
      ---
      title: "Extension API"
      description: "The options and lib.chalkos helpers that an extension declares kinds, facts, outputs and effects with"
      ---

      # Extension API

      This page is for authors of extensions, the NixOS modules that declare config kinds of their
      own, read a node's documents in a service, and react when they change. It lists the options
      the node modules give an extension and the helpers of `lib.chalkos`. The Build pages show
      them at work, from [Declare a config kind](../build/declare-a-kind.md) to
      [React to config changes](../build/react-to-changes.md).
    '';
    "extension-api.tail.md" = pkgs.writeText "extension-api.tail.md" ''

      ## lib.chalkos { #lib-chalkos }

      Every image's NixOS modules get `lib.chalkos` as part of `lib`. It holds the type and the
      `mkOption` of an extension kind's fields, the renderers of `chalkos.outputs.<name>.render`
      and the common effects.

      | Helper | Arguments | Result |
      | --- | --- | --- |
      | `lib.chalkos.mkOption` | `lib.mkOption`'s, and `rules`, a list of `{ rule; message or messageExpression; takes ? "refuse"; description ? null; }` | An option for a field of a kind, with the field's change rules. |
      | `lib.chalkos.types.secretRef` | — | Type of a field that holds a reference to a key of a Secret: `secretRef` with `name` and `key`. |
      | `lib.chalkos.render.template` | a path ending in `.tmpl` | Renderer of a Go `text/template` into one file, named as the template less `.tmpl`; a missing or null key prints as nothing. |
      | `lib.chalkos.render.jq` | a path ending in `.jq` | Renderer of a jq program into one file, named as the program less `.jq`; a string is written raw. |
      | `lib.chalkos.render.documents` | a list of kind names | Renderer of the documents of those kinds as they are, each as `<Kind>/<name>.json`. |
      | `lib.chalkos.restartOn` | `{ outputs ? [ ], kinds ? [ ] }`, then a list of units | Effect that restarts the units when one of the outputs, the documents of one of the kinds or a value of a Secret they reference changed. |
      | `lib.chalkos.reloadOn` | `{ outputs ? [ ], kinds ? [ ] }`, then a list of units | Effect that reloads the units when one of the outputs, the documents of one of the kinds or a value of a Secret they reference changed. |
    '';
  };
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
  # in the repository: a page per command of chalkctl, chalklab and chalkbuild, the config kinds,
  # the arguments of mkImages, the image options, the extension API, the built-ins, the node API,
  # and the glossary's terms as abbreviations, which every page appends.
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
        mkdir -p $out/docs/reference $out/includes
        install -m 644 ${../zensical.toml} $out/zensical.toml
        docgen cli $out/docs/reference/cli $out/zensical.toml ${helpLinkPages}
        docgen kinds $out/docs/reference/kinds.md ${kindSchemas} ${inventorySchema} ${labSchema}
        docgen options $out/docs/reference ${optionsTemplates} ${argumentOptions}/share/doc/nixos/options.json ${imageOptions}/share/doc/nixos/options.json
        docgen builtins $out/docs/reference/builtins.md ${builtinsImage "docs" imageSystem} ${builtinsImage "plain" docsImages.nixos.plain-metal}
        cp -r $api proto
        chmod -R u+w proto
        (cd proto && buf generate)
        cp proto/docs/reference/api.md $out/docs/reference/api.md
        docgen abbr ${../docs/reference/glossary.md} $out/includes/abbreviations.md
      '';

  # The site builds strictly; the chalkctl, chalklab, chalkbuild and chalkcfg commands its pages
  # show exist; the config documents they show are valid against an image's, the inventory's and
  # the lab file's schemas; and the Nix files they show parse.
  check =
    pkgs.runCommand "chalkos-docs-check"
      {
        nativeBuildInputs = [
          docgen
          pkgs.nixfmt
        ];
      }
      ''
        cd ${src}
        docgen check docs
        docgen yaml docs ${snippetSchemas}
        docgen nix docs $TMPDIR/nix
        status=0
        # A fragment parses as an expression, or within braces as the attributes of a module.
        for f in $TMPDIR/nix/*.nix; do
          [ -e "$f" ] || continue
          case "$f" in
            *.fragment.nix)
              nixfmt - < "$f" > /dev/null 2>&1 || { echo '{'; cat "$f"; echo '}'; } | nixfmt - > /dev/null
              ;;
            *) nixfmt - < "$f" > /dev/null ;;
          esac || {
            echo "error: $(basename "$f"), a Nix block of the documentation named by its page and first line, does not parse" >&2
            status=1
          }
        done
        [ "$status" = 0 ]
        test -d ${site}
        touch $out
      '';

  # Fails when the generated pages in the repository differ from what the code generates.
  generated = pkgs.runCommand "chalkos-docs-generated" { } ''
    status=0
    for path in docs/reference/cli docs/reference/kinds.md docs/reference/mkimages.md docs/reference/image-options.md docs/reference/extension-api.md docs/reference/builtins.md docs/reference/api.md includes/abbreviations.md zensical.toml; do
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
      cp --no-preserve=mode ${reference}/docs/reference/kinds.md ${reference}/docs/reference/mkimages.md ${reference}/docs/reference/image-options.md ${reference}/docs/reference/extension-api.md ${reference}/docs/reference/builtins.md ${reference}/docs/reference/api.md docs/reference/
      cp --no-preserve=mode ${reference}/zensical.toml zensical.toml
      mkdir -p includes
      cp --no-preserve=mode ${reference}/includes/abbreviations.md includes/
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
        rsync -a includes/ "$work/includes/"
        mkdir -p "$work/examples/homelab"
        rsync -a examples/homelab/flake.nix "$work/examples/homelab/flake.nix"
        rsync -a --delete examples/homelab/extensions/ "$work/examples/homelab/extensions/"
        rsync -a examples/homelab/chalkos.yaml "$work/examples/homelab/chalkos.yaml"
        rsync -a --delete examples/homelab/nodes/ "$work/examples/homelab/nodes/"
        rsync -a --delete examples/homelab/cluster/ "$work/examples/homelab/cluster/"
      }
      copy
      while sleep 1; do copy; done &
      syncer=$!
      zensical serve -f "$work/zensical.toml" "$@"
    '';
  };
}
