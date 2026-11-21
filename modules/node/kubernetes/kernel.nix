# The kernel modules of images that run Kubernetes beyond the base groups every image carries,
# which hold netfilter, bridges, queueing disciplines, overlayfs and the tunnels and virtual
# devices of the pod networks: SCTP, for SCTP services, and Open vSwitch, for the pod networks
# that run on it. The groups are declared on every image, so an image without Kubernetes may add
# them.
{
  config,
  lib,
  pkgs,
  ...
}:
{
  chalkos.kernel.groups.kubernetes.directories = [
    "net/sctp"
    "net/openvswitch"
  ];
  # KubeVirt's virtual machines: KVM (built in on arm64), vhost for their network and vsock, VFIO
  # and UIO for devices passed through. The kubevirt feature adds the group; the kernel loads the
  # modules on demand, KVM's by the CPU's features and vhost's when /dev/vhost-net opens.
  chalkos.kernel.groups.kubevirt.directories =
    lib.optional pkgs.stdenv.hostPlatform.isx86_64 "arch/x86/kvm"
    ++ [
      "drivers/vhost"
      "drivers/vfio"
      "drivers/uio"
    ];
  chalkos.kernel.moduleGroups = lib.mkIf config.chalkos.kubernetes.enable [ "kubernetes" ];
}
