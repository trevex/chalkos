# Virtual machines under KVM, such as QEMU, libvirt and Proxmox: virtio is in the base module
# groups; the console is the first serial port, and the QEMU guest agent answers the host.
{ config, lib, ... }:
{
  boot.kernelParams = [ "console=ttyS0,115200" ];

  # The store and STATE are on virtio disks, which the initrd must see before it can mount them:
  # virtio-blk, or virtio-scsi as Proxmox offers it. The initrd needs no network, so no NIC driver.
  boot.initrd.availableKernelModules = [
    "virtio_pci"
    "virtio_blk"
    "virtio_scsi"
  ];

  # The agent starts when the host offers its channel, and only under KVM.
  services.qemuGuest.enable = true;
  systemd.services.qemu-guest-agent = {
    unitConfig.ConditionVirtualization = "kvm";
    # It shuts the node down with /sbin/poweroff, halt, reboot or shutdown, which systemd gives.
    serviceConfig.BindReadOnlyPaths = map (b: "${config.systemd.package}/bin/${b}:/sbin/${b}") [
      "poweroff"
      "halt"
      "reboot"
      "shutdown"
    ];
    # The host may ask the agent about the node and shut it down; it never runs commands or reads
    # and writes files on the node, which the agent would otherwise allow.
    serviceConfig.ExecStart = lib.mkForce "${config.services.qemuGuest.package}/bin/qemu-ga --statedir /run/qemu-ga --allow-rpcs=${
      lib.concatStringsSep "," [
        "guest-sync-delimited"
        "guest-sync"
        "guest-ping"
        "guest-info"
        "guest-get-osinfo"
        "guest-get-host-name"
        "guest-get-time"
        "guest-get-timezone"
        "guest-network-get-interfaces"
        "guest-shutdown"
      ]
    }";
  };
}
