# What the layers share of the node's network: forwarding, which a layer turns on, and the target
# after which the node's addresses have settled.
{ config, lib, ... }:
{
  options.chalkos.forwarding.enable = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = ''
      Whether the node forwards IPv4 and IPv6 packets between its interfaces, as a Kubernetes node
      does for its pods and a router for its networks. The Kubernetes and the router layer turn it
      on.
    '';
  };

  config = {
    boot.kernel.sysctl = lib.mkIf config.chalkos.forwarding.enable {
      "net.ipv4.ip_forward" = 1;
      "net.ipv6.conf.all.forwarding" = 1;
    };

    # The node's addresses have settled. A unit that configures or announces addresses, such as a
    # routing daemon's, orders itself before the target and is wanted by it; a unit that picks the
    # node's addresses, such as the Kubernetes preparation, orders itself after it.
    systemd.targets.chalkos-node-addresses = {
      description = "The node's addresses have settled";
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
    };
  };
}
