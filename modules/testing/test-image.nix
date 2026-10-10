# The image chalklab e2e tests boot: the base modules plus the probe, with small partitions.
{ config, lib, ... }:
{
  imports = [ ./probe.nix ];

  chalkos.disk = {
    espSize = "260M";
    storeSize = "2G";
    storeVeritySize = "64M";
    stateSize = "64M";
  };

  # chalklab attaches disks as virtio-blk-pci, which the initrd needs to find the store. The kvm
  # platform loads them itself, and the Kubernetes tests boot its images to prove it. The metal
  # platform leaves them out, as real machines have no virtio disks, so its test images, which run
  # under QEMU too, get them here.
  boot.initrd.availableKernelModules = lib.mkIf (config.chalkos.platform.name != "kvm") [
    "virtio_pci"
    "virtio_blk"
  ];

  # The platform puts the console on the first serial port, where the tests read the facts.
  boot.kernelParams = [ "quiet" ];

  # Certificates last a year; a test renews them by delivering the identity, due or not.
  systemd.services.chalkd.environment.CHALKD_TEST_RENEW_ON_APPLY_IDENTITY = "1";
}
