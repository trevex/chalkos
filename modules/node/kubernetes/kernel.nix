# The kernel modules of images that run Kubernetes beyond the base groups every image carries,
# which hold netfilter, bridges, queueing disciplines, overlayfs and the tunnels and virtual
# devices of the pod networks: SCTP, for SCTP services, and Open vSwitch, for the pod networks
# that run on it. The group is declared on every image, so a variant without Kubernetes may add it.
{ config, lib, ... }:
{
  chalkos.kernel.groups.kubernetes.directories = [
    "net/sctp"
    "net/openvswitch"
  ];
  chalkos.kernel.moduleGroups = lib.mkIf config.chalkos.kubernetes.enable [ "kubernetes" ];
}
