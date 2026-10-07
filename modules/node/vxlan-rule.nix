# Fills the chalkos-vxlan chain from the file holding the node's address, and empties it without
# one. It fails, leaving the chain empty, when the file holds anything but a bare address.
{
  lib,
  writeShellApplication,
  util-linux,
  iptables,
  ipv6 ? true,
  lockFile ? "/run/chalkos-vxlan-rule.lock",
}:
writeShellApplication {
  name = "chalkos-vxlan-rule";
  runtimeInputs = [
    iptables
    util-linux
  ];
  text = ''
    # The firewall runs this whenever it starts, also while the node picks its address: one run
    # at a time, so the last one sees the file as it is now.
    exec 9>>${lib.escapeShellArg lockFile}
    flock 9
    families=(iptables ${lib.optionalString ipv6 "ip6tables"})
    for family in "''${families[@]}"; do
      "$family" -w -F chalkos-vxlan
    done
    status=0
    if [ "$#" -eq 1 ] && [ -e "$1" ]; then
      mapfile -t lines <"$1"
      # Anything but a bare address would end up among iptables's arguments.
      if [ "''${#lines[@]}" -eq 1 ] && [[ "''${lines[0]}" =~ ^[0-9A-Fa-f.:]+$ ]]; then
        address=''${lines[0]}
        family=iptables
        case "$address" in
          *:*) family=ip6tables ;;
        esac
        "$family" -w -A chalkos-vxlan -p udp --dport 8472 -d "$address" \
          -m addrtype --dst-type LOCAL --limit-iface-in -j ACCEPT
      else
        echo "chalkos-vxlan-rule: $1 holds no address; VXLAN stays refused" >&2
        status=1
      fi
    fi
    for family in "''${families[@]}"; do
      "$family" -w -S chalkos-vxlan
    done
    exit "$status"
  '';
}
