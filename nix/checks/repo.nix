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
  # The inventories of the e2e fixtures that an e2e test has chalkctl validate refuse, each with
  # the refusal it wants; router_test.go names the same.
  refusedFixtures = {
    "chalklab/router-twice.yaml" =
      "the config is refused: BGPPeer/fabric: spec.interfaces: BGPPeer again and BGPPeer fabric both name enp0s3 in the VRF default; give each session one peer";
  };
  # The homelab example's flake, with this flake as its chalkos.
  homelab = (import ../../examples/homelab/flake.nix).outputs {
    chalkos = self;
    self = { };
  };
  # chalkctl validate of a tree whose Cluster document pins every image: a reference with a tag
  # and no digest fails the check, and so does an API server that no longer follows the kubelet
  # of nixpkgs's Kubernetes release.
  validatePinned = ''
    chalkctl validate 2>warnings || { cat warnings >&2; exit 1; }
    cat warnings >&2
    if grep -q "names a tag but no digest" warnings; then
      echo "error: the Cluster document names an image without a digest" >&2
      exit 1
    fi
    grep -qF "kube-apiserver:v${pkgs.kubernetes.version}@sha256:" cluster/cluster.yaml || {
      echo "error: cluster/cluster.yaml names no kube-apiserver:v${pkgs.kubernetes.version} by its digest, the kubelet's release" >&2
      exit 1
    }
  '';
  # A check that builds the Go code of the whole tree, its tests included, in its buildPhase.
  goCheck =
    args:
    pkgs.chalkos.buildGoProgram (
      {
        paths = pkgs.chalkos.treeGoPaths;
        withTests = true;
        installPhase = "touch $out";
      }
      // args
    );
