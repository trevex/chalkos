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
  # A variant's metal image again, with the module given and a file in /etc that names the image,
  # so its store, and with it its root hash, differs from the variant's image and from the others.
  variantAs =
    variant: name: extra:
    let
      v = arguments.variants.${variant};
    in
    (self.lib.mkImages (
      arguments
      // {
        platforms = [ "metal" ];
        variants.${variant} = v // {
          modules = v.modules or [ ] ++ [
            { environment.etc."chalktest-image".text = name; }
            extra
          ];
        };
      }
    )).images."${variant}-metal";
in
images
// {
  inherit secrets;
  # The images e2e-upgrade installs after the test image: the upgrade, which declares a Farewell
  # kind the test image does not, so its config is config the test image refuses, and one that
  # never becomes healthy, as a unit fails at every boot the way a broken service would, and falls
  # back after one try of 30 seconds.
  upgradeImage = variantAs "test" "upgrade" (
    { lib, ... }:
    {
      chalkos.kinds.Farewell = {
        group = "greeting.example.org";
        version = "v1alpha1";
        spec.message = lib.mkOption {
          type = lib.types.nonEmptyStr;
          description = "What the node says on leaving.";
        };
      };
    }
  );
  unhealthyImage = variantAs "test" "unhealthy" {
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
  # The Kubernetes image the HA test's rolling upgrade installs.
  haUpgradeImage = variantAs "k8s" "upgrade" { };
  kubernetesImages = import ./kubernetes-images.nix {
    inherit pkgs;
    kubernetesVersion = images.nixos.k8s-metal.config.chalkos.kubernetes.package.version;
  };
}
