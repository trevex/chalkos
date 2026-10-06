# Test-only stand-in for Install: records the node's storage section on STATE at first boot, so
# chalkos-storage creates and mounts the node's volumes. Takes the section as the manifest renders it.
storage:
{ pkgs, ... }:
let
  section = pkgs.writeText "storage.json" (builtins.toJSON storage);
in
{
  boot.initrd.systemd = {
    storePaths = [ section ];
    services.chalktest-storage-seed = {
      description = "Record the test node's storage section on STATE";
      unitConfig = {
        DefaultDependencies = false;
        ConditionPathExists = "!/sysroot/state/storage/storage.json";
      };
      requires = [ "chalkos-state.service" ];
      after = [ "chalkos-state.service" ];
      before = [ "chalkos-storage.service" ];
      requiredBy = [ "chalkos-storage.service" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "/bin/install -D -m 0600 ${section} /sysroot/state/storage/storage.json";
      };
    };
  };
}
