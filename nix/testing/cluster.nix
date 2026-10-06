# The cluster whose `test` role image the e2e tests boot.
{ self }:
self.lib.mkCluster {
  modules = [
    {
      chalkos.cluster = {
        name = "chalklab";
        endpoint = "https://10.0.0.10:6443";
      };
      chalkos.roles.test.nixosModules = [ ../../modules/testing/test-image.nix ];
    }
  ];
}
