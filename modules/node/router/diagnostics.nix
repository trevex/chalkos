# The diagnostics router images add to every image's: the routers on the path to an address, FRR's
# BGP sessions, neighbours and paths and its BFD peers of a VRF, which vtysh asks FRR for as a
# member of frrvty, the group of FRR's sockets, a NAT64 instance's sessions, which Jool shows to
# CAP_NET_ADMIN in the instance's namespace alone, and the DHCP servers' leases, which Kea gives
# on its control sockets to the group kea.
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
  # Asks each DHCP server on its control socket for its subnets and its leases, and prints the
  # leases of the subnet and MAC address of --subnet= and --mac=, or all, a line each. A server
  # that does not answer a question with JSON within 2 seconds, or whose answers hold no subnets
  # and leases, is named on standard error, and the others still listed. Servers still unasked
  # after 24 seconds are named as well, so the listing ends within the diagnostic's timeout.
  dhcpLeases = ''
    set -euo pipefail
    subnet= mac=
    for a in "$@"; do
      case $a in
      --subnet=*) subnet=''${a#--subnet=} ;;
      --mac=*) mac=''${a#--mac=} ;;
      esac
    done
    # The answer of the server of the socket $1 to the command $2; fails unless it is a JSON object.
    ask() {
      local answer
      answer=$(printf '%s' "$2" | ${pkgs.socat}/bin/socat -T 2 -t 2 - "UNIX-CONNECT:$1" 2>/dev/null) || return 1
      ${lib.getExe pkgs.jq} -e 'type == "object"' <<<"$answer" >/dev/null 2>&1 || return 1
      printf '%s' "$answer"
    }
    printf 'VRF\tSUBNET\tADDRESS\tMAC\tHOSTNAME\tENDS\n'
    shopt -s nullglob
    for socket in /run/kea/dhcp4.sock /run/kea/dhcp4-*.sock; do
      vrf=''${socket#/run/kea/dhcp4}
      vrf=''${vrf%.sock}
      vrf=''${vrf#-}
      vrf=''${vrf:-default}
      if ((SECONDS >= 24)); then
        echo "the DHCP server of VRF $vrf was not asked: the others took too long" >&2
        continue
      fi
      if ! config=$(ask "$socket" '{"command": "config-get"}') || ! leases=$(ask "$socket" '{"command": "lease4-get-all"}'); then
        echo "the DHCP server of VRF $vrf does not answer" >&2
        continue
      fi
      ${lib.getExe pkgs.jq} -rn --argjson c "$config" --argjson l "$leases" --arg vrf "$vrf" --arg subnet "$subnet" --arg mac "$mac" '
        ($c.arguments.Dhcp4.subnet4 | map({key: (.id | tostring), value: .["user-context"].name}) | from_entries) as $names
        | ($l.arguments.leases // [])[]
        | ($names[.["subnet-id"] | tostring] // "-") as $name
        | select(($subnet == "" or $name == $subnet) and ($mac == "" or .["hw-address"] == $mac))
        | [$vrf, $name, .["ip-address"], .["hw-address"], (if (.hostname // "") == "" then "-" else .hostname end), (.cltt + .["valid-lft"] | todate)]
        | @tsv' || echo "the DHCP server of VRF $vrf answered without its subnets or leases" >&2
    done
  '';
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
      # A NAT64 document's name of 11 characters at most, as the kinds take it, in ShortName's
      # alphabet of pkg/router.
      parameters.instance = {
        type = "pattern";
        regex = "^[a-z0-9]([-a-z0-9.]{0,9}[a-z0-9])?$";
        description = "Name of the NAT64 document, which names its instance and the instance's network namespace.";
      };
      # Jool answers about the instances of the namespace it runs in.
      networkNamespace = "nat64-{instance}";
      # The sessions show who connects where.
      role = "operator";
      timeout = "30s";
      capabilities = [ "CAP_NET_ADMIN" ];
    };
    # Only images with DHCP run Kea.
    chalkos.diagnostics.dhcp-leases = lib.mkIf config.chalkos.router.dhcp.enable {
      description = "The leases of the DHCP servers of every VRF, or of one subnet or MAC address: each lease's VRF, subnet, address, MAC address, host name and end, as Kea's lease commands report them.";
      command = [
        "${pkgs.bash}/bin/bash"
        "-c"
        dhcpLeases
        "dhcp-leases"
        "--subnet={subnet}"
        "--mac={mac}"
      ];
      parameters = {
        # A DHCPSubnet document's name, as the kinds take it.
        subnet = {
          type = "pattern";
          regex = "^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$";
          optional = true;
          description = "Name of the DHCPSubnet document whose leases to list.";
        };
        mac = {
          type = "pattern";
          regex = "^[0-9a-f]{2}(:[0-9a-f]{2}){5}$";
          optional = true;
          description = "MAC address, in lower case, whose leases to list.";
        };
      };
      # The leases show which machines are on the networks.
      role = "operator";
      timeout = "30s";
      # Kea's control sockets in /run/kea admit the group kea.
      groups = [ "kea" ];
    };
    chalkos.diagnostics.bfd-peers =
      vtysh "One VRF's BFD sessions and their state, as show bfd peers prints them."
        "show bfd vrf {vrf} peers"
        { inherit vrf; };
  };
}
