# Fills the table chalkos-vxlan from the file the preparation writes, and empties it without one.
# The file has a line "destination <address> <interface|any>" for each of the node's addresses,
# at most one per family, and a line "source <range>" for each range VXLAN must come from; without
# those, any source will do. It fails, leaving the table empty, when the file holds anything else.
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

    # The table, replaced in one transaction, with the rules given. Its input chain runs before
    # the firewall's, which drops what it does not accept, and its clear chain after it, so the
    # mark never outlives the firewall: not on packets the firewall accepted before looking at it,
    # such as those on lo or of established connections, nor on the packets VXLAN carries.
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
      echo "  chain clear {"
      echo "    type filter hook input priority filter + 1; policy accept;"
      echo "    meta mark & ${mark} == ${mark} meta mark set meta mark ^ ${mark}"
      echo "  }"
      echo "}"
    }
    # Should anything fail before the table is written, reading the file for one, it is emptied,
    # or removed should that fail too: no rule of an earlier run stays.
    written=false
    trap '"$written" || table | nft -f - || nft delete table inet chalkos-vxlan' EXIT

    # Whether the string is four decimal octets. Leading zeros are refused: nft may read them as
    # octal.
    ipv4() {
      [[ "$1" =~ ^(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})\.(0|[1-9][0-9]{0,2})$ ]] || return 1
      local octet
      for octet in "''${BASH_REMATCH[@]:1}"; do
        ((octet <= 255)) || return 1
      done
    }

    # Whether the string is an IPv6 address without a zone: eight groups of up to four hex digits,
    # the last two possibly written as an IPv4 address, or fewer around a single "::".
    ipv6() {
      local address=$1 group='[0-9A-Fa-f]{1,4}' part colons count=0
      local groups="^$group(:$group)*\$"
      if [[ "$address" == *.* ]]; then
        ipv4 "''${address##*:}" || return 1
        address="''${address%:*}:0:0"
      fi
      case "$address" in
        *::*::*)
          return 1
          ;;
        *::*)
          for part in "''${address%%::*}" "''${address#*::}"; do
            if [ -n "$part" ]; then
              [[ "$part" =~ $groups ]] || return 1
              colons=''${part//[^:]/}
              count=$((count + ''${#colons} + 1))
            fi
          done
          ((count <= 7))
          ;;
        *)
          [[ "$address" =~ $groups ]] || return 1
          colons=''${address//[^:]/}
          ((''${#colons} == 7))
          ;;
      esac
    }

    # The nft family of a bare address, or nothing: nft would resolve anything else, such as
    # cafe.be, as a host name.
    family_of() {
      if ipv4 "$1"; then
        echo ip
      elif ipv6 "$1"; then
        echo ip6
      fi
    }

    rules=()
    status=0
    if [ "$#" -eq 1 ] && [ -e "$1" ]; then
      mapfile -t lines <"$1"
      # Anything but these lines would end up in the ruleset, so every line is checked before any
      # rule is written.
      declare -A addresses=() modes=() sources=()
      for line in "''${lines[@]}"; do
        read -r kind address mode rest <<<"$line" || true
        if [ "$kind" = source ] && [ -z "$mode" ] && [ "$line" = "source $address" ]; then
          # A range: an address and a prefix length no longer than the family's addresses.
          length=''${address##*/}
          family=$(family_of "''${address%/*}")
          longest=128
          if [ "$family" = ip ]; then
            longest=32
          fi
          if [ -n "$family" ] && [[ "$address" == */* ]] && [[ "$length" =~ ^(0|[1-9][0-9]{0,2})$ ]] &&
            ((length <= longest)) && [[ " ''${families[*]} " == *" $family "* ]]; then
            sources[$family]+="''${sources[$family]:+, }$address"
            continue
          fi
          status=1
          break
        fi
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
          # With source ranges a family without one takes no VXLAN. The ranges limit who may send
          # VXLAN; within them a source may be forged. The kernel drops an IPv4 packet with a
          # source of the node's own arriving from the network (accept_local is off), but IPv6
          # has no such check.
          if [ "''${#sources[@]}" -gt 0 ]; then
            if [ -z "''${sources[$family]:-}" ]; then
              continue
            fi
            rule="$family saddr { ''${sources[$family]} } $rule"
          fi
          if [ "''${modes[$family]}" = interface ]; then
            # An address on a network interface takes VXLAN on that interface alone.
            rule+=" fib daddr . iif type local"
          else
            # One on a loopback or dummy interface, which routers reach through the node's other
            # interfaces, takes it on any but those of the pod network and kube-proxy, where pods
            # could send it. The prefixes are kubernetesInterfaces' in
            # pkg/kubernetes/nodeip/nodeip.go; keep them in sync.
            rule+=' iifname != { "cni*", "flannel*", "kube-*", "veth*" }'
          fi
          rules+=("$rule meta mark set meta mark | ${mark}")
        done
      else
        echo "chalkos-vxlan-rule: $1 does not hold the node's addresses; VXLAN stays refused" >&2
      fi
    fi
    # A ruleset nft refuses changes nothing, so the old rules go then.
    if ! table "''${rules[@]}" | nft -f -; then
      table | nft -f -
      status=1
    fi
    written=true
    nft list table inet chalkos-vxlan
    exit "$status"
  '';
}
