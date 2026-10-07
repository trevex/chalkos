{ pkgs, self }:
let
  inherit (pkgs) lib;
  chalkPkgs = self.packages.${pkgs.stdenv.hostPlatform.system};
  testEnv = import ./testing/env.nix { inherit pkgs self; };

  # Tests skip when a tool or variable is missing, so a check must not pass on skipped or zero tests.
  runTests = command: ''
    set -o pipefail
    ${command} 2>&1 | tee "$TMPDIR/test.log"
    if grep -q -- '--- SKIP' "$TMPDIR/test.log"; then
      echo "error: a test was skipped" >&2
      exit 1
    fi
    if ! grep -q -- '--- PASS' "$TMPDIR/test.log"; then
      echo "error: no test ran" >&2
      exit 1
    fi
  '';

  e2e =
    name: pattern: images:
    pkgs.runCommand "chalkos-e2e-${name}"
      (
        {
          requiredSystemFeatures = [ "kvm" ];
          nativeBuildInputs = testEnv.tools ++ [ chalkPkgs.chalklab-e2e ];
          # Tests install nodes with the real chalkctl, from the test cluster's manifests.
          CHALKLAB_CHALKCTL = lib.getExe chalkPkgs.chalkctl;
          CHALKLAB_SECRETS = "${chalkPkgs.test-secrets}/secrets.json";
          CHALKLAB_MANIFESTS = "${chalkPkgs.test-manifests}";
        }
        // images
        // testEnv.vars
      )
      ''
        export HOME=$TMPDIR
        ${runTests "chalklab-e2e -test.v -test.run '${pattern}' -test.timeout 60m"}
        touch $out
      '';
  testImage.CHALKLAB_IMAGE_DIR = "${chalkPkgs.test-image}";
