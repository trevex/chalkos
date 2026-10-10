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
          path = lib.removePrefix "${root}/" (toString d);
        in
        {
          name = path;
          url = "${repository}/${path}";
        }
      ) opt.declarations;
    };
  declaredIn = dir: opt: lib.all (d: lib.hasPrefix "${root}/${dir}/" (toString d)) opt.declarations;

  cluster = lib.evalModules {
    modules = [ ../modules/cluster ];
    specialArgs = { inherit (self.inputs) nixpkgs; };
  };
  clusterOptions =
    (pkgs.nixosOptionsDoc {
      options = { inherit (cluster.options) chalkos; };
      transformOptions = linkDeclarations;
    }).optionsCommonMark;

  # A role image's own options, those the node modules declare. The role builder adds the
  # cluster's settings to a role image read-only; they are documented with the cluster's options.
  roleSystem =
    (self.lib.mkCluster {
      modules = [
        {
          chalkos.cluster = {
            name = "docs";
            endpoint = "https://127.0.0.1:6443";
          };
          chalkos.roles.docs = { };
        }
      ];
    }).roles.docs.nixos.metal;
  nodeOptions =
    (pkgs.nixosOptionsDoc {
      options = { inherit (roleSystem.options) chalkos; };
      transformOptions =
        opt:
        linkDeclarations opt
        // {
          visible = opt.visible && opt.name != "chalkos" && declaredIn "modules/node" opt;
        };
    }).optionsCommonMark;

  optionsHead = pkgs.writeText "options-head.md" ''
    ---
    title: "Cluster and node options"
    description: "The options of a chalkos cluster definition and of its role images"
    ---

    # Cluster and node options

    A cluster definition sets the options under `chalkos` of the cluster modules, which
    `chalkos.lib.mkCluster` evaluates; a role's `nixosModules` set the options of its role image,
    a NixOS system with the node modules. This page is generated from those modules with
    nixosOptionsDoc; each option links to its declaration.

    ## Cluster definition

  '';
  nodeHead = pkgs.writeText "options-node-head.md" ''

    ## Role images

    The options the node modules declare in each role image. The cluster's settings appear in a
    role image under the same names as in the cluster definition, read-only.

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
  # in the repository: a page per command of chalkctl and chalklab, the cluster and node options,
  # and the node API.
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
          sed 's/^## /### /' ${clusterOptions}
          cat ${nodeHead}
          sed 's/^## /### /' ${nodeOptions}
        } > $out/docs/reference/options.md
        cp -r $api proto
        chmod -R u+w proto
        (cd proto && buf generate)
        cp proto/docs/reference/api.md $out/docs/reference/api.md
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
      trap 'kill "$syncer" 2>/dev/null; rm -rf "$work"' EXIT
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
