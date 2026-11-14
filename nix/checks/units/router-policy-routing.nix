# The policy example's ruleset in network namespaces: LAN peers on lan0 and lan1, uplinks on
# wana0, which the main table routes, and wanb0, and a transit peer on vt0 in the VRF transit of
# table 100. The router holds the kernel's rules and the next hops' tables as networkd installs
# them from the units the example renders, and transit's table the routes of the example's
# Networks of lan0 and lan1. Each uplink and transit answer on the same far address with their
# name, so a probe tells which way the router sent it. Guests' HTTPS takes transit and their
# other ports the main table, DSCP 46 takes transit in both families, ahead of the next hops the
# servers' sources take in each family, traffic between lan0 and lan1 the main table ahead of
# those next hops, and other traffic the main table. A rule without in that sends BGP to the
# main table leaves the router's BGP in transit alone in both families. While wanb0 is down the next hops' traffic is dropped, not routed by the main table,
# and once networkd's route is back it takes wanb0 again.
{
  checkLib,
  unitCheck,
  ...
}:
let
  inherit (checkLib.router.config.systemd.services.chalkos-firewall.serviceConfig) ExecStart;
  dir = checkLib.router.config.chalkos.outputs.router-nftables.dir;
  inherit (checkLib) routerRendered;
in
unitCheck "router-policy-routing" checkLib.networkTools ''
  debug() {
    nft list ruleset >&2
    ip -4 rule show >&2
    ip -6 rule show >&2
  }
  ip link set lo up
  netns lan lan1 wana wanb transit
  on() {
    local ns=$1
    shift
    nsenter -t "''${pids[$ns]}" -n "$@"
  }
  ip link add transit type vrf table 100
  ip link set transit up
  # link <namespace> <router's link> <router's IPv4> <router's IPv6> <peer's IPv4> <peer's IPv6>:
  # a veth pair whose peer routes everything through the router and forwards nothing, so a
  # packet the router sends the wrong way does not come back to it.
  link() {
    ip link add "$2" type veth peer name eth0 netns "''${pids[$1]}"
    if [ "$2" = vt0 ]; then ip link set vt0 master transit; fi
    ip addr add "$3" dev "$2"
    ip addr add "$4" dev "$2" nodad
    ip link set "$2" up
    on "$1" ip link set lo up
    on "$1" sh -c "echo 0 >/proc/sys/net/ipv4/ip_forward; echo 0 >/proc/sys/net/ipv6/conf/all/forwarding"
    on "$1" ip addr add "$5" dev eth0
    on "$1" ip addr add "$6" dev eth0 nodad
    on "$1" ip link set eth0 up
    on "$1" ip route add default via "''${3%/*}"
    on "$1" ip route add default via "''${4%/*}"
  }
  link lan lan0 192.0.2.1/25 2001:db8:1::1/64 192.0.2.2/25 2001:db8:1::2/64
  link lan1 lan1 192.0.2.129/25 2001:db8:2::1/64 192.0.2.130/25 2001:db8:2::2/64
  link wana wana0 198.51.100.1/25 2001:db8:a::1/64 198.51.100.2/25 2001:db8:a::2/64
  link wanb wanb0 198.51.100.129/25 2001:db8:b::1/64 198.51.100.130/25 2001:db8:b::2/64
  link transit vt0 203.0.113.1/24 2001:db8:c::1/64 203.0.113.2/24 2001:db8:c::2/64
  # The LAN's guest and server hosts.
  on lan ip addr add 192.0.2.70/25 dev eth0
  on lan ip addr add 192.0.2.33/25 dev eth0
  on lan ip addr add 2001:db8:1::21/64 dev eth0 nodad
  # The far address, which each uplink and transit answer on.
  for ns in wana wanb transit; do
    on "$ns" ip addr add 198.18.0.1/32 dev lo
    on "$ns" ip addr add fd00:ff::1/128 dev lo
  done
  # The main table, as 20-wana's gateways give it, and transit's, as a BGP session in transit
  # and 10-lan's and 11-lan1's routes of table 100 give it.
  ip route add default via 198.51.100.2
  ip route add default via 2001:db8:a::2
  ip route add default via 203.0.113.2 table 100
  ip route add default via 2001:db8:c::2 table 100
  ip route add 192.0.2.0/25 dev lan0 table 100
  ip route add 2001:db8:1::/64 dev lan0 table 100
  ip route add 192.0.2.128/25 dev lan1 table 100
  ip route add 2001:db8:2::/64 dev lan1 table 100
  for family in -4 -6; do
    ip "$family" route add unreachable default metric 4278198272 table 100
  done
  # The units' rules and the next hops' tables, as networkd installs them: a rule per
  # [RoutingPolicyRule] of the units, in both families.
  awk -F= '/^\[RoutingPolicyRule\]/ { rule = 1; next } /^\[/ { rule = 0 }
    rule && $1 == "FirewallMark" { mark = $2 } rule && $1 == "Table" { print mark, $2 }' \
    ${routerRendered "policy"}/networkd/* >rules
  [ "$(wc -l <rules)" = 4 ] || fail "the example's units hold $(wc -l <rules) RoutingPolicyRules, not four"
  while read -r mark table; do
    for family in -4 -6; do
      ip "$family" rule add fwmark "$mark" lookup "$table" priority 900 protocol static
    done
  done <rules
  hops() {
    ip route add default via 198.51.100.130 dev wanb0 table 200 proto static
    ip -6 route add default via 2001:db8:b::2 dev wanb0 table 201 proto static
  }
  hops
  for table in 200 201; do
    for family in -4 -6; do
      ip "$family" route add unreachable default metric 4278198272 table "$table" proto static
    done
  done
  echo 1 >/proc/sys/net/ipv4/ip_forward
  echo 1 >/proc/sys/net/ipv6/conf/all/forwarding
  # The firewall's reverse-path filter is the only one, as router-sysctl sets it.
  for conf in /proc/sys/net/ipv4/conf/*/rp_filter; do
    echo 0 >"$conf"
  done

  for ns in wana wanb transit; do
    for port in 80 443; do
      on "$ns" socat TCP4-LISTEN:$port,fork,reuseaddr EXEC:"echo $ns" &
      on "$ns" socat TCP6-LISTEN:$port,fork,reuseaddr,ipv6only=1 EXEC:"echo $ns" &
    done
  done
  on lan1 socat TCP4-LISTEN:80,fork,reuseaddr EXEC:"echo lan1" &
  on lan1 socat TCP6-LISTEN:80,fork,reuseaddr,ipv6only=1 EXEC:"echo lan1" &
  # The router's BGP in transit, which listens on the VRF's device.
  socat TCP4-LISTEN:179,fork,reuseaddr,so-bindtodevice=transit EXEC:"echo router" &
  socat TCP6-LISTEN:179,fork,reuseaddr,ipv6only=1,so-bindtodevice=transit EXEC:"echo router" &
  listening() { [ "$(on wana ss -Hltn | wc -l)" = 4 ] && [ "$(on wanb ss -Hltn | wc -l)" = 4 ] && [ "$(on transit ss -Hltn | wc -l)" = 4 ] && [ "$(on lan1 ss -Hltn | wc -l)" = 2 ] && [ "$(ss -Hltn | wc -l)" = 2 ]; }
  retry 100 "the listeners did not start" listening

  # settled waits until no link holds a tentative IPv6 address, as neighbour discovery waits
  # for the links' link-local addresses.
  untentative() { [ -z "$(ip -6 addr show tentative)$(for ns in lan lan1 wana wanb transit; do on "$ns" ip -6 addr show tentative; done)" ]; }
  settled() { retry 100 "the links' IPv6 addresses stayed tentative" untentative; }
  settled
  mkdir -p ${dir}
  install -m 644 ${routerRendered "policy"}/router-nftables/ruleset.nft ${dir}/ruleset.nft
  ${ExecStart} || fail "chalkos-firewall did not load the policy example's ruleset"

  # way <source> <port> [<socat options>]: the name of the peer that answered a connection
  # from the LAN host's source to the far address, or to $far when it is set, or none.
  way() {
    local to=TCP4:''${far:-198.18.0.1} source=$1
    case $1 in *:*)
      to="TCP6:[''${far:-fd00:ff::1}]"
      source="[$1]"
      ;;
    esac
    on lan socat -t 2 - "$to:$2,bind=$source,connect-timeout=1''${3:+,$3}" </dev/null 2>/dev/null || echo none
  }
  takes() {
    local got
    got=$(way "$2" "$3" "''${4:-}")
    [ "$got" = "$1" ] || fail "$2 to port $3''${4:+ with $4} went to $got, not $1: $5"
  }
  takes wana 192.0.2.2 80 "" "no rule matches it"
  takes wana 2001:db8:1::2 80 "" "no rule matches it"
  takes transit 192.0.2.70 443 "" "a guest's HTTPS takes transit"
  takes wana 192.0.2.70 80 "" "a guest's other ports take the main table"
  takes wana 192.0.2.2 443 "" "HTTPS of a host that is no guest takes the main table"
  takes transit 192.0.2.2 80 ip-tos=184 "DSCP 46 takes transit"
  takes transit 2001:db8:1::2 80 ipv6-tclass=184 "DSCP 46 takes transit"
  takes wanb 192.0.2.33 80 "" "a server takes the next hop on wanb0"
  takes wanb 2001:db8:1::21 80 "" "a server takes the next hop on wanb0"
  takes transit 192.0.2.33 80 ip-tos=184 "the first rule that matches decides, and the documents apply by name"
  far=192.0.2.130 takes lan1 192.0.2.33 80 "" "traffic between the LAN's links stays in the main table"
  far=2001:db8:2::2 takes lan1 2001:db8:1::21 80 "" "traffic between the LAN's links stays in the main table"

  # A rule of a document without in, which the example lacks, that sends BGP to the main
  # table, and BGP, which the example's firewall keeps closed. The router's own addresses in
  # transit take no policy: their local routes are in table 100, past which the mark's rule
  # would send the packet to the main table's default.
  nft 'add rule inet chalkos policy-routing tcp dport 179 counter meta mark set meta mark & 0xff00ffff | 0x00040000 return comment "bgp 1"'
  nft 'add rule inet chalkos input tcp dport 179 accept'
  for to in TCP4:203.0.113.1 'TCP6:[2001:db8:c::1]'; do
    got=$(on transit socat -t 2 - "$to:179,connect-timeout=1" </dev/null 2>/dev/null || echo none)
    [ "$got" = router ] || fail "transit's peer reached $got, not the router's BGP on $to, past a policy without in"
  done

  # The kernel removes a next hop's route when its link goes down; the table's unreachable
  # default drops the traffic then, where the main table would route it.
  ip link set wanb0 down
  takes none 192.0.2.33 80 "" "the next hop's traffic is dropped while its link is down"
  takes none 2001:db8:1::21 80 "" "the next hop's traffic is dropped while its link is down"
  # networkd configures the link again once it is up: its IPv6 address, which the kernel
  # removed, and the routes of its Network.
  ip link set wanb0 up
  ip -6 addr add 2001:db8:b::1/64 dev wanb0 nodad
  settled
  hops
  takes wanb 192.0.2.33 80 "" "the next hop takes its traffic again once its route is back"
  takes wanb 2001:db8:1::21 80 "" "the next hop takes its traffic again once its route is back"
''
