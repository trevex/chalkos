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
}
