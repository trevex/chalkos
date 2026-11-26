# The documentation site, built with Zensical from zensical.toml and docs/, and the reference
# pages generated from the code. The flake's source holds tracked files alone, so
# docs/superpowers, which git ignores, never reaches the site.
{
  pkgs,
  self,
  fixtures,
}:
let
  inherit (pkgs) lib;
  # The files of the examples and the templates that the pages include as snippets, by their
  # directories.
  snippets = {
    "examples/homelab" = [
      "flake.nix"
      "chalkos.yaml"
      "chalkos-air-gapped.yaml"
      "nodes"
      "cluster"
      "router"
      "bgp"
      "air-gapped"
    ];
    "examples/bgp-extension" = [
      "flake.nix"
      "extensions"
    ];
    "examples/extensions" = [
      "rack.nix"
      "hardware.nix"
      "hardware.env.tmpl"
      "hardware-examples"
      "tests"
    ];
    "templates/lab" = [
      "chalkos.yaml"
      "cluster"
    ];
    "templates/router-lab" = [
      "chalkos.yaml"
      "nodes"
    ];
    "templates/cluster-metal" = [
      "flake.nix"
      "chalkos.yaml"
      "cluster"
      "nodes"
    ];
  };
  # The site's files, the snippets, and SECURITY.md, which the security page includes.
  src = lib.fileset.toSource {
    root = ../../.;
    fileset = lib.fileset.unions (
      [
        ../../zensical.toml
        ../../docs
        ../../SECURITY.md
      ]
      ++ lib.concatLists (lib.mapAttrsToList (dir: map (path: ../../. + "/${dir}/${path}")) snippets)
    );
  };
  # The Go files of cmd/ and pkg/, whose directories the page of packages and programs lists.
  goSources = lib.fileset.toSource {
    root = ../../.;
    fileset = lib.fileset.unions (
      map (lib.fileset.fileFilter (f: f.hasExt "go")) [
        ../../cmd
        ../../pkg
      ]
    );
  };
  # The index pages, whose regions of cards docgen fills from the front matter of the pages they
  # name.
  indexPages = [
    "get-started/index.md"
    "guides/index.md"
    "operate/index.md"
    "kubernetes/index.md"
    "router/index.md"
    "build/index.md"
    "how-it-works/index.md"
    "reference/index.md"
    "contribute/index.md"
    "contribute/internals/index.md"
  ];

  # The pages and files docgen writes, as they lie in the repository.
  generatedPaths = [
    "docs/reference/cli"
    "docs/reference/kinds.md"
    "docs/reference/mkimages.md"
    "docs/reference/image-options.md"
    "docs/reference/extension-api.md"
    "docs/reference/builtins.md"
    "docs/reference/api.md"
    "docs/includes/abbreviations.md"
    "docs/includes/features-router.md"
    "docs/includes/features-kubernetes.md"
    "zensical.toml"
  ]
  ++ map (page: "docs/${page}") indexPages;

  # The pages that title the links of command help to the site: every page but the generated
  # command pages, which would otherwise feed their own generation.
  helpLinkPages = lib.fileset.toSource {
    root = ../../docs;
    fileset = lib.fileset.difference ../../docs ../../docs/reference/cli;
  };

  inherit (pkgs.chalkos) docgen;

  # Declarations link to the repository rather than to the store path of the flake's source.
  root = toString ../../.;
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

  # The arguments of chalkos.lib.mkImages, from the modules that check them, which the file
  # declares as attributes, so their options are declared in it.
  arguments = lib.evalModules {
    modules = [
      {
        _file = ../../lib/images-arguments.nix;
        imports =
          let
            modules = import ../../lib/images-arguments.nix;
          in
          [
            modules.shared
            modules.images
          ];
      }
    ];
    specialArgs.defaultNixpkgs = self.inputs.nixpkgs;
  };
  argumentOptions =
    (pkgs.nixosOptionsDoc {
      options = removeAttrs arguments.options [ "_module" ];
      transformOptions = linkDeclarations;
    }).optionsJSON;

  # The documentation is generated and checked against the images of nix/testing/fixtures.nix: one
  # with the Kubernetes layer, whose options the reference lists, one without a layer, whose
  # built-ins are those of every image, a router image, whose kinds and built-ins router images
  # add, and one with the examples' extensions, whose documents the pages show as an extension's.
  # An image's own options are those the node modules declare.
  imageSystem = fixtures.docs-metal.nixos;
  # The schemas of chalkctl's inventory and chalklab's lab file, which they are built with.
  inventorySchema = ../../pkg/inventory/chalkctl.chalkos.dev_v1alpha1_Inventory.json;
  labSchema = ../../pkg/chalklab/chalklab.chalkos.dev_v1alpha1_Lab.json;
  # The schemas of the kinds router images add.
  routerSchemas = "${fixtures.edge-metal.nixos.config.system.build.chalkosSchemas}/router.chalkos.dev_*.json";
  # The schemas the pages' config documents are checked against: the kinds of the image with the
  # Rack, Hardware and BGP extensions, those router images add, the inventory and the lab file.
  snippetSchemas = pkgs.runCommand "chalkos-docs-schemas" { } ''
    mkdir $out
    ln -s ${fixtures.examples-metal.nixos.config.system.build.chalkosSchemas}/*.json $out/
    ln -s ${routerSchemas} $out/
    ln -s ${inventorySchema} $out/chalkctl.chalkos.dev_v1alpha1_Inventory.json
    ln -s ${labSchema} $out/chalklab.chalkos.dev_v1alpha1_Lab.json
  '';
  # The schemas of the Kubernetes image's kinds and of those router images add, which the page of
  # config kinds lists.
  kindSchemas = pkgs.runCommand "chalkos-docs-kind-schemas" { } ''
    mkdir $out
    ln -s ${imageSystem.config.system.build.chalkosSchemas}/*.json ${routerSchemas} $out/
  '';
  # The schemas, facts, validators, outputs, effects and diagnostics of an image, which the page of
  # built-ins lists.
  builtinsImage =
    name: system:
    pkgs.linkFarm "chalkos-docs-builtins-${name}" {
      schemas = system.config.system.build.chalkosSchemas;
      "facts.json" = system.config.system.build.chalkosFacts;
      "validators.json" = system.config.system.build.chalkosValidators;
      "outputs.json" = system.config.system.build.chalkosOutputs;
      "effects.json" = system.config.system.build.chalkosEffects;
      "diagnostics.json" = system.config.system.build.chalkosDiagnostics;
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
  optionsTemplates = ./templates;

  # A table of the features of a layer, from the options chalkosLib.internal.feature declares
  # below chalkos.<layer>: the option, its default, what it carries and the kinds an image without
  # it does not accept.
  featuresTable =
    layer: options:
    let
      features = lib.filterAttrs (_: v: v ? enable.chalkosFeature) options;
      code = s: "`${s}`";
      row =
        name: v:
        let
          f = v.enable.chalkosFeature;
        in
        "| ${code "chalkos.${layer}.${name}.enable"} | ${code (lib.boolToString f.default)} | ${lib.removeSuffix "," f.carries} | ${
          if f.kinds == [ ] then "none" else lib.concatMapStringsSep ", " code f.kinds
        } |";
    in
    pkgs.writeText "features-${layer}.md" ''
      <!-- Generated by nix run .#docgen from the features' declarations; do not edit. -->

      | Option | Default | What images carry | Kinds an image accepts only with it |
      | --- | --- | --- | --- |
      ${lib.concatStringsSep "\n" (lib.mapAttrsToList row features)}
    '';
  # The features tables, by the include file each is written to.
  featuresTables = {
    "docs/includes/features-router.md" =
      featuresTable "router" fixtures.edge-metal.nixos.options.chalkos.router;
    "docs/includes/features-kubernetes.md" =
      featuresTable "kubernetes" imageSystem.options.chalkos.kubernetes;
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
        python3 ${./nav.py} zensical.toml
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
          pkgs.chalkos.protoc-gen-connect-go
          pkgs.protoc-gen-doc
        ];
        api = lib.fileset.toSource {
          root = ../../.;
          fileset = lib.fileset.unions [
            ../../buf.yaml
            ../../buf.gen.yaml
            ../../api
          ];
        };
      }
      ''
        export HOME=$TMPDIR
        mkdir -p $out/docs/reference $out/docs/includes
        install -m 644 ${../../zensical.toml} $out/zensical.toml
        docgen cli $out/docs/reference/cli $out/zensical.toml ${helpLinkPages}
        docgen cards $out/docs ${helpLinkPages} ${lib.escapeShellArgs indexPages}
        docgen kinds $out/docs/reference/kinds.md ${kindSchemas} ${snippetSchemas}/chalkctl.chalkos.dev_v1alpha1_Inventory.json ${snippetSchemas}/chalklab.chalkos.dev_v1alpha1_Lab.json
        docgen options $out/docs/reference ${optionsTemplates} ${argumentOptions}/share/doc/nixos/options.json ${imageOptions}/share/doc/nixos/options.json
        docgen builtins $out/docs/reference/builtins.md ${builtinsImage "docs" imageSystem} ${builtinsImage "plain" fixtures.plain-metal.nixos} ${builtinsImage "edge" fixtures.edge-metal.nixos}
        cp -r $api proto
        chmod -R u+w proto
        (cd proto && buf generate)
        cp proto/docs/reference/api.md $out/docs/reference/api.md
        docgen abbr ${../../docs/reference/glossary.md} $out/docs/includes/abbreviations.md
        ${lib.concatStrings (
          lib.mapAttrsToList (path: table: "install -m 644 ${table} $out/${path}\n") featuresTables
        )}
      '';

  # The site builds strictly; the chalkctl, chalklab, chalkbuild and chalkcfg commands its pages
  # show exist; the config documents they show are valid against an image's, the inventory's and
  # the lab file's schemas; the Nix files they show parse; and the page of packages and programs
  # has a row for each directory of Go code.
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
        docgen packages docs/contribute/packages.md ${goSources}
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
    for path in ${lib.escapeShellArgs generatedPaths}; do
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
      # The command pages go first, so a command that no longer exists leaves no page.
      rm -rf docs/reference/cli
      for path in ${lib.escapeShellArgs generatedPaths}; do
        mkdir -p "$(dirname "$path")"
        cp -r --no-preserve=mode "${reference}/$path" "$(dirname "$path")/"
      done
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
        rsync -a SECURITY.md "$work/SECURITY.md"
        ${lib.concatStrings (
          lib.mapAttrsToList (dir: paths: ''
            mkdir -p "$work/${dir}"
            rsync -a --delete ${lib.escapeShellArgs (map (path: "${dir}/${path}") paths)} "$work/${dir}/"
          '') snippets
        )}
      }
      copy
      while sleep 1; do copy; done &
      syncer=$!
      zensical serve -f "$work/zensical.toml" "$@"
    '';
  };
}
