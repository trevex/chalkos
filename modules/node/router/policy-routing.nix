# Policy routing on router images: the PolicyRoute documents' rules mark the packets they match in
# the firewall's chain policy-routing, which the output router-nftables renders, and networkd
# keeps a kernel rule per mark and the next hops' tables, which the output networkd renders with
# chalkos-render router-networkd into the Network of lo and those of the next hops' links. The
# outputs' effects reload the firewall and networkd; networkd adds a next hop's route again each
# time its link comes up.
{ config, lib, ... }:
{
  config = lib.mkIf config.chalkos.router.enable {
    chalkos.outputs.router-nftables.inputs = [ "PolicyRoute" ];
    chalkos.outputs.networkd.inputs = [ "PolicyRoute" ];
  };
}
