{
  pkgs,
  self,
  testing,
  docs,
  ...
}:
let
  inherit (pkgs) lib;
in
{
  inherit (pkgs.chalkos)
    chalkbuild
    chalkcfg
    chalkctl
    chalkd
    chalklab
    chalklab-e2e
    chalkos-console
    chalkos-storage
    ;

  test-secureboot = import ./testing/secureboot.nix { inherit pkgs; };
  test-secrets = testing.secrets;

  # The documentation site.
  docs = docs.site;

  test-image = testing.images.test-metal;
  # The test image's OCI image layout, as mkImages's oci output writes it.
  test-image-oci = testing.oci.test-metal;
  # The repart definitions of the test image's system region, which it ships.
  test-repart-definitions = testing.nixos.test-metal.config.environment.etc."chalkos/repart.d".source;
  test-storage-image = testing.images.storage-metal;
  test-installer = testing.installer;
  test-kubernetes-image = testing.images.k8s-kvm;
  # The HA test's control planes run the Kubernetes image on metal.
  test-kubernetes-ha-image = testing.images.k8s-metal;
  test-upgrade-image = testing.upgradeImage;
  test-unhealthy-image = testing.unhealthyImage;
  test-kubernetes-ha-upgrade-image = testing.haUpgradeImage;
  test-kubernetes-images = testing.kubernetesImages;
  # The router test's routers run the router image on KVM.
  test-router-image = testing.images.router-kvm;

  # The kubelet of every Kubernetes release an image can run, by minor, to build by hand after a
  # change of the table. No check builds it: each release but nixpkgs's own compiles from source.
  kubelet-releases = pkgs.linkFarm "kubelet-releases" (
    import ../modules/node/kubernetes/releases.nix { inherit lib pkgs; }
  );

  # The installer without an OS CA: it accepts any client until it installs a node.
  inherit (self.lib.mkImages { variants = { }; }) installer;
}
