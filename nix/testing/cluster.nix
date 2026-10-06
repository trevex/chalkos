# The cluster whose role images the e2e tests boot. Each role has one node standing for the VM;
# its image records that node's storage section on first boot, as Install will.
{ self }:
let
  definition = {
    chalkos.cluster = {
      name = "chalklab";
      endpoint = "https://10.0.0.10:6443";
    };
    # The default layout: VAR fills the system disk.
    chalkos.roles.test = { };
    chalkos.nodes.chalklab = {
      role = "test";
      storage.system.disk = "/dev/vda";
    };
  };
  # Taken from a separate evaluation, so the role images do not depend on their own cluster's
  # manifest.
  storageOf =
    node: (self.lib.mkCluster { modules = [ definition ]; }).manifest.nodes.${node}.identity.storage;
  testImage = node: [
    ../../modules/testing/test-image.nix
    (import ../../modules/testing/storage-seed.nix (storageOf node))
  ];
in
self.lib.mkCluster {
  modules = [
    definition
    { chalkos.roles.test.nixosModules = testImage "chalklab"; }
  ];
}
