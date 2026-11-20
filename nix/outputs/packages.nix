{
  pkgs,
  self,
  testing,
  docs,
  sbom,
  ...
}:
let
  inherit (pkgs) lib;
  # chalkctl and chalkbuild as the flake's packages, whose version commands name the git revision
  # they were built from. Only these two take it: a revision in a program of the images, or in the
  # chalkbuild their oci attributes run, would rebuild every image on every commit. Their sbom is
  # the CycloneDX SBOM a release publishes beside the binary.
  withRevision =
    program:
    program.overrideAttrs (
      final: old: {
        ldflags = old.ldflags ++ [
          "-X github.com/trevex/chalkos/pkg/version.Revision=${self.rev or self.dirtyRev or "unknown"}"
        ];
        passthru = old.passthru // {
          sbom = sbom.program final.finalPackage;
        };
      }
    );
in
{
  inherit (pkgs.chalkos)
    chalkcfg
    chalkd
    chalklab
    chalklab-e2e
    chalkos-console
    chalkos-registry
    chalkos-storage
    ;
  chalkbuild = withRevision pkgs.chalkos.chalkbuild;
  chalkctl = withRevision pkgs.chalkos.chalkctl;

  test-secureboot = import ../testing/secureboot.nix { inherit pkgs; };
  test-secrets = testing.secrets;

  # The documentation site.
  docs = docs.site;

  test-image = testing.test-metal;
  # The test image's OCI image layout, as mkImage's oci writes it.
  test-image-oci = testing.test-metal.oci;
  # The repart definitions of the test image's system region, which it ships.
  test-repart-definitions = testing.test-metal.nixos.config.environment.etc."chalkos/repart.d".source;
  test-installer = testing.installer;
  test-kubernetes-image = testing.k8s-kvm;
  # The HA test's control planes run the Kubernetes image on metal.
  test-kubernetes-ha-image = testing.k8s-metal;
  test-upgrade-image = testing.upgradeImage;
  test-unhealthy-image = testing.unhealthyImage;
  test-kubernetes-ha-upgrade-image = testing.haUpgradeImage;
  test-kubernetes-images = testing.kubernetesImages;
  # The router test's routers run the router image on KVM.
  test-router-image = testing.router-kvm;

  # The kubelet of every Kubernetes release an image can run, by minor, to build by hand after a
  # change of the table. Each compiles from source; the checks build only the newest, which the
  # test images run.
  kubelet-releases = pkgs.linkFarm "kubelet-releases" (
    lib.mapAttrs (
      _: release:
      pkgs.callPackage ../../modules/node/kubernetes/kubelet.nix { inherit (release) version hash; }
    ) self.lib.kubernetesReleases
  );

  # The installer without an OS CA: it accepts any client until it installs a node.
  installer = self.lib.mkInstaller { };
}
