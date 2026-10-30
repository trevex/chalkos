# The diagnostics router images add to every image's: the routers on the path to an address, FRR's
# BGP sessions, neighbours and paths and its BFD peers of a VRF, which vtysh asks FRR for as a
# member of frrvty, the group of FRR's sockets, and a NAT64 instance's sessions, which Jool shows
# to CAP_NET_ADMIN alone.
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
      vtysh "One VRF's BGP sessions of each address family, as show bgp summary prints them."
        "show bgp vrf {vrf} summary"
        { inherit vrf; };
    chalkos.diagnostics.bgp-neighbor =
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
    chalkos.diagnostics.bgp-route =
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
    chalkos.diagnostics.nat64-sessions = {
      description = "A NAT64 instance's sessions, as jool session display prints them: each translated TCP, UDP and ICMP connection's IPv6 and IPv4 addresses and ports, and when it expires.";
      command = [
        "${pkgs.bash}/bin/bash"
        "-c"
        ''
          for protocol in --tcp --udp --icmp; do
            ${pkgs.jool-cli}/bin/jool -i "$1" session display --numeric "$protocol" || exit
          done
        ''
        "nat64-sessions"
        "{instance}"
      ];
      parameters.instance = {
        type = "pattern";
        regex = "^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$";
        description = "Name of the NAT64 document, which names its instance.";
      };
      # The sessions show who connects where.
      role = "operator";
      timeout = "30s";
      capabilities = [ "CAP_NET_ADMIN" ];
    };
    chalkos.diagnostics.bfd-peers =
      vtysh "One VRF's BFD sessions and their state, as show bfd peers prints them."
        "show bfd vrf {vrf} peers"
        { inherit vrf; };
  };
}
