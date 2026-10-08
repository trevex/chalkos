# Fills the table chalkos-vxlan from the file the preparation writes, and empties it without one.
# The file has a line "destination <address> <interface|any>" for each of the node's addresses,
# at most one per family. It fails, leaving the table empty, when the file holds anything else.
#
# The NixOS firewall drops what its own input chain does not accept, whatever other tables do, so
# the table only marks VXLAN packets sent to the node's addresses, and the firewall accepts
# packets carrying the mark (see kubernetes.nix). The table is not the firewall's: reloading the
# firewall leaves it alone.
{
  lib,
  writeShellApplication,
  util-linux,
  nftables,
  ipv6 ? true,
  lockFile ? "/run/chalkos-vxlan-rule.lock",
  # The packet mark bit the firewall accepts.
  mark,
}:
writeShellApplication {
  name = "chalkos-vxlan-rule";
  runtimeInputs = [
    nftables
    util-linux
  ];
  text = ''
    # The preparation runs this before and after it picks the node's addresses: one run at a time,
    # so the last one sees the file as it is now.
    exec 9>>${lib.escapeShellArg lockFile}
    flock 9
    families=(ip ${lib.optionalString ipv6 "ip6"})

    # The nft family of a bare address, or nothing. An IPv4 address is four decimal octets, an
    # IPv6 one has a colon: nft would resolve anything else, such as cafe.be, as a host name.
    family_of() {
      case "$1" in
        *:*)
          if [[ "$1" =~ ^[0-9A-Fa-f.:]+$ ]]; then
            echo ip6
          fi
          ;;
        *)
          if [[ "$1" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]]; then
            for octet in "''${BASH_REMATCH[@]:1}"; do
              if ((10#$octet > 255)); then
                return
              fi
            done
            echo ip
          fi
          ;;
      esac
    }

    rules=()
    status=0
    if [ "$#" -eq 1 ] && [ -e "$1" ]; then
      mapfile -t lines <"$1"
      # Anything but these lines would end up in the ruleset, so every line is checked before any
      # rule is written.
      declare -A addresses=() modes=()
      for line in "''${lines[@]}"; do
        read -r kind address mode rest <<<"$line" || true
        family=$(family_of "$address")
        if [ "$kind" != destination ] || [ -z "$family" ] || [ -n "$rest" ] ||
          [ -n "''${addresses[$family]:-}" ] || ! [[ " ''${families[*]} " == *" $family "* ]] ||
          ! [[ "$mode" == interface || "$mode" == any ]] || [ "$line" != "$kind $address $mode" ]; then
          status=1
          break
        fi
        addresses[$family]=$address
        modes[$family]=$mode
      done
      if [ "''${#addresses[@]}" -eq 0 ]; then
        status=1
      fi
      if [ "$status" -eq 0 ]; then
        for family in "''${!addresses[@]}"; do
          rule="$family daddr ''${addresses[$family]} udp dport 8472"
          # An address on a network interface takes VXLAN on that interface alone. One on a
          # loopback or dummy interface, which routers reach through the node's other interfaces,
          # takes it on any.
          if [ "''${modes[$family]}" = interface ]; then
            rule+=" fib daddr . iif type local"
          fi
          rules+=("$rule meta mark set meta mark | ${mark}")
        done
      else
        echo "chalkos-vxlan-rule: $1 does not hold the node's addresses; VXLAN stays refused" >&2
      fi
    fi
    # The table is replaced in one transaction. It runs before the firewall's input chain, which
    # drops what it does not accept.
    table() {
      echo "table inet chalkos-vxlan"
      echo "delete table inet chalkos-vxlan"
      echo "table inet chalkos-vxlan {"
      echo "  chain input {"
      echo "    type filter hook input priority filter - 1; policy accept;"
      for rule in "$@"; do
        echo "    $rule"
      done
      echo "  }"
      echo "}"
    }
    # A ruleset nft refuses changes nothing, so the old rules go then.
    if ! table "''${rules[@]}" | nft -f -; then
      table | nft -f -
      status=1
    fi
    nft list table inet chalkos-vxlan
    exit "$status"
  '';
}
