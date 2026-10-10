{
  pkgs,
  self,
}:
let
  inherit (pkgs) lib;
  testing = import ./testing/cluster.nix { inherit self pkgs; };
  goModule = pkgs.callPackage ./go-module.nix { };
  docs = import ./docs.nix { inherit pkgs self; };

  goPaths = [
    ../cmd
    ../pkg
    ../test/e2e
    ../test/fixtures
  ];
in
{
  test-secureboot = import ./testing/secureboot.nix { inherit pkgs; };
  test-secrets = testing.secrets;
  test-manifests = testing.manifests;

  chalkctl = goModule {
    pname = "chalkctl";
    paths = goPaths;
    subPackages = [ "cmd/chalkctl" ];
    meta.mainProgram = "chalkctl";
    nativeBuildInputs = [ pkgs.makeWrapper ];
    postFixup = ''
      wrapProgram $out/bin/chalkctl --prefix PATH : ${
        lib.makeBinPath [
          pkgs.mtools
          pkgs.sbsigntool
        ]
      }
    '';
  };

  # chalklab, with the tools a lab runs: QEMU, OVMF with Secure Boot, swtpm, the switch, socat for
  # guest forwards, the tools that sign images and enroll the lab's keys, and chalkctl. A package of
  # its own, so chalkctl's closure stays free of them. QEMU is the headless one of the binary cache;
  # chalklab denies it io_uring, whose main loop loses TPM emulator commands.
  chalklab = goModule {
    pname = "chalklab";
    paths = goPaths;
    subPackages = [ "cmd/chalklab" ];
    # The go-unit check runs its tests.
    doCheck = false;
    meta.mainProgram = "chalklab";
    nativeBuildInputs = [ pkgs.makeWrapper ];
    postFixup = ''
      wrapProgram $out/bin/chalklab --prefix PATH : ${
        lib.makeBinPath [
          pkgs.qemu_test
          pkgs.swtpm
          pkgs.vde2
          pkgs.socat
          pkgs.mtools
          pkgs.sbsigntool
          pkgs.python3Packages.virt-firmware
          self.packages.${pkgs.stdenv.hostPlatform.system}.chalkctl
        ]
      } \
        --set-default CHALKLAB_OVMF_CODE ${pkgs.OVMFFull.fd}/FV/OVMF_CODE.fd \
        --set-default CHALKLAB_OVMF_VARS ${pkgs.OVMFFull.fd}/FV/OVMF_VARS.fd
    '';
  };

  # The documentation site.
  docs = docs.site;

  chalkos-storage = pkgs.callPackage ./chalkos-storage.nix { };
  chalkd = pkgs.callPackage ./chalkd.nix { };

  test-image = testing.cluster.roles.test.images.metal;
  # The repart definitions of the test role's system region, which its image ships.
  test-repart-definitions =
    testing.cluster.roles.test.nixos.metal.config.environment.etc."chalkos/repart.d".source;
  test-storage-image = testing.cluster.roles.storage.images.metal;
  test-installer = testing.cluster.installer.image;
  test-kubernetes-controlplane-image = testing.cluster.roles.k8s-controlplane.images.kvm;
  test-kubernetes-worker-image = testing.cluster.roles.k8s-worker.images.kvm;
  test-kubernetes-ha-image = testing.haCluster.roles.k8s-ha.images.metal;
  test-upgrade-image = testing.upgradeImage;
  test-unhealthy-image = testing.unhealthyImage;
  test-kubernetes-ha-upgrade-image = testing.haUpgradeImage;
  test-kubernetes-images = testing.kubernetesImages;

  # The installer of a cluster without an OS CA: it accepts any client until it installs a node.
  installer =
    (self.lib.mkCluster {
      modules = [
        {
          chalkos.cluster = {
            name = "generic";
            endpoint = "https://127.0.0.1:6443";
          };
        }
      ];
    }).installer.image;

  chalklab-e2e = goModule {
    pname = "chalklab-e2e";
    paths = goPaths;
    doCheck = false;
    buildPhase = ''
      runHook preBuild
      go test -c -o chalklab-e2e ./test/e2e
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      install -Dm755 chalklab-e2e $out/bin/chalklab-e2e
      runHook postInstall
    '';
  };
}