in
{
  go-unit = goCheck (
    {
      pname = "chalkos-go-unit";
      nativeBuildInputs = testEnv.tools;
      # With the VM checks running alongside, a package's tests can take longer than go test's
      # default of 10 minutes.
      buildPhase = ''
        runHook preBuild
        ${checkLib.runTests "go test -v -timeout 30m ./pkg/... ./cmd/..."}
        runHook postBuild
      '';
    }
    // testEnv.vars
  );

  # The Go code, the e2e tests included, is gofmt-formatted and passes go vet and staticcheck,
  # with the checks staticcheck.conf selects; pkg/imagecheck also with the tag vmtest of its VM
  # test. Each program built from source paths of its own lists the directories of the packages it
  # imports: every one lies below a path, and every path holds one.
  go-lint = goCheck {
    pname = "chalkos-go-lint";
    nativeBuildInputs = [
      pkgs.chalkos.go.tools.staticcheck.latest
      pkgs.jq
    ];
    goPrograms = builtins.toJSON (
      map
        (
          name:
          let
            program = pkgs.chalkos.${name};
          in
          {
            inherit name;
            inherit (program) subPackages;
            paths = map (lib.removePrefix "./") program.goPaths;
          }
        )
        [
          "chalkbuild"
          "chalkcfg"
          "chalkd"
          "chalkos-console"
          "chalkos-networkd-schema"
          "chalkos-render"
          "chalkos-rules"
          "chalkos-storage"
        ]
    );
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
      go vet -tags vmtest ./pkg/imagecheck
      XDG_CACHE_HOME=$TMPDIR/cache staticcheck ./pkg/... ./cmd/... ./test/...
      XDG_CACHE_HOME=$TMPDIR/cache staticcheck -tags vmtest ./pkg/imagecheck

      failed=
      for name in $(jq -r '.[].name' <<<"$goPrograms"); do
        program=$(jq -c --arg name "$name" '.[] | select(.name == $name)' <<<"$goPrograms")
        listed=$(jq -r '.paths[]' <<<"$program")
        imported=$(go list -deps -f '{{.Dir}}' $(jq -r '.subPackages[] | "./" + .' <<<"$program") \
          | sed -n "s|^$PWD/||p" | sort -u)
        for dir in $imported; do
          covered=
          for path in $listed; do
            case "$dir/" in "$path"/*) covered=1 ;; esac
          done
          if [ -z "$covered" ]; then
            echo "error: $name imports ./$dir, which nix/$name.nix does not list" >&2
            failed=1
          fi
        done
        for path in $listed; do
          used=
          for dir in $imported; do
            case "$dir/" in "$path"/*) used=1 ;; esac
          done
          if [ -z "$used" ]; then
            echo "error: nix/$name.nix lists ./$path, of which $name imports no package" >&2
            failed=1
          fi
        done
      done
      [ -z "$failed" ]
      runHook postBuild
    '';
  };

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
  # image where their Image points, and validatePinned checks its Cluster document's images;
  # TestTheLabFiles of go-unit checks chalklab.yaml's MAC addresses. The flake's mkImages gets the
  # test secrets' OS CA in place of the secrets.pub.json chalkctl gen secrets writes, which the
  # repository does not hold.
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
      pkgs.chalkos.chalklab
      pkgs.chalkos.chalkctl
      pkgs.kubectl
    ];
    assert lib.isString shell.drvPath;
    # nixosConfigurations holds the system the image is built from, whose outputs the guides build.
    assert
      template.nixosConfigurations.node-kvm.config.system.build.chalkosImage.drvPath
      == template.packages.${system}.node-kvm.drvPath;
    # checks holds the tests of the system the image is built from, which nix flake check runs.
    assert
      template.checks.${system}.node-kvm.drvPath
      == template.nixosConfigurations.node-kvm.config.system.build.chalkosTests.drvPath;
    checkLib.validateDirectory {
      name = "template-lab";
      src = ../../templates/lab;
      images."images/node-kvm" = template.packages.${system}.node-kvm;
      command = validatePinned;
    };

  # The homelab example: its flake builds the router's metal image and the nodes' with the BGP
  # extension, and chalkctl validate accepts the router's and the nodes' YAML, Speaker and Peer
  # documents included, with those images where their groups point, and validatePinned checks
  # its Cluster document's images.
  example-homelab =
    let
      packages = homelab.packages.${pkgs.stdenv.hostPlatform.system};
    in
    # The installer, which the guide writes to a USB stick, evaluates.
    assert lib.isString packages.installer.drvPath;
    # nixosConfigurations holds the systems the images are built from, whose outputs the guides
    # build, such as declare-a-kind.md's schemas.
    assert lib.all
      (
        image:
        homelab.nixosConfigurations.${image}.config.system.build.chalkosImage.drvPath
        == packages.${image}.drvPath
      )
      [
        "router-metal"
        "node-metal"
      ];
    # checks holds the tests of the systems the images are built from, which nix flake check runs.
    assert lib.all
      (
        image:
        homelab.checks.${pkgs.stdenv.hostPlatform.system}.${image}.drvPath
        == homelab.nixosConfigurations.${image}.config.system.build.chalkosTests.drvPath
      )
      [
        "router-metal"
        "node-metal"
      ];
    checkLib.validateDirectory {
      name = "example-homelab";
      src = ../../examples/homelab;
      images = {
        "images/router-metal" = packages.router-metal;
        "images/node-metal" = packages.node-metal;
      };
      command = validatePinned;
    };

  # The extensions example: its flake builds a metal image with the Rack and Hardware extensions
  # and Kubernetes off, and chalkctl validate accepts its node's YAML, Rack and Hardware documents
  # included, with that image where its Image points.
  example-extensions =
    let
      example = (import ../../examples/extensions/flake.nix).outputs {
        chalkos = self;
        self = { };
      };
    in
    # nixosConfigurations holds the system the image is built from, whose outputs the guides build.
    assert
      example.nixosConfigurations.node-metal.config.system.build.chalkosImage.drvPath
      == example.packages.${pkgs.stdenv.hostPlatform.system}.node-metal.drvPath;
    # checks holds the tests of the system the image is built from, which nix flake check runs.
    assert
      example.checks.${pkgs.stdenv.hostPlatform.system}.node-metal.drvPath
      == example.nixosConfigurations.node-metal.config.system.build.chalkosTests.drvPath;
    checkLib.validateDirectory {
      name = "example-extensions";
      src = ../../examples/extensions;
      images."images/node-metal" = example.packages.${pkgs.stdenv.hostPlatform.system}.node-metal;
    };

  # validate-config.md shows the report of the refusal that the homelab's test
  # bgp-unconfigured-peer-interface expects of its validator bgp-peers. The page keeps the report
  # on one line of its own that starts with {"refusals": [{"kind": "Peer", "name": "router", which
  # is how the check finds it.
  example-homelab-validator-docs =
    let
      inherit (homelab.nixosConfigurations.node-metal.config.chalkos) tests;
    in
    pkgs.runCommand "chalkos-example-homelab-validator-docs"
      {
        nativeBuildInputs = [ pkgs.jq ];
        page = ../../docs/build/validate-config.md;
        expected = builtins.toJSON { inherit (tests.bgp-unconfigured-peer-interface) refusals; };
      }
      ''
        documented=$(grep '^{"refusals": \[{"kind": "Peer", "name": "router"' "$page" | jq -cS .)
        if [ -z "$documented" ]; then
          echo "error: $page shows no report of the Peer router" >&2
          exit 1
        fi
        expected=$(jq -cS . <<<"$expected")
        if [ "$documented" != "$expected" ]; then
          echo "error: $page shows $documented, not the test's $expected" >&2
          exit 1
        fi
        touch $out
      '';

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

  # The Nix files are formatted as nix fmt formats them, hold no unused binding or argument
  # (deadnix) and pass statix, except for the lints statix.toml leaves out.
  nix-lint =
    pkgs.runCommand "chalkos-nix-lint"
      {
        nativeBuildInputs = [
          pkgs.nixfmt
          pkgs.deadnix
          pkgs.statix
        ];
        src = lib.fileset.toSource {
          root = ../..;
          fileset = lib.fileset.unions [
            (lib.fileset.fileFilter (f: f.hasExt "nix") ../..)
            ../../statix.toml
          ];
        };
      }
      ''
        cd $src
        nixfmt --check $(find . -name '*.nix' | sort)
        deadnix --fail .
        statix check --config statix.toml .
        touch $out
      '';

  # The documentation site builds strictly and shows only commands that exist.
  docs = docs.check;
  # The pages generated from the code are those in the repository.
  docs-generated = docs.generated;

  # Every node of the e2e fixtures has config its image accepts, as chalkctl validate checks it
  # against the directory of its image in the inventory, but for the inventories the e2e tests
  # want refused, which chalkctl validate refuses with their message.
  fixtures = checkLib.validateDirectory {
    name = "fixtures";
    src = ../../test/fixtures/clusters;
    images = fixtureImages;
    # Every inventory of every cluster; the clusters' other files at the top, such as lab files,
    # are no inventories. Every image the clusters name is one the e2e tests' registry serves.
    command = ''
      inventories=$(grep -l '^kind: Inventory$' */*.yaml)
      test -n "$inventories"
      for inventory in $inventories; do
        case "$inventory" in
          ${lib.concatStrings (
            lib.mapAttrsToList (inventory: message: ''
              ${inventory})
                if refusal=$(chalkctl validate --inventory "$inventory" 2>&1); then
                  echo "$inventory: chalkctl validate accepts it" >&2
                  exit 1
                fi
                grep -qF ${lib.escapeShellArg message} <<<"$refusal" || { echo "$refusal" >&2; exit 1; }
                ;;
            '') refusedFixtures
          )}
          *) chalkctl validate --inventory "$inventory" ;;
        esac
      done
      # The images the clusters name, and every image they name by its digest, are those the e2e
      # tests' registry serves.
      for ref in $(grep -rhoE '[^ "]+@sha256:[0-9a-f]{64}|(registry\.k8s\.io|ghcr\.io)/[^ "]+' . | sort -u); do
        ${pkgs.jq}/bin/jq -e --arg r "$ref" 'has($r)' ${chalkPkgs.test-kubernetes-images}/images.json >/dev/null || {
          echo "error: the fixtures name $ref, which nix/testing/kubernetes-images.nix does not serve" >&2
          exit 1
        }
      done
    '';
  };
}
