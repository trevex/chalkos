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
      # With the VM checks running alongside, a package's tests can take longer than go test's
      # default of 10 minutes.
      buildPhase = ''
        runHook preBuild
        ${runTests "go test -v -timeout 30m ./pkg/... ./cmd/..."}
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
  e2e-kubernetes-ha = e2e "kubernetes-ha" "^TestKubernetesHA$" {
    CHALKLAB_K8S_HA_IMAGE_DIR = "${chalkPkgs.test-kubernetes-ha-image}";
    CHALKLAB_K8S_IMAGES = "${chalkPkgs.test-kubernetes-images}/images.json";
  };

  # The VXLAN rule's script with the firewall of the test cluster's worker, in a network namespace:
  # a peer sends UDP to port 8472 over a veth pair, and the node receives what the firewall
  # accepts.
  vxlan-rule =
    let
      worker =
        (import ./testing/cluster.nix { inherit self pkgs; }).cluster.roles.k8s-worker.nixos.config;
      # The script NixOS runs to load and reload the firewall.
      firewall = builtins.elemAt worker.systemd.services.nftables.serviceConfig.ExecReload 1;
      rule = lib.getExe (
        pkgs.callPackage ../modules/node/vxlan-rule.nix {
          lockFile = "vxlan.lock";
          mark = "0x01000000";
        }
      );
    in
    pkgs.runCommand "chalkos-vxlan-rule"
      {
        nativeBuildInputs = [
          pkgs.nftables
          pkgs.iproute2
          pkgs.util-linux
          pkgs.socat
        ];
      }
      ''
        cat >test.sh <<'TEST'
        set -euo pipefail
        fail() {
          echo "error: $*" >&2
          nft list ruleset >&2
          exit 1
        }
        # This namespace is the node; the peer gets one of its own.
        ip link set lo up
        unshare -n sh -c 'touch peer-ready; exec sleep 600' &
        peer=$!
        until [ -e peer-ready ]; do sleep 0.1; done
        on_peer() { nsenter -t "$peer" -n "$@"; }
        ip link add v0 type veth peer name v1 netns "$peer"
        ip addr add 10.0.0.11/24 dev v0
        ip addr add fd00::11/64 dev v0 nodad
        ip link set v0 up
        # Addresses of the node on another interface.
        ip link add d0 type dummy
        ip addr add 10.0.1.11/24 dev d0
        ip addr add fd00:1::11/64 dev d0 nodad
        ip link set d0 up
        on_peer ip link set lo up
        on_peer ip addr add 10.0.0.12/24 dev v1
        on_peer ip addr add fd00::12/64 dev v1 nodad
        on_peer ip link set v1 up
        on_peer ip route add 10.0.1.0/24 via 10.0.0.11
        on_peer ip route add fd00:1::/64 via fd00::11

        # The firewall as NixOS loads it, with the state file it keeps in /var/lib/nftables here.
        sed "s|/var/lib/nftables/deletions.nft|$PWD/deletions.nft|" ${firewall} >firewall.nft
        touch deletions.nft
        nft -f firewall.nft
        # Counts the packets the firewall accepted, and those still marked then.
        nft -f - <<'NFT'
        table inet count {
          chain input {
            type filter hook input priority filter + 10; policy accept;
            udp dport 8472 counter name accepted
            udp dport 8472 meta mark & 0x01000000 != 0 counter name marked
          }
          counter accepted {}
          counter marked {}
        }
        NFT

        socat -u UDP4-RECV:8472 OPEN:received,creat,append &
        socat -u UDP6-RECV:8472,ipv6only=1 OPEN:received,creat,append &
        sleep 0.5
        n=0
        # Whether a datagram the peer sends to the address arrives.
        arrives() {
          n=$((n + 1))
          local to=$1
          case $1 in *:*) to="[$1]" ;; esac
          echo "datagram $n" | on_peer socat -u - "UDP-SENDTO:$to:8472"
          for _ in $(seq 20); do
            grep -qx "datagram $n" received && return 0
            sleep 0.1
          done
          return 1
        }
        # Runs the script with the arguments, setting status; the rules it prints go to rules.log.
        run() {
          status=0
          ${rule} "$@" >rules.log 2>errors.log || status=$?
        }
        # Runs the script with the file holding the content, with escapes. Without source lines
        # VXLAN may come from any source.
        holding() {
          printf '%b' "$1" >vxlan
          run vxlan
        }
        v4='destination 10.0.0.11 interface\n'
        v6='destination fd00::11 interface\n'
        rules() {
          grep -c 'udp dport 8472' rules.log || true
        }

        if arrives 10.0.0.11; then fail "VXLAN arrived without the rule"; fi

        holding "$v4$v6"
        [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "an address of each family"
        arrives 10.0.0.11 || fail "IPv4 VXLAN to the node's address was refused"
        arrives fd00::11 || fail "IPv6 VXLAN to the node's address was refused"
        # The node's other addresses, and the picked ones arriving on another interface.
        if arrives 10.0.1.11; then fail "VXLAN arrived at another address"; fi
        if arrives fd00:1::11; then fail "IPv6 VXLAN arrived at another address"; fi
        [ "$(nft list counter inet count marked | grep -o 'packets [0-9]*')" = "packets 0" ] || fail "accepted packets keep the mark"

        # A reload of the firewall replaces its own table alone.
        nft add table ip kube-proxy
        nft add table ip6 flannel-ipv6
        nft -f firewall.nft
        nft list tables | grep -qx 'table ip kube-proxy' || fail "the firewall's reload removed kube-proxy's table"
        nft list tables | grep -qx 'table ip6 flannel-ipv6' || fail "the firewall's reload removed flannel's table"
        arrives 10.0.0.11 || fail "VXLAN was refused after the firewall's reload"
        arrives fd00::11 || fail "IPv6 VXLAN was refused after the firewall's reload"

        holding "$v6"
        [ "$status" = 0 ] && [ "$(rules)" = 1 ] || fail "the IPv6 address alone"
        if arrives 10.0.0.11; then fail "VXLAN arrived at an address no longer picked"; fi
        arrives fd00::11 || fail "IPv6 VXLAN to the node's address was refused"

        # An address on a dummy interface, as a routing daemon announces it, takes VXLAN that
        # arrives on another interface.
        holding 'destination 10.0.1.11 any\ndestination fd00:1::11 any\n'
        [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "addresses on a dummy interface"
        arrives 10.0.1.11 || fail "VXLAN to the address on the dummy interface was refused"
        arrives fd00:1::11 || fail "IPv6 VXLAN to the address on the dummy interface was refused"
        if arrives 10.0.0.11; then fail "VXLAN arrived at an address no longer picked"; fi

        # With source ranges VXLAN comes from them alone; a family without one takes none.
        holding "$v4$v6"'source 10.0.0.0/24\nsource fd00::/64\n'
        [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "sources of each family"
        arrives 10.0.0.11 || fail "VXLAN from a source in the ranges was refused"
        arrives fd00::11 || fail "IPv6 VXLAN from a source in the ranges was refused"
        holding "$v4$v6"'source 10.0.9.0/24\nsource fd00:9::/64\nsource 192.168.0.0/16\n'
        [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "sources elsewhere"
        if arrives 10.0.0.11; then fail "VXLAN arrived from a source outside the ranges"; fi
        if arrives fd00::11; then fail "IPv6 VXLAN arrived from a source outside the ranges"; fi
        holding "$v4$v6"'source 10.0.0.0/24\n'
        [ "$status" = 0 ] && [ "$(rules)" = 1 ] || fail "sources of one family"
        arrives 10.0.0.11 || fail "VXLAN from a source in the ranges was refused"
        if arrives fd00::11; then fail "IPv6 VXLAN arrived without an IPv6 source range"; fi

        # Anything but a destination line per family, of a bare address and a mode, and source
        # lines of a range each, adds no rule, empties the table and fails.
        for content in "" '\n' '10.0.0.11\n' 'destination 10.0.0.11\n' 'destination 10.0.0.11 eth0\n' \
          'destination 10.0.0.0/8 interface\n' "$v4"'destination 10.0.0.12 any\n' "$v6"'destination fd00::12 interface\n' \
          "$v4"'destination fd00::11 interface accept\n' 'destination 10.0.0.11 interface accept\n' \
          'destination fd00::11 }\n' 'destination  10.0.0.11 interface\n' 'destination 10.0.0.11 interface \n' \
          'Destination 10.0.0.11 interface\n' 'source 10.0.0.0/24\n' "$v4"'\n' 'destination ::ffff: interface\n' \
          'destination eth0 interface\n' 'destination cafe.be interface\n' 'destination cafe any\n' \
          'destination 10.0.0 interface\n' 'destination 10.0.0.11.12 interface\n' 'destination 10.0.0.256 interface\n' \
          'destination 10.0.0.0x1 interface\n' 'destination 10.0..11 interface\n' 'destination ::: interface\n' \
          "$v4"'source 10.0.0.0\n' "$v4"'source 10.0.0.0/33\n' "$v4"'source fd00::/129\n' "$v4"'source 10.0.0.0/024\n' \
          "$v4"'source 10.0.0.0/\n' "$v4"'source cafe/8\n' "$v4"'source 10.0.0.0/24 accept\n' "$v4"'source 10.0.0.0/24 }\n' \
          "$v4"'source  10.0.0.0/24\n' "$v4"'source 10.0.0.0/8/8\n' "$v4"'source 10.0.0.256/24\n'; do
          holding "$v4"
          holding "$content"
          [ "$status" != 0 ] && [ "$(rules)" = 0 ] || fail "accepted a file holding '$content'"
          if arrives 10.0.0.11; then fail "VXLAN arrived after the file held '$content'"; fi
        done

        # Without the file or an argument the table is emptied.
        holding "$v4"
        rm vxlan
        run vxlan
        [ "$status" = 0 ] && [ "$(rules)" = 0 ] || fail "without the file"
        holding "$v4"
        run
        [ "$status" = 0 ] && [ "$(rules)" = 0 ] || fail "without an argument"
        if arrives 10.0.0.11; then fail "VXLAN arrived without the file"; fi
        TEST
        unshare -rn bash test.sh
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
