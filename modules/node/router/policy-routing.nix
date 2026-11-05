# Policy routing on router images: the PolicyRoute documents' rules mark the packets they match in
# the firewall's chain policy-routing, which the output router-nftables renders, and networkd
# keeps a kernel rule per mark and the NextHop documents' tables, which the output networkd renders
# with chalkos-render router-networkd into the Network of lo and those of the next hops' links.
# Both outputs read the NextHop documents: the firewall for the family and table of a rule's next
# hop, networkd for every next hop's routes. The outputs' effects reload the firewall and
# networkd; networkd adds a next hop's route again each time its link comes up.
{ config, lib, ... }:
{
  config = lib.mkIf config.chalkos.router.enable {
    chalkos.outputs.router-nftables.inputs = [
      "PolicyRoute"
      "NextHop"
    ];
    chalkos.outputs.networkd.inputs = [
      "PolicyRoute"
      "NextHop"
    ];
  };
}
