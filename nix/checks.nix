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
  e2e-upgrade = e2e "upgrade" "^TestUpgrade$" (
    testImage
    // {
      CHALKLAB_UPGRADE_IMAGE_DIR = "${chalkPkgs.test-upgrade-image}";
      CHALKLAB_UNHEALTHY_IMAGE_DIR = "${chalkPkgs.test-unhealthy-image}";
    }
  );
  e2e-kubernetes = e2e "kubernetes" "^TestKubernetesCluster$" {
    CHALKLAB_K8S_CONTROLPLANE_IMAGE_DIR = "${chalkPkgs.test-kubernetes-controlplane-image}";
    CHALKLAB_K8S_WORKER_IMAGE_DIR = "${chalkPkgs.test-kubernetes-worker-image}";
    CHALKLAB_K8S_IMAGES = "${chalkPkgs.test-kubernetes-images}/images.json";
  };
  e2e-kubernetes-ha = e2e "kubernetes-ha" "^TestKubernetesHA$" {
    CHALKLAB_K8S_HA_IMAGE_DIR = "${chalkPkgs.test-kubernetes-ha-image}";
    CHALKLAB_K8S_HA_UPGRADE_IMAGE_DIR = "${chalkPkgs.test-kubernetes-ha-upgrade-image}";
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
      script =
        sourceSubnets:
        lib.getExe (
          pkgs.callPackage ../modules/node/vxlan-rule.nix {
            lockFile = "vxlan.lock";
            mark = "0x01000000";
            podCIDRs = [
              "10.244.0.0/16"
              "fd00:10:244::/56"
            ];
            inherit sourceSubnets;
          }
        );
      rule = script [
        "10.0.0.0/24"
        "fd00::/64"
      ];
      # The script of a cluster without source ranges.
      anySource = script [ ];
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
        # An interface of the pod network, over which the peer reaches the node too.
        ip link add cni0 type veth peer name c1 netns "$peer"
        ip addr add 10.0.2.11/24 dev cni0
        ip addr add fd00:2::11/64 dev cni0 nodad
        ip link set cni0 up
        on_peer ip link set lo up
        on_peer ip addr add 10.0.0.12/24 dev v1
        on_peer ip addr add fd00::12/64 dev v1 nodad
        on_peer ip link set v1 up
        on_peer ip route add 10.0.1.0/24 via 10.0.0.11
        on_peer ip route add fd00:1::/64 via fd00::11
        on_peer ip addr add 10.0.2.12/24 dev c1
        on_peer ip addr add fd00:2::12/64 dev c1 nodad
        on_peer ip link set c1 up
        # The routes of datagrams the peer sends out of c1.
        on_peer ip route add 10.0.1.0/24 via 10.0.2.11 dev c1 metric 100
        on_peer ip route add fd00:1::/64 via fd00:2::11 dev c1 metric 2048
        # A pod on the node, whose datagrams the node forwards to the peer.
        unshare -n sh -c 'touch pod-ready; exec sleep 600' &
        pod=$!
        until [ -e pod-ready ]; do sleep 0.1; done
        on_pod() { nsenter -t "$pod" -n "$@"; }
        ip link add p0 type veth peer name p1 netns "$pod"
        ip addr add 10.244.0.1/24 dev p0
        ip addr add fd00:10:244::1/64 dev p0 nodad
        ip link set p0 up
        on_pod ip link set lo up
        on_pod ip addr add 10.244.0.2/24 dev p1
        on_pod ip addr add fd00:10:244::2/64 dev p1 nodad
        on_pod ip link set p1 up
        on_pod ip route add default via 10.244.0.1
        on_pod ip route add default via fd00:10:244::1
        echo 1 >/proc/sys/net/ipv4/ip_forward
        echo 1 >/proc/sys/net/ipv6/conf/all/forwarding
        # The peer has no route back to the pod; it takes the pod's datagrams nonetheless.
        for conf in all v1 c1; do
          on_peer sh -c "echo 0 >/proc/sys/net/ipv4/conf/$conf/rp_filter"
        done

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
        for port in 8472 8473; do
          on_peer socat -u UDP4-RECV:$port OPEN:peer-received,creat,append &
          on_peer socat -u UDP6-RECV:$port,ipv6only=1 OPEN:peer-received,creat,append &
        done
        sleep 0.5
        n=0
        # Whether a datagram the peer sends to the address arrives, with further socat options
        # for the sender, such as the interface it goes out of.
        arrives() {
          n=$((n + 1))
          local to=$1
          case $1 in *:*) to="[$1]" ;; esac
          echo "datagram $n" | on_peer socat -u - "UDP-SENDTO:$to:8472''${2:-}"
          for _ in $(seq 20); do
            grep -qx "datagram $n" received && return 0
            sleep 0.1
          done
          return 1
        }
        # Whether a datagram the pod sends to the address and port reaches the peer through the
        # node.
        forwarded() {
          n=$((n + 1))
          local to=$1
          case $1 in *:*) to="[$1]" ;; esac
          echo "datagram $n" | on_pod socat -u - "UDP-SENDTO:$to:$2"
          for _ in $(seq 20); do
            grep -qx "datagram $n" peer-received && return 0
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
          grep -c 'meta mark set meta mark |' rules.log || true
        }
        # The rules in the table now, which may differ from what the script printed.
        marking() {
          nft list table inet chalkos-vxlan 2>/dev/null | grep -c 'meta mark set meta mark |' || true
        }
        marked() {
          nft list counter inet count marked | grep -o 'packets [0-9]*'
        }

        if arrives 10.0.0.11; then fail "VXLAN arrived without the rule"; fi

        holding "$v4$v6"
        [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "an address of each family"
        arrives 10.0.0.11 || fail "IPv4 VXLAN to the node's address was refused"
        arrives fd00::11 || fail "IPv6 VXLAN to the node's address was refused"
        # The node's other addresses, and the picked ones arriving on another interface.
        if arrives 10.0.1.11; then fail "VXLAN arrived at another address"; fi
        if arrives fd00:1::11; then fail "IPv6 VXLAN arrived at another address"; fi
        [ "$(marked)" = "packets 0" ] || fail "accepted packets keep the mark"

        # A mark another table set earlier opens nothing.
        nft -f - <<'NFT'
        table inet premark {
          chain input {
            type filter hook input priority filter - 10; policy accept;
            udp dport 8472 meta mark set meta mark | 0x01000000
          }
        }
        NFT
        if arrives 10.0.1.11; then fail "VXLAN marked by another table arrived"; fi
        if arrives fd00:1::11; then fail "IPv6 VXLAN marked by another table arrived"; fi
        arrives 10.0.0.11 || fail "VXLAN marked by another table too was refused"
        nft delete table inet premark

        # Pods cannot send VXLAN through the node to the source ranges, where the node's
        # masquerade would give it a source the other nodes take VXLAN from.
        forwarded 10.0.0.12 8473 || fail "a pod's datagram to another port was dropped"
        forwarded fd00::12 8473 || fail "a pod's IPv6 datagram to another port was dropped"
        if forwarded 10.0.0.12 8472; then fail "a pod sent VXLAN to a source range"; fi
        if forwarded fd00::12 8472; then fail "a pod sent IPv6 VXLAN to a source range"; fi
        forwarded 10.0.2.12 8472 || fail "a pod's VXLAN outside the source ranges was dropped"
        forwarded fd00:2::12 8472 || fail "a pod's IPv6 VXLAN outside the source ranges was dropped"
        # Without source ranges VXLAN may come from anywhere, so nothing is dropped.
        ${anySource} vxlan >/dev/null
        forwarded 10.0.0.12 8472 || fail "a pod's VXLAN was dropped without source ranges"
        forwarded fd00::12 8472 || fail "a pod's IPv6 VXLAN was dropped without source ranges"
        holding "$v4$v6"

        # A reload of the firewall replaces its own table alone.
        nft add table ip kube-proxy
        nft add table ip6 flannel-ipv6
        nft -f firewall.nft
        nft list tables | grep -qx 'table ip kube-proxy' || fail "the firewall's reload removed kube-proxy's table"
        nft list tables | grep -qx 'table ip6 flannel-ipv6' || fail "the firewall's reload removed flannel's table"
        arrives 10.0.0.11 || fail "VXLAN was refused after the firewall's reload"
        arrives fd00::11 || fail "IPv6 VXLAN was refused after the firewall's reload"

        # Addresses on d0, taking VXLAN there alone, refuse it arriving on v0.
        holding 'destination 10.0.1.11 interface\ndestination fd00:1::11 interface\n'
        [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "addresses on another interface"
        if arrives 10.0.1.11; then fail "VXLAN arrived on an interface not holding the address"; fi
        if arrives fd00:1::11; then fail "IPv6 VXLAN arrived on an interface not holding the address"; fi

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
        # Not on an interface of the pod network, where pods could send it.
        if arrives 10.0.1.11 ,so-bindtodevice=c1; then fail "VXLAN arrived on the pod network"; fi
        if arrives fd00:1::11 ,so-bindtodevice=c1; then fail "IPv6 VXLAN arrived on the pod network"; fi
        # The mark goes once the firewall saw the packet, even one it accepts before looking at
        # the mark, such as those the node sends itself over lo.
        nft reset counters table inet count >/dev/null
        n=$((n + 1))
        echo "datagram $n" | socat -u - UDP-SENDTO:10.0.1.11:8472
        for _ in $(seq 20); do
          grep -qx "datagram $n" received && break
          sleep 0.1
        done
        grep -qx "datagram $n" received || fail "VXLAN the node sent itself was refused"
        [ "$(marked)" = "packets 0" ] || fail "packets the firewall accepted on lo keep the mark"

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
          "$v4"'source  10.0.0.0/24\n' "$v4"'source 10.0.0.0/8/8\n' "$v4"'source 10.0.0.256/24\n' \
          'destination a:b interface\n' 'destination cafe:babe interface\n' 'destination 1::2::3 interface\n' \
          'destination 12345:: interface\n' 'destination fe80::1%v0 interface\n' 'destination 1:2:3:4:5:6:7:8:9 interface\n' \
          'destination 1:2:3:4:5:6:7::8 interface\n' 'destination :1::2 interface\n' 'destination 1::2: interface\n' \
          'destination ::10.0.0 interface\n' 'destination fd00:10.0.0.11 interface\n' 'destination ::10.0.0.11:1 interface\n' \
          'destination 10.0.0.011 interface\n' "$v4"'source a:b/64\n' "$v4"'source 1::2::3/64\n'; do
          holding "$v4"
          holding "$content"
          [ "$status" != 0 ] && [ "$(rules)" = 0 ] || fail "accepted a file holding '$content'"
          if arrives 10.0.0.11; then fail "VXLAN arrived after the file held '$content'"; fi
        done

        # IPv6 addresses written out in full, and with an IPv4 address at the end.
        holding "$v4"'destination fd00:0:0:0:0:0:0:11 interface\n'
        [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "an IPv6 address in full"
        arrives fd00::11 || fail "IPv6 VXLAN to the address in full was refused"
        holding 'destination ::ffff:10.0.0.11 interface\n'
        [ "$status" = 0 ] && [ "$(rules)" = 1 ] || fail "an IPv6 address ending in an IPv4 one"

        # A file that cannot be read empties the table and fails.
        for kind in directory socket; do
          holding "$v4"
          rm vxlan
          case $kind in
            directory) mkdir vxlan ;;
            socket)
              socat UNIX-LISTEN:vxlan /dev/null &
              listener=$!
              until [ -S vxlan ]; do sleep 0.1; done
              ;;
          esac
          run vxlan
          [ "$status" != 0 ] && [ "$(marking)" = 0 ] || fail "accepted a $kind"
          if arrives 10.0.0.11; then fail "VXLAN arrived after the file was a $kind"; fi
          if [ "$kind" = socket ]; then kill "$listener"; wait "$listener" || true; fi
          rm -rf vxlan
        done

        # A lock that cannot be taken empties the table and fails.
        holding "$v4"
        rm vxlan.lock
        mkdir vxlan.lock
        run vxlan
        [ "$status" != 0 ] && [ "$(marking)" = 0 ] || fail "kept its rules without the lock"
        if arrives 10.0.0.11; then fail "VXLAN arrived after the lock could not be taken"; fi
        rmdir vxlan.lock

        # Without the file or an argument the table is emptied.
        holding "$v4"
        rm vxlan
        run vxlan
        [ "$status" = 0 ] && [ "$(rules)" = 0 ] || fail "without the file"
        holding "$v4"
        run
        [ "$status" = 0 ] && [ "$(rules)" = 0 ] || fail "without an argument"
        if arrives 10.0.0.11; then fail "VXLAN arrived without the file"; fi
        if forwarded 10.0.0.12 8472; then fail "a pod sent VXLAN to a source range without the file"; fi
        TEST
        unshare -rn bash test.sh
        touch $out
      '';

  # The worker's kernel module tree, and one with the gpu group: a module outside a role's groups
  # is not in its tree, and neither a name the kernel does not know, a directory without modules,
  # a module the image loads but its tree lacks, nor one whose dependency the tree lacks gets past
  # the build. Out-of-tree modules bring the in-tree modules they depend on.
  kernel-modules =
    let
      worker = (import ./testing/cluster.nix { inherit self pkgs; }).cluster.roles.k8s-worker.nixos;
      treeOf = nixos: nixos.config.system.build.chalkosKernelModules;
      tree = treeOf worker;
      gpu = treeOf (worker.extendModules { modules = [ { chalkos.kernel.moduleGroups = [ "gpu" ]; } ]; });
      # v4l2loopback depends on videodev, which no base group holds.
      v4l2loopback = worker.extendModules {
        modules = [
          (
            { config, ... }:
            {
              boot.extraModulePackages = [ config.boot.kernelPackages.v4l2loopback ];
              boot.kernelModules = [ "v4l2loopback" ];
            }
          )
        ];
      };
      v4l2loopbackCheck = lib.findFirst (
        d: d.name == "kernel-modules-check"
      ) null v4l2loopback.config.system.checks;
      tool = lib.getExe (pkgs.callPackage ./kernel-modules.nix { });
      full = lib.getOutput "modules" worker.config.boot.kernelPackages.kernel;
      version = worker.config.boot.kernelPackages.kernel.modDirVersion;
      # A package of one out-of-tree module whose modinfo names the dependencies given.
      stub =
        name: modinfo:
        pkgs.runCommandCC "chalkos-${name}" { } ''
          echo >empty.c
          $CC -c empty.c -o empty.o
          printf 'name=${name}\0${modinfo}\0vermagic=${version} SMP preempt mod_unload \0' >modinfo
          mkdir -p $out/lib/modules/${version}/extra
          $OBJCOPY --add-section .modinfo=modinfo --set-section-flags .modinfo=alloc,readonly \
            empty.o $out/lib/modules/${version}/extra/${name}.ko
        '';
      # uvcvideo, a soft dependency, is in no base group either.
      stubWithDependencies = stub "chalkos_stub" "depends=videodev\\0softdep=pre: uvcvideo";
      stubWithMissingDependency = stub "chalkos_stub_missing" "depends=chalkos_no_such_module";
    in
    pkgs.runCommand "chalkos-kernel-modules" { nativeBuildInputs = [ pkgs.kmod ]; } ''
      fail() {
        echo "error: $*" >&2
        exit 1
      }
      has() {
        modprobe --config no-config -d "$1" -S ${version} --show-depends "$2" >/dev/null 2>&1
      }
      for module in virtio_net i2c_i801 i2c_dev lpc_ich vmw_balloon ptp_vmw ceph cifs rpcsec_gss_krb5; do
        has ${tree} $module || fail "the worker's tree lacks $module"
      done
      if has ${tree} amdgpu; then fail "the worker's tree holds amdgpu"; fi
      has ${gpu} amdgpu || fail "the gpu group's tree lacks amdgpu"

      echo drivers/nvme >directories
      echo chalkos_no_such_module >names
      if ${tool} filter ${full} unknown directories names 2>errors; then fail "an unknown module was taken"; fi
      grep -q 'unknown kernel module chalkos_no_such_module' errors || fail "the error names no module: $(cat errors)"
      echo drivers/chalkos_no_such_directory >directories
      : >names
      if ${tool} filter ${full} empty directories names 2>errors; then fail "a directory without modules was taken"; fi
      grep -q 'no kernel module below drivers/chalkos_no_such_directory' errors || fail "the error names no directory: $(cat errors)"

      echo amdgpu >loaded
      if ${tool} check ${tree} loaded 2>errors; then fail "a module outside the tree passed the check"; fi
      grep -q 'does not hold: amdgpu' errors || fail "the error names no module: $(cat errors)"

      # The image's own check passed: ${v4l2loopbackCheck}
      has ${treeOf v4l2loopback} videodev || fail "the tree of an image with v4l2loopback lacks videodev"
      echo drivers/nvme >directories
      ${tool} filter ${full} "$PWD/stubbed" directories names ${stubWithDependencies}
      has stubbed videodev || fail "the dependency of an out-of-tree module is missing"
      has stubbed uvcvideo || fail "the soft dependency of an out-of-tree module is missing"
      if ${tool} filter ${full} missing directories names ${stubWithMissingDependency} 2>errors; then
        fail "an out-of-tree module whose dependency the kernel lacks was taken"
      fi
      grep -q 'chalkos_stub_missing depends on chalkos_no_such_module' errors || fail "the error names no modules: $(cat errors)"
      : >loaded
      if ${tool} check ${stubWithDependencies} loaded 2>errors; then fail "a module whose dependency the tree lacks passed the check"; fi
      grep -q 'chalkos_stub depends on videodev' errors || fail "the error names no modules: $(cat errors)"
      touch $out
    '';

  # The test cluster's role images within their ceilings (testing/image-sizes.nix). An image over
  # one fails the check with the largest paths of its system's closure, read from closureInfo, as
  # the build has no Nix daemon to ask. The images' own fit check is tried on the test image too.
  image-size =
    let
      testing = import ./testing/cluster.nix { inherit self pkgs; };
      ceilings = import ./testing/image-sizes.nix;
      measure =
        name:
        let
          inherit (testing.cluster.roles.${name}.nixos) config;
          image = config.system.build.image;
          ceiling = ceilings.${name};
        in
        lib.escapeShellArgs [
          name
          "${image}/${config.image.fileName}"
          "${image}/repart-output.json"
          "${config.system.build.uki}/${config.system.boot.loader.ukiFile}"
          (pkgs.closureInfo { rootPaths = [ config.system.build.toplevel ]; })
          ceiling.storeData
          ceiling.hashTree
          ceiling.uki
        ];
      test = testing.cluster.roles.test.nixos.config;
      testImage = "${test.system.build.image}/${test.image.fileName}";
      testPartitions = "${test.system.build.image}/repart-output.json";
      testUKI = "${test.system.build.uki}/${test.system.boot.loader.ukiFile}";
    in
    pkgs.runCommand "chalkos-image-size"
      {
        nativeBuildInputs = [ (pkgs.callPackage ./image-size.nix { }) ];
      }
      ''
        mib() { awk -v b="$1" 'BEGIN { printf "%.1f MiB", b / 1048576 }'; }
        failed=0
        check() {
          local name=$1 raw=$2 partitions=$3 uki=$4 closure=$5 sizes data hash over=()
          local -A ceiling=([storeData]=$6 [hashTree]=$7 [uki]=$8)
          sizes=$(chalkos-image-size measure "$raw" "$partitions")
          read -r data hash <<<"$sizes"
          local -A size=([storeData]=$data [hashTree]=$hash [uki]=$(stat -L -c %s "$uki"))
          for part in storeData hashTree uki; do
            # A ceiling may be a fraction of a MiB, which Nix writes as 2.200000.
            echo "$name: $part $(mib "''${size[$part]}") of at most $(awk -v c="''${ceiling[$part]}" 'BEGIN { printf "%g", c }') MiB"
            if awk -v size="''${size[$part]}" -v ceiling="''${ceiling[$part]}" \
              'BEGIN { exit !(size > ceiling * 1048576) }'; then
              over+=("$part")
            fi
          done
          if [[ ''${#over[@]} != 0 ]]; then
            echo "error: $name is over its ceiling in ''${over[*]}; the largest paths of its closure:" >&2
            # closureInfo's registration: a path, its hash, its size, its deriver, its number of
            # references and the references.
            awk 'step == 0 { path = $0; step = 1; next }
              step == 1 { step = 2; next }
              step == 2 { size = $0; step = 3; next }
              step == 3 { step = 4; next }
              step == 4 { refs = $0; print size, path; step = refs > 0 ? 5 : 0; next }
              step == 5 { if (--refs == 0) step = 0 }' "$closure/registration" |
              sort -rn | awk 'NR <= 20' | while read -r bytes path; do echo "  $(mib "$bytes") $path"; done >&2
            failed=1
          fi
        }
        check ${measure "k8s-controlplane"}
        check ${measure "k8s-worker"}
        check ${measure "test"}

        # The fit check every image build runs refuses a store, a hash tree or UKIs that leave no
        # room.
        if chalkos-image-size fits test ${testImage} ${testPartitions} ${testUKI} 3 64M 64M 256M 2>errors; then
          echo "error: the fit check passed a store larger than its slot" >&2
          failed=1
        fi
        grep -q "the store's data takes" errors || { cat errors >&2; failed=1; }
        if chalkos-image-size fits test ${testImage} ${testPartitions} ${testUKI} 3 2G 1M 256M 2>errors; then
          echo "error: the fit check passed a hash tree larger than its partition" >&2
          failed=1
        fi
        grep -q "the store's hash tree takes" errors || { cat errors >&2; failed=1; }
        if chalkos-image-size fits test ${testImage} ${testPartitions} ${testUKI} 3 2G 64M 100M 2>errors; then
          echo "error: the fit check passed UKIs larger than the ESP" >&2
          failed=1
        fi
        grep -q "3 UKIs of" errors || { cat errors >&2; failed=1; }
        chalkos-image-size fits test ${testImage} ${testPartitions} ${testUKI} 3 2G 64M 256M

        if [[ $failed != 0 ]]; then exit 1; fi
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
