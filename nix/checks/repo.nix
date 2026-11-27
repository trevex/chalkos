# Checks of the repository's code, documentation, workflows, templates and examples.
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
  # Every file of the repository, by its path relative to the root.
  repoFiles =
    let
      walk =
        dir: path:
        lib.concatLists (
          lib.mapAttrsToList (
            name: type:
            let
              sub = if path == "" then name else "${path}/${name}";
            in
            if type == "directory" then walk (dir + "/${name}") sub else [ sub ]
          ) (builtins.readDir dir)
        );
    in
    walk ../.. "";
  # The image directories the e2e fixtures' inventories name, by their paths below
  # test/fixtures/clusters. images.json there names, for each path, the variable that names the
  # image in the e2e tests, which read it too.
  fixtureImages = lib.mapAttrs (_: variable: testEnv.imageDirs.${variable}) (
    lib.importJSON ../../test/fixtures/clusters/images.json
  );
  # The inventories of the e2e fixtures that chalkctl validate refuses, each with the refusal it
  # wants.
  refusedFixtures = {
    "chalklab/router-twice.yaml" =
      "the config is refused: BGPPeer/fabric: spec.interfaces: BGPPeer again and BGPPeer fabric both name enp0s3 in the VRF default; give each session one peer";
  };
  # The homelab example's flake, with this flake as its chalkos.
  homelab = (import ../../examples/homelab/flake.nix).outputs {
    chalkos = self;
    self = { };
  };
  bgpExtension = (import ../../examples/bgp-extension/flake.nix).outputs {
    chalkos = self;
    self = { };
  };
  # The newest Kubernetes release chalkos pins, whose kubelet the templates' and examples' images
  # run.
  kubernetes =
    let
      inherit (self.lib) kubernetesReleases;
    in
    kubernetesReleases.${lib.last (lib.sort lib.versionOlder (lib.attrNames kubernetesReleases))}.version;
  # chalkctl validate of a tree whose Cluster document pins every image: a reference with a tag
  # and no digest fails the check, and so does an API server that no longer follows the kubelet
  # of that release.
  validatePinned = ''
    chalkctl validate 2>warnings || { cat warnings >&2; exit 1; }
    cat warnings >&2
    if grep -q "names a tag but no digest" warnings; then
      echo "error: the Cluster document names an image without a digest" >&2
      exit 1
    fi
    grep -qF "kube-apiserver:v${kubernetes}@sha256:" cluster/cluster.yaml || {
      echo "error: cluster/cluster.yaml names no kube-apiserver:v${kubernetes} by its digest, the kubelet's release" >&2
      exit 1
    }
  '';
  # The check of a template: its flake's dev shell brings the tools it names, and chalkctl
  # validate, or command, accepts the template's nodes with the images it builds where their
  # inventory points, each image at images/<name>. The flake's mkImages gets the test secrets' OS
  # CA in place of the secrets.pub.json chalkctl gen secrets writes, which the repository does not
  # hold. An image the inventory takes from a registry, on a line of its own, must be a published
  # image of this release and one the flake builds too: the check reads no registry, but takes the
  # flake's build of the image's name in its place, which has the published image's layers.
  templateCheck =
    {
      name,
      tools,
      command ? "chalkctl validate",
    }:
    let
      system = pkgs.stdenv.hostPlatform.system;
      chalkos = self // {
        lib = self.lib // {
          # The template must name its secrets.pub.json, which the check replaces.
          mkImages =
            arguments:
            assert lib.assertMsg (arguments ? osCA) "the ${name} template's flake passes mkImages no osCA";
            self.lib.mkImages (arguments // { osCA = "${chalkPkgs.test-secrets}/secrets.pub.json"; });
        };
      };
      # A flake of no git repository has no revision.
      template = (import ../../templates/${name}/flake.nix).outputs {
        inherit chalkos;
        self = { };
      };
      shell = template.devShells.${system}.default;
      packages = template.packages.${system};
      # The inventory's images from a registry, by name: the reference of each.
      published = lib.listToAttrs (
        lib.concatMap (
          line:
          let
            m = builtins.match " +([a-z0-9-]+): [{]source: [{]oci: ([^}]+)[}][}].*" line;
          in
          lib.optional (m != null) (lib.nameValuePair (lib.elemAt m 0) (lib.elemAt m 1))
        ) (lib.splitString "\n" (builtins.readFile ../../templates/${name}/chalkos.yaml))
      );
      isPublished =
        image: reference:
        let
          m = builtins.match "ghcr[.]io/trevex/chalkos/([^:@]+):v(.+)" reference;
        in
        lib.assertMsg
          (
            m != null && self.releaseImages.${system} ? ${lib.elemAt m 0} && lib.elemAt m 1 == self.lib.version
          )
          "the ${name} template takes the image ${image} from ${reference}, which chalkos v${self.lib.version} does not publish"
        && lib.assertMsg (
          packages ? ${image}
        ) "the ${name} template's flake builds no ${image}, which its inventory takes from ${reference}";
    in
    assert lib.all (p: lib.elem p shell.nativeBuildInputs) tools;
    assert lib.isString shell.drvPath;
    # The installer, which a guide writes to a USB stick, evaluates.
    assert packages ? installer -> lib.isString packages.installer.drvPath;
    assert lib.all lib.id (lib.mapAttrsToList isPublished published);
    checkLib.validateDirectory {
      name = "template-${name}";
      src = ../../templates/${name};
      images = lib.mapAttrs' (image: lib.nameValuePair "images/${image}") (
        removeAttrs packages [ "installer" ]
      );
      command = ''
        ${lib.concatStrings (
          lib.mapAttrsToList (image: reference: ''
            sed -i 's|{source: {oci: ${reference}}}|{source: {directory: images/${image}}}|' chalkos.yaml
          '') published
        )}
        if grep -q '{source: {oci:' chalkos.yaml; then
          echo "error: chalkos.yaml takes an image from a registry on a line the check does not read" >&2
          exit 1
        fi
        ${command}
      '';
    };
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
          "chalkos-registry"
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
            echo "error: $name imports ./$dir, which nix/packages/$name.nix does not list" >&2
            failed=1
          fi
        done
        for path in $listed; do
          used=
          for dir in $imported; do
            case "$dir/" in "$path"/*) used=1 ;; esac
          done
          if [ -z "$used" ]; then
            echo "error: nix/packages/$name.nix lists ./$path, of which $name imports no package" >&2
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
          pkgs.chalkos.protoc-gen-connect-go
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

  # The lab template: its flake builds the nodes' kvm image, and its dev shell brings chalklab,
  # chalkctl and kubectl; validatePinned checks its Cluster document's images. TestTheLabFiles of
  # go-unit checks chalklab.yaml's MAC addresses.
  template-lab = templateCheck {
    name = "lab";
    tools = [
      pkgs.chalkos.chalklab
      chalkPkgs.chalkctl
      pkgs.kubectl
    ];
    command = validatePinned;
  };

  # The router lab template: its flake builds the router's and the client's kvm images, and its
  # dev shell brings chalklab and chalkctl.
  template-router-lab = templateCheck {
    name = "router-lab";
    tools = [
      pkgs.chalkos.chalklab
      chalkPkgs.chalkctl
    ];
  };

  # The metal cluster template: its flake builds the nodes' metal image and the installer, and its
  # dev shell brings chalkctl, chalkbuild and kubectl; validatePinned checks its Cluster
  # document's images.
  template-cluster-metal = templateCheck {
    name = "cluster-metal";
    tools = [
      chalkPkgs.chalkctl
      chalkPkgs.chalkbuild
      pkgs.kubectl
    ];
    command = validatePinned;
  };

  # The homelab example: its flake builds the router's metal image and the nodes', and chalkctl
  # validate accepts the router's and the nodes' YAML, BGPSpeaker and BGPPeer documents included,
  # with those images where their groups point, and validatePinned checks its Cluster document's
  # images. chalkctl validate accepts the air-gapped variant's inventory too, without the NAT64 and
  # with the cluster registry and the router's time. vm/vm.sh, the script that creates the lab as
  # libvirt VMs on a workstation, parses and passes shellcheck.
  example-homelab =
    let
      packages = homelab.packages.${pkgs.stdenv.hostPlatform.system};
    in
    # The installer, which the guide writes to a USB stick, evaluates.
    assert lib.isString packages.installer.drvPath;
    checkLib.validateDirectory {
      name = "example-homelab";
      src = ../../examples/homelab;
      images = {
        "images/router-metal" = packages.router-metal;
        "images/node-metal" = packages.node-metal;
      };
      command = ''
        ${validatePinned}
        chalkctl validate --inventory chalkos-air-gapped.yaml
        ${pkgs.bash}/bin/bash -n vm/vm.sh
        ${pkgs.shellcheck}/bin/shellcheck vm/vm.sh
      '';
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
    checkLib.validateDirectory {
      name = "example-extensions";
      src = ../../examples/extensions;
      images."images/node-metal" = example.packages.${pkgs.stdenv.hostPlatform.system}.node-metal;
    };

  # The BGP extension example: its image evaluates, which leaves building it to the image tests of
  # chalkos's own images, and the extension's tests pass.
  example-bgp-extension =
    let
      system = pkgs.stdenv.hostPlatform.system;
    in
    assert lib.isString bgpExtension.packages.${system}.node-bird-metal.drvPath;
    bgpExtension.checks.${system}.node-bird-metal;

  # The BGP guide shows the refusals that the extension's tests bgp-unconfigured-peer-interface, of
  # its validator bgp-peers, and bgp-new-asn, of a change rule, expect, as refusals.json holds
  # them. Each sits on one line of its own that starts with [{"kind":"Peer","name":"router","path":
  # and the test's field, spec.interfaces[2] or spec.asn, which is how the check finds it.
  example-bgp-extension-validator-docs =
    let
      inherit (bgpExtension.nixosConfigurations.node-bird-metal.config.chalkos) tests;
    in
    pkgs.runCommand "chalkos-example-bgp-extension-validator-docs"
      {
        nativeBuildInputs = [ pkgs.jq ];
        page = ../../docs/guides/bgp-extension.md;
        validatorRefusals = builtins.toJSON tests.bgp-unconfigured-peer-interface.refusals;
        changeRefusals = builtins.toJSON tests.bgp-new-asn.refusals;
      }
      ''
        shows() {
          local test=$1 field=$2 expected documented
          expected=$(jq -cS . <<<"$3")
          documented=$(grep "^\[{\"kind\":\"Peer\",\"name\":\"router\",\"path\":\"$field\"" "$page" | jq -cS . || true)
          if [ -z "$documented" ]; then
            echo "error: $page shows no refusals of the test $test" >&2
            exit 1
          fi
          if [ "$documented" != "$expected" ]; then
            echo "error: $page shows $documented, not the refusals $expected of the test $test" >&2
            exit 1
          fi
        }
        shows bgp-unconfigured-peer-interface 'spec\.interfaces\[2\]' "$validatorRefusals"
        shows bgp-new-asn 'spec\.asn' "$changeRefusals"
        touch $out
      '';

  # The GitHub workflows pass actionlint, with shellcheck for their scripts, which reads the inputs
  # of the repository's own actions. So do the workflow and the script of the guide that releases
  # a cluster's own images, which the page includes. The scripts of .github/scripts pass
  # shellcheck, and their test passes.
  workflows =
    pkgs.runCommand "chalkos-workflows"
      {
        nativeBuildInputs = [
          pkgs.actionlint
          pkgs.gitMinimal
          pkgs.jq
          pkgs.shellcheck
        ];
        src = lib.fileset.toSource {
          root = ../../.;
          fileset = lib.fileset.unions [
            ../../.github/workflows
            ../../.github/actions
            ../../.github/scripts
            ../../docs/guides/release-your-images/release-images.yml
            ../../docs/guides/release-your-images/release-images.sh
          ];
        };
      }
      ''
        cd $src
        actionlint -shellcheck shellcheck .github/workflows/*.yml docs/guides/release-your-images/release-images.yml
        shellcheck docs/guides/release-your-images/release-images.sh .github/scripts/*.sh
        bash .github/scripts/test.sh
        touch $out
      '';

  # Renovate's configuration and that of the guide that releases a cluster's own images pass its
  # validator, and releases.nix lists the newest Kubernetes minor first, which the configuration
  # takes for the newest.
  renovate =
    pkgs.runCommand "chalkos-renovate"
      {
        nativeBuildInputs = [ pkgs.renovate ];
        src = lib.fileset.toSource {
          root = ../../.;
          fileset = lib.fileset.unions [
            ../../.github/renovate.json5
            ../../docs/guides/release-your-images/renovate.json5
            ../../modules/node/kubernetes/releases.nix
          ];
        };
        newest = lib.last (lib.sort lib.versionOlder (lib.attrNames self.lib.kubernetesReleases));
      }
      ''
        cd $src
        HOME=$(mktemp -d) renovate-config-validator --strict
        HOME=$(mktemp -d) renovate-config-validator --strict --no-global docs/guides/release-your-images/renovate.json5
        first=$(grep -m 1 -oP '^  "\K[0-9]+\.[0-9]+(?=" = \{)' modules/node/kubernetes/releases.nix)
        if [ "$first" != "$newest" ]; then
          echo "error: modules/node/kubernetes/releases.nix lists $first first, not the newest minor $newest" >&2
          exit 1
        fi
        touch $out
      '';

  # security-insights.yml is valid against the CUE schema of the OpenSSF Security Insights version
  # it names, which a new version of the specification brings here with its hash.
  security-insights =
    let
      version = "2.2.0";
      schema = pkgs.fetchurl {
        url = "https://raw.githubusercontent.com/ossf/security-insights-spec/v${version}/spec/schema.cue";
        sha256 = "1caqrla6q94g31wj0347i9y2lwg0zdzsga7jd64zr12l1d3n2jsf";
      };
    in
    pkgs.runCommand "chalkos-security-insights"
      {
        nativeBuildInputs = [
          pkgs.cue
          pkgs.yq-go
        ];
        insights = ../../security-insights.yml;
      }
      ''
        named=$(yq '.header.schema-version' "$insights")
        if [ "$named" != ${version} ]; then
          echo "error: security-insights.yml names the schema version $named, and the check has ${version}" >&2
          exit 1
        fi
        cp ${schema} schema.cue
        cp "$insights" security-insights.yml
        cue vet -c -d '#SecurityInsights' schema.cue security-insights.yml
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

  # No path of the repository holds a character golang.org/x/mod/zip refuses when it builds this
  # module's zip for `go get`. golang.org/x/mod/module.CheckFilePath allows a letter, a digit, a
  # space, the path separator / and !#$%&()+,-.=@[]^_{}~, plus any other Unicode letter; this
  # check allows the same ASCII set and refuses every other ASCII character and, since a Nix
  # regex cannot ask whether a rune is a Unicode letter, every non-ASCII byte too. Two testdata
  # fixtures of cmd/chalkos-storage need a systemd-escaped name, which holds a backslash; they
  # keep that name out of their own path, in cmd/chalkos-storage/testdata/generate/escaped, which
  # cmd/chalkos-storage/generate_test.go maps back to the name runGenerate writes.
  module-paths =
    let
      bad = lib.filter (p: builtins.match ".*[^][!#$%&()+,.=@^_{}~ /A-Za-z0-9-].*" p != null) repoFiles;
    in
    pkgs.runCommand "chalkos-module-paths" { bad = lib.concatLines bad; } ''
      if [ -n "$bad" ]; then
        echo "error: these paths hold a character golang.org/x/mod/zip refuses when it builds this module's zip for \`go get\`:" >&2
        printf '%s' "$bad" >&2
        exit 1
      fi
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
          echo "error: the fixtures name $ref, which nix/testing/kubernetes-images.json does not serve" >&2
          exit 1
        }
      done
    '';
  };
}
