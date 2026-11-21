# Ceph CSI and Rook: the rbd, ceph and nbd modules load at boot, so ceph-csi, which checks
# /sys/module before it runs modprobe, finds them loaded, and LVM stays for Rook's OSDs on
# encrypted or metadata devices.
{
  chalkosLib,
  config,
  lib,
  ...
}:
let
  k = config.chalkos.kubernetes;
in
{
  options.chalkos.kubernetes = chalkosLib.internal.feature "Kubernetes" "ceph" {
    carries = "the `rbd`, `ceph` and `nbd` kernel modules, loaded at boot, and LVM for Rook's OSDs";
  };

  config = lib.mkIf (k.enable && k.ceph.enable) {
    boot.kernelModules = [
      "rbd"
      "ceph"
      "nbd"
    ];
    services.lvm.enable = true;
  };
}
