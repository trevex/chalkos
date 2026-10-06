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
    # A fixed VAR, an unencrypted and a raw volume next to it, and a volume on a second disk.
    chalkos.roles.storage.storage = {
      var.size = "2G";
      volumes = {
        plain = {
          size = "256M";
          mountPoint = "/srv/plain";
          encryption.mode = "none";
        };
        raw = {
          size = "128M";
          format = null;
        };
        data = {
          disk.serial = "chalk-data";
          format = "xfs";
          mountPoint = "/var/lib/data";
        };
      };
    };
    chalkos.nodes.chalklab-storage = {
      role = "storage";
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
    {
      chalkos.roles.test.nixosModules = testImage "chalklab";
      chalkos.roles.storage.nixosModules = testImage "chalklab-storage";
    }
  ];
}
