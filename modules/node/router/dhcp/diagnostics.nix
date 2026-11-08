# DHCP's diagnostics: the DHCP servers' leases, which Kea gives on its control sockets to the
# group kea.
{ lib, pkgs }:
let
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
    for socket in /run/kea/dhcp4.sock /run/kea/dhcp4-*.sock /run/kea/dhcp6.sock /run/kea/dhcp6-*.sock; do
      # dhcp4.sock and dhcp6.sock are no patterns, so nullglob keeps them whether they exist or not.
      [[ -S $socket ]] || continue
      family=''${socket#/run/kea/dhcp}
      family=''${family:0:1}
      vrf=''${socket#/run/kea/dhcp"$family"}
      vrf=''${vrf%.sock}
      vrf=''${vrf#-}
      vrf=''${vrf:-default}
      if ((SECONDS >= 24)); then
        echo "the DHCPv$family server of VRF $vrf was not asked: the others took too long" >&2
        continue
      fi
      if ! config=$(ask "$socket" '{"command": "config-get"}') || ! leases=$(ask "$socket" "{\"command\": \"lease$family-get-all\"}"); then
        echo "the DHCPv$family server of VRF $vrf does not answer" >&2
        continue
      fi
      # A delegated prefix is its address and length.
      ${lib.getExe pkgs.jq} -rn --argjson c "$config" --argjson l "$leases" --arg vrf "$vrf" --arg subnet "$subnet" --arg mac "$mac" '
        (($c.arguments.Dhcp4.subnet4 // $c.arguments.Dhcp6.subnet6) | map({key: (.id | tostring), value: .["user-context"].name}) | from_entries) as $names
        | ($l.arguments.leases // [])[]
        | ($names[.["subnet-id"] | tostring] // "-") as $name
        | select(($subnet == "" or $name == $subnet) and ($mac == "" or .["hw-address"] == $mac))
        | [$vrf, $name, (if .type == "IA_PD" then "\(.["ip-address"])/\(.["prefix-len"])" else .["ip-address"] end),
          (if (.["hw-address"] // "") == "" then "-" else .["hw-address"] end),
          (if (.hostname // "") == "" then "-" else .hostname end), (.cltt + .["valid-lft"] | todate)]
        | @tsv' || echo "the DHCPv$family server of VRF $vrf answered without its subnets or leases" >&2
    done
  '';
in
{
  dhcp-leases = {
    description = "The leases of the DHCPv4 and DHCPv6 servers of every VRF, or of one subnet or MAC address: each lease's VRF, subnet, address or delegated prefix, MAC address, host name and end, as Kea's lease commands report them.";
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
}
