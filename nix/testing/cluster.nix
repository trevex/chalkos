# The cluster whose images the e2e tests boot, its secrets, and manifests the tests deliver with
# chalkctl. Each role has a node standing for the VM that runs it.
{ self, pkgs }:
let
  secrets = import ./secrets.nix { inherit pkgs; };
  definition = {
    chalkos.cluster = {
      name = "chalklab";
      endpoint = "https://10.0.0.10:6443";
      osCA = "${secrets}/secrets.pub.json";
    };
    # The default layout: VAR fills the system disk.
    chalkos.roles.test = {
      kubernetes.kind = null;
      nixosModules = [ ../../modules/testing/test-image.nix ];
    };
    chalkos.nodes.chalklab = {
      role = "test";
      storage.system.disk = "/dev/vda";
    };
    # A node the installer installs onto the disk with this serial.
    chalkos.nodes.chalklab-target = {
      role = "test";
      storage.system.disk.serial = "chalk-target";
    };
    # A fixed VAR, an unencrypted and a raw volume next to it, and a volume on a second disk.
    chalkos.roles.storage = {
      kubernetes.kind = null;
      nixosModules = [ ../../modules/testing/test-image.nix ];
      storage = {
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
    };
    chalkos.nodes.chalklab-storage = {
      role = "storage";
      storage.system.disk = "/dev/vda";
    };
  };
  manifestOf =
    name: modules:
    pkgs.writeText "${name}.json" (
      builtins.toJSON (self.lib.mkCluster { modules = [ definition ] ++ modules; }).manifest
    );
in
{
  cluster = self.lib.mkCluster { modules = [ definition ]; };
  inherit secrets;
  manifests = pkgs.linkFarm "chalkos-test-manifests" {
    "base.json" = manifestOf "base" [ ];
    # A new encrypted volume on an empty disk: an additive change.
    "added.json" = manifestOf "added" [
      {
        chalkos.nodes.chalklab.storage.volumes.extra = {
          disk.serial = "chalk-extra";
          mountPoint = "/srv/extra";
        };
      }
    ];
    # VAR with a fixed size where it filled its disk: a destructive change.
    "destructive.json" = manifestOf "destructive" [
      { chalkos.nodes.chalklab.storage.var.size = "1G"; }
    ];
  };
}
