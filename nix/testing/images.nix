# The images the e2e tests boot, and their secrets. The tests' clusters are the YAML of
# test/fixtures/clusters, whose Image documents point at these images' directories.
{ self, pkgs }:
let
  secrets = import ./secrets.nix { inherit pkgs; };
  # Every variant has the test image's small partitions and the probe the tests read. test, which
  # most VM tests boot, carries chalklab's Greeting kind; storage is the storage test's; k8s is the
  # image of the Kubernetes tests' control planes and workers.
  arguments = {
    osCA = "${secrets}/secrets.pub.json";
    modules = [ ../../modules/testing/test-image.nix ];
    variants = {
      test = {
        kubernetes = false;
        modules = [ ../../modules/testing/greeting.nix ];
      };
      storage.kubernetes = false;
      k8s = { };
    };
  };
  images = self.lib.mkImages arguments;
  # A variant's metal image again, at another version and with the module given.
  variantAt =
    variant: version: extra:
    let
      v = arguments.variants.${variant};
    in
    (self.lib.mkImages (
      arguments
      // {
        platforms = [ "metal" ];
        variants.${variant} = v // {
          modules = v.modules or [ ] ++ [ ({ system.image.version = version; } // extra) ];
        };
      }
    )).images."${variant}-metal";
in
images
// {
  inherit secrets;
  # The test image at the versions e2e-upgrade installs: 0.2.0, and 0.3.0, which never becomes
  # healthy, as a unit fails at every boot the way a broken service would, and falls back after
  # one try of 30 seconds.
  upgradeImage = variantAt "test" "0.2.0" { };
  unhealthyImage = variantAt "test" "0.3.0" {
    chalkos.upgrade = {
      bootTries = 1;
      healthTimeout = 30;
    };
    systemd.services.chalktest-broken = {
      description = "Fail, as a broken service does";
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        Type = "oneshot";
        ExecStart = "${pkgs.coreutils}/bin/false";
      };
    };
  };
  # The Kubernetes image at the version the HA test's rolling upgrade installs.
  haUpgradeImage = variantAt "k8s" "0.2.0" { };
  kubernetesImages = import ./kubernetes-images.nix {
    inherit pkgs;
    kubernetesVersion = images.nixos.k8s-metal.config.chalkos.kubernetes.package.version;
  };
}