in
{
  go-unit = chalkPkgs.chalkctl.overrideAttrs (
    old:
    {
      pname = "chalkos-go-unit";
      nativeBuildInputs = old.nativeBuildInputs ++ testEnv.tools;
      buildPhase = ''
        runHook preBuild
        ${runTests "go test -v ./pkg/... ./cmd/..."}
        runHook postBuild
      '';
      doCheck = false;
      installPhase = "touch $out";
      postFixup = "";
    }
    // testEnv.vars
  );

  e2e-firmware = e2e "firmware" "^TestFirmwareBoots$" { };
  e2e-image = e2e "image" "^TestImageBootsWithoutSecureBoot$" testImage;
  e2e-secureboot = e2e "secureboot" "^TestSecureBoot" testImage;
  e2e-verity = e2e "verity" "^TestVerityRejectsTamperedStore$" testImage;
  e2e-install = e2e "install" "^TestInstallInPlace$" testImage;
  e2e-installer = e2e "installer" "^TestInstallerInstallsOntoBlankDisk$" (
    testImage // { CHALKLAB_INSTALLER_DIR = "${chalkPkgs.test-installer}"; }
  );
  e2e-iso = e2e "iso" "^TestInstallerISOBoots$" {
    CHALKLAB_GENERIC_INSTALLER_DIR = "${chalkPkgs.installer}";
  };
  e2e-storage = e2e "storage" "^TestStorage" {
    CHALKLAB_STORAGE_IMAGE_DIR = "${chalkPkgs.test-storage-image}";
  };
  e2e-kubernetes = e2e "kubernetes" "^TestKubernetesCluster$" {
    CHALKLAB_K8S_CONTROLPLANE_IMAGE_DIR = "${chalkPkgs.test-kubernetes-controlplane-image}";
    CHALKLAB_K8S_WORKER_IMAGE_DIR = "${chalkPkgs.test-kubernetes-worker-image}";
    CHALKLAB_K8S_IMAGES = "${chalkPkgs.test-kubernetes-images}/images.json";
  };

  # The VXLAN rule's script against an iptables that records what it is asked to do.
  vxlan-rule =
    let
      iptables = pkgs.runCommand "fake-iptables" { } ''
        mkdir -p $out/bin
        cat >$out/bin/iptables <<'EOF'
        #!${pkgs.runtimeShell}
        echo "''${0##*/} $*" >>"$IPTABLES_LOG"
        EOF
        chmod +x $out/bin/iptables
        ln -s iptables $out/bin/ip6tables
      '';
      rule = lib.getExe (
        pkgs.callPackage ../modules/node/vxlan-rule.nix {
          inherit iptables;
          lockFile = "vxlan.lock";
        }
      );
    in
    pkgs.runCommand "chalkos-vxlan-rule" { } ''
      export IPTABLES_LOG=$PWD/log
      fail() {
        echo "error: $*" >&2
        cat "$IPTABLES_LOG" >&2
        exit 1
      }
      # Runs the script with the arguments, setting status.
      run() {
        : >"$IPTABLES_LOG"
        status=0
        ${rule} "$@" || status=$?
      }
      # Runs the script with node-ip holding the content, with escapes.
      holding() {
        printf '%b' "$1" >node-ip
        run node-ip
      }
      flushed() {
        grep -qx 'iptables -w -F chalkos-vxlan' "$IPTABLES_LOG" &&
          grep -qx 'ip6tables -w -F chalkos-vxlan' "$IPTABLES_LOG"
      }
      added() {
        grep -- '-A chalkos-vxlan' "$IPTABLES_LOG" || true
      }
      accepts() {
        echo "$1 -w -A chalkos-vxlan -p udp --dport 8472 -d $2 -m addrtype --dst-type LOCAL --limit-iface-in -j ACCEPT"
      }

      holding '10.0.0.11\n'
      { [ "$status" = 0 ] && flushed && [ "$(added)" = "$(accepts iptables 10.0.0.11)" ]; } || fail "IPv4 address"
      holding 'fd00::11'
      { [ "$status" = 0 ] && flushed && [ "$(added)" = "$(accepts ip6tables fd00::11)" ]; } || fail "IPv6 address"

      # Anything but a bare address adds no rule, empties the chain and fails.
      for content in "" '\n' '10.0.0.0/8\n' '10.0.0.11\n10.0.0.12\n' '10.0.0.11 -j DROP\n' '-s 0.0.0.0/0\n' 'eth0\n'; do
        holding "$content"
        { [ "$status" != 0 ] && flushed && [ -z "$(added)" ]; } || fail "accepted node-ip holding '$content'"
      done

      # Without the file or an argument the chain is emptied.
      rm node-ip
      run node-ip
      { [ "$status" = 0 ] && flushed && [ -z "$(added)" ]; } || fail "without node-ip"
      run
      { [ "$status" = 0 ] && flushed && [ -z "$(added)" ]; } || fail "without an argument"
      touch $out
    '';

  # The generated API code is committed; it must match what buf generates from the proto files.
  api-generated =
    pkgs.runCommand "chalkos-api-generated"
      {
        nativeBuildInputs = [
          pkgs.buf
          pkgs.protoc-gen-go
          pkgs.protoc-gen-connect-go
        ];
        src = pkgs.lib.fileset.toSource {
          root = ../.;
          fileset = pkgs.lib.fileset.unions [
            ../buf.yaml
            ../buf.gen.yaml
            ../api
            ../pkg/api
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

  manifest-golden =
    let
      homelab = self.lib.mkCluster { modules = [ ../examples/homelab/cluster.nix ]; };
      generated = pkgs.writeText "homelab-manifest.json" (builtins.toJSON homelab.manifest);
    in
    pkgs.runCommand "chalkos-manifest-golden" { nativeBuildInputs = [ pkgs.jq ]; } ''
      diff -u <(jq -S . ${../test/fixtures/homelab-manifest.json}) <(jq -S . ${generated})
      touch $out
    '';

  eval =
    let
      failures = import ../test/nix/eval.nix {
        inherit pkgs;
        inherit (pkgs) lib;
        inherit (self.lib) mkCluster;
        flakeModule = self.flakeModules.default;
      };
    in
    if failures == [ ] then
      pkgs.runCommand "chalkos-eval-tests" { } "touch $out"
    else
      throw "evaluation tests failed:\n${pkgs.lib.generators.toPretty { } failures}";
}
