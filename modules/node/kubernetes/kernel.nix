# The kernel modules of images that run Kubernetes, beside those every image carries: what
# containerd, kube-proxy and the pod networks a cluster may run load, such as SCTP services and
# Open vSwitch. The group is declared on every image, so a variant without Kubernetes may add it.
{ config, lib, ... }:
{
  chalkos.kernel.groups.kubernetes = {
    directories = [
      "net/netfilter"
      "net/ipv4/netfilter"
      "net/ipv6/netfilter"
      "net/bridge"
      "net/sched"
      "net/sctp"
      "net/openvswitch"
    ];
    modules = [
      "overlay"
      "veth"
      "vxlan"
      "geneve"
      "ipip"
      "dummy"
    ];
  };
  chalkos.kernel.moduleGroups = lib.mkIf config.chalkos.kubernetes.enable [ "kubernetes" ];
}
