# The image chalklab e2e tests boot: the base modules plus the probe, with small partitions.
{
  imports = [ ./probe.nix ];

  chalkos.disk = {
    espSize = "256M";
    storeSize = "2G";
    storeVeritySize = "64M";
    stateSize = "64M";
  };

  # chalklab attaches disks as virtio-blk-pci; the initrd needs them to find the store.
  boot.initrd.availableKernelModules = [
    "virtio_pci"
    "virtio_blk"
  ];

  boot.kernelParams = [
    "console=ttyS0,115200"
    "quiet"
  ];

  # Certificates last a year; a test renews them by delivering the identity, due or not.
  systemd.services.chalkd.environment.CHALKD_TEST_RENEW_ON_APPLY_IDENTITY = "1";
}
