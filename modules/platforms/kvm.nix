# Virtual machines under KVM, such as QEMU, libvirt and Proxmox: virtio is in the base module
# groups; the console is the first serial port, and the QEMU guest agent answers the host.
{ config, lib, ... }:
{
  boot.kernelParams = [ "console=ttyS0,115200" ];

  # The agent starts when the host offers its channel, and only under KVM.
  services.qemuGuest.enable = true;
  systemd.services.qemu-guest-agent = {
    unitConfig.ConditionVirtualization = "kvm";
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
