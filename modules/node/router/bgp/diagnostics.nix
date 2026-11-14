# BGP's diagnostics: FRR's BGP sessions, neighbours and paths and its BFD peers of a VRF, which
# vtysh asks FRR for as a member of frrvty, the group of FRR's sockets.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  # vtysh runs one show command, which the diagnostic's arguments complete.
  vtysh = description: show: parameters: {
    inherit description parameters;
    command = [
      "${pkgs.frr}/bin/vtysh"
      "-c"
      show
    ];
    role = "reader";
    timeout = "10s";
    groups = [ "frrvty" ];
  };
  # The VRF a vtysh diagnostic asks about; FRR names the default VRF default. The regex is that of
  # ShortName in pkg/router, which chalkd checks a VRF's name against; change both together.
  vrf = {
    type = "pattern";
    regex = "^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$";
    default = "default";
    description = "VRF to ask about, by the name of its VRF document, or default for the default VRF.";
  };
in
{
  config = lib.mkIf (config.chalkos.router.enable && config.chalkos.router.bgp.enable) {
    chalkos.diagnostics = {
      bgp-summary =
        vtysh "One VRF's BGP sessions of each address family, as show bgp summary prints them."
          "show bgp vrf {vrf} summary"
          { inherit vrf; };
      bgp-neighbor =
        vtysh
          "One BGP session in detail, as show bgp neighbors prints it: its state, timers, capabilities and routes."
          "show bgp vrf {vrf} neighbors {peer}"
          {
            peer = {
              type = "pattern";
              regex = "^([0-9A-Fa-f:.]+|[A-Za-z0-9_.:@-]{1,15})$";
              description = "The session's interface or the neighbour's address, as the node's status lists it.";
            };
            inherit vrf;
          };
      bgp-route =
        vtysh
          "One VRF's BGP paths to a prefix, as show bgp prints them: each path's AS path, next hop and attributes, and the best."
          "show bgp vrf {vrf} {family} unicast {prefix}"
          {
            inherit vrf;
            prefix = {
              type = "prefix";
              description = "Address range of the family, such as ::/0.";
            };
            family = {
              type = "enum";
              values = [
                "ipv4"
                "ipv6"
              ];
              default = "ipv6";
              description = "Address family of the prefix.";
            };
          };
      bfd-peers =
        vtysh "One VRF's BFD sessions and their state, as show bfd peers prints them."
          "show bfd vrf {vrf} peers"
          { inherit vrf; };
    };
  };
}
