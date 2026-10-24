# Checks of the repository's code, documentation, workflows, template and examples.
{
  pkgs,
  self,
  checkLib,
  docs,
  ...
}:
let
  inherit (pkgs) lib;
  inherit (checkLib) chalkPkgs testEnv;
  # The image directories the e2e fixtures' inventories name, by their paths below
  # test/fixtures/clusters. images.json there names, for each path, the variable that names the
  # image in the e2e tests, which read it too.
  fixtureImages = lib.mapAttrs (_: variable: testEnv.imageDirs.${variable}) (
    lib.importJSON ../../test/fixtures/clusters/images.json
  );
in
{
  go-unit = chalkPkgs.chalkctl.overrideAttrs (
    old:
    {
      pname = "chalkos-go-unit";
      nativeBuildInputs = old.nativeBuildInputs ++ testEnv.tools;
      # With the VM checks running alongside, a package's tests can take longer than go test's
      # default of 10 minutes.
      buildPhase = ''
        runHook preBuild
        ${checkLib.runTests "go test -v -timeout 30m ./pkg/... ./cmd/..."}
        runHook postBuild
      '';
      doCheck = false;
      installPhase = "touch $out";
      postFixup = "";
    }
    // testEnv.vars
  );

  # The Go code, the e2e tests included, is gofmt-formatted and passes go vet and staticcheck,
  # with the checks staticcheck.conf selects.
  go-lint = chalkPkgs.chalkctl.overrideAttrs (old: {
    pname = "chalkos-go-lint";
    nativeBuildInputs = old.nativeBuildInputs ++ [ pkgs.go-tools ];
    buildPhase = ''
      runHook preBuild
      cp ${../../staticcheck.conf} staticcheck.conf
      unformatted=$(gofmt -l pkg cmd test)
      if [ -n "$unformatted" ]; then
        echo "error: gofmt would change these files:" >&2
        echo "$unformatted" >&2
        exit 1
      fi
      go vet ./pkg/... ./cmd/... ./test/...
      XDG_CACHE_HOME=$TMPDIR/cache staticcheck ./pkg/... ./cmd/... ./test/...
      runHook postBuild
    '';
    doCheck = false;
    installPhase = "touch $out";
    postFixup = "";
  });

  # The generated API code is committed; it must match what buf generates from the proto files.
  api-generated =
    pkgs.runCommand "chalkos-api-generated"
      {
        nativeBuildInputs = [
          pkgs.buf
          pkgs.protoc-gen-go
          pkgs.protoc-gen-connect-go
          pkgs.protoc-gen-doc
        ];
        src = pkgs.lib.fileset.toSource {
          root = ../../.;
          fileset = pkgs.lib.fileset.unions [
            ../../buf.yaml
            ../../buf.gen.yaml
            ../../api
            ../../pkg/api
          ];
        };
      }
      ''
        export HOME=$TMPDIR
        cp -r $src work
        chmod -R u+w work
        cd work
        buf lint
        rm -r pkg/api
        buf generate
        diff -ru $src/pkg/api pkg/api
        touch $out
      '';

  # The lab template: its flake builds the nodes' kvm image with mkImages, and its dev shell
  # brings chalklab, chalkctl and kubectl. chalkctl validate accepts the template's nodes with that
  # image where their Image points; TestTheLabFiles of go-unit checks chalklab.yaml's MAC
  # addresses. The flake's mkImages gets the test secrets' OS CA in place of the secrets.pub.json
  # chalkctl gen secrets writes, which the repository does not hold.
  template-lab =
    let
      system = pkgs.stdenv.hostPlatform.system;
      chalkos = self // {
        lib = self.lib // {
          # The template must name its secrets.pub.json, which the check replaces.
          mkImages =
            arguments:
            assert lib.assertMsg (arguments ? osCA) "the lab template's flake passes mkImages no osCA";
            self.lib.mkImages (arguments // { osCA = "${chalkPkgs.test-secrets}/secrets.pub.json"; });
        };
      };
      # A flake of no git repository has no revision.
      template = (import ../../templates/lab/flake.nix).outputs {
        inherit chalkos;
        self = { };
      };
      shell = template.devShells.${system}.default;
    in
    assert lib.all (p: lib.elem p shell.nativeBuildInputs) [
      chalkPkgs.chalklab
      chalkPkgs.chalkctl
      pkgs.kubectl
    ];
    assert lib.isString shell.drvPath;
    checkLib.validateDirectory {
      name = "template-lab";
      src = ../../templates/lab;
      images."images/node-kvm" = template.packages.${system}.node-kvm;
    };

  # The homelab example: its flake builds the nodes' metal image with the Rack, Hardware and BGP
  # extensions, and chalkctl validate accepts its nodes' YAML, Rack, Hardware and Peer documents
  # included, with that image where their Image points.
  example-homelab =
    let
      homelab = (import ../../examples/homelab/flake.nix).outputs {
        chalkos = self;
        self = { };
      };
    in
    checkLib.validateDirectory {
      name = "example-homelab";
      src = ../../examples/homelab;
      images."images/node-metal" = homelab.packages.${pkgs.stdenv.hostPlatform.system}.node-metal;
    };

  # The GitHub workflows pass actionlint, with shellcheck for their scripts.
  workflows =
    pkgs.runCommand "chalkos-workflows"
      {
        nativeBuildInputs = [
          pkgs.actionlint
          pkgs.shellcheck
        ];
        src = lib.fileset.toSource {
          root = ../../.;
          fileset = ../../.github/workflows;
        };
      }
      ''
        cd $src
        actionlint -shellcheck shellcheck .github/workflows/*.yml
        touch $out
      '';

  # The documentation site builds strictly and shows only commands that exist.
  docs = docs.check;
  # The pages generated from the code are those in the repository.
  docs-generated = docs.generated;

  # Every node of the e2e fixtures has config its image accepts, as chalkctl validate checks it
  # against the directory of its image in the inventory.
  fixtures = checkLib.validateDirectory {
    name = "fixtures";
    src = ../../test/fixtures/clusters;
    images = fixtureImages;
    # Every inventory of every cluster; the clusters' other files at the top, such as lab files,
    # are no inventories.
    command = ''
      inventories=$(grep -l '^kind: Inventory$' */*.yaml)
      test -n "$inventories"
      for inventory in $inventories; do
        chalkctl validate --inventory "$inventory"
      done
    '';
  };
}
