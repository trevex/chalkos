# The diagnostics router images add to every image's: the routers on the path to an address, and
# FRR's BGP sessions, neighbours and paths and its BFD peers, which vtysh asks FRR for as a member
# of frrvty, the group of FRR's sockets.
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
in
{
  config = lib.mkIf config.chalkos.router.enable {
    # traceroute's default UDP probes need no privileges; one probe a hop with a wait of two
    # seconds keeps a path of 30 hops within the timeout.
    chalkos.diagnostics.traceroute = {
      description = "The routers on the path to an address, as traceroute -n prints them: one probe a hop, 30 hops at most.";
      command = [
        "${pkgs.traceroute}/bin/traceroute"
        "-n"
        "-m"
        "30"
        "-q"
        "1"
        "-w"
        "2"
        "{address}"
      ];
      parameters.address = {
        type = "address";
        description = "IPv4 or IPv6 address.";
      };
      # It sends traffic.
      role = "operator";
      timeout = "90s";
    };
    chalkos.diagnostics.bgp-summary =
      vtysh "FRR's BGP sessions of each address family, as show bgp summary prints them."
        "show bgp summary"
        { };
    chalkos.diagnostics.bgp-neighbor =
      vtysh
        "One BGP session in detail, as show bgp neighbors prints it: its state, timers, capabilities and routes."
        "show bgp neighbors {peer}"
        {
          peer = {
            type = "pattern";
            regex = "^([0-9A-Fa-f:.]+|[A-Za-z0-9_.:@-]{1,15})$";
            description = "The session's interface or the neighbour's address, as the node's status lists it.";
          };
        };
    chalkos.diagnostics.bgp-route =
      vtysh
        "BGP's paths to a prefix, as show bgp prints them: each path's AS path, next hop and attributes, and the best."
        "show bgp {family} unicast {prefix}"
        {
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
    chalkos.diagnostics.bfd-peers =
      vtysh "FRR's BFD sessions and their state, as show bfd peers prints them." "show bfd peers"
        { };
  };
}
