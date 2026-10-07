# Fills the chalkos-vxlan chain from the file holding the node's addresses, one per line and at
# most one per family, and empties it without one. It fails, leaving the chain empty, when the
# file holds anything else.
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
    # The firewall runs this whenever it starts, also while the node picks its addresses: one run
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
      # Anything but bare addresses would end up among iptables's arguments, so every line is
      # checked before any rule is added.
      declare -A addresses=()
      for line in "''${lines[@]}"; do
        family=iptables
        case "$line" in
          *:*) family=ip6tables ;;
        esac
        if ! [[ "$line" =~ ^[0-9A-Fa-f.:]+$ ]] || [ -n "''${addresses[$family]:-}" ] ||
          ! [[ " ''${families[*]} " == *" $family "* ]]; then
          status=1
          break
        fi
        addresses[$family]=$line
      done
      if [ "''${#addresses[@]}" -eq 0 ]; then
        status=1
      fi
      if [ "$status" -eq 0 ]; then
        for family in "''${!addresses[@]}"; do
          "$family" -w -A chalkos-vxlan -p udp --dport 8472 -d "''${addresses[$family]}" \
            -m addrtype --dst-type LOCAL --limit-iface-in -j ACCEPT
        done
      else
        echo "chalkos-vxlan-rule: $1 holds no address per family; VXLAN stays refused" >&2
      fi
    fi
    for family in "''${families[@]}"; do
      "$family" -w -S chalkos-vxlan
    done
    exit "$status"
  '';
}
