# The router's firewall unit in network namespaces, with a LAN peer on lan0 and a WAN peer on
# wan0, each over a veth pair. Without a rendered ruleset chalkos-firewall loads the fallback,
# which accepts chalkd's port on every interface and forwards nothing. With the LAN example's
# ruleset, as the image renders it, the peers probe chalkd's port on the management interface
# alone, the protected ping past policies that drop everything else and on dmz0, an interface of
# no zone, an AddressSet, forwarding by zone, masquerade, destination NAT and the strict
# reverse-path filter. A reload replaces the table alone, in one transaction, and keeps it when
# nft refuses the new one. The leaf example, loaded as at boot, forwards everything. The VRF
# example's ruleset, with its links in VRFs as networkd leaves them and the routes its imports
# leak, forwards and masquerades in IPv4 from the zone of VRF tenant to that of VRF internet and
# nothing back, forwards nothing between mgmt and tenant in IPv6, whose routes the default VRF
# imports, drops the internet's connections to the router's other ports, opens the transit's BGP
# port in its VRF alone, masquerades and translates in guest, whose addresses the default VRF and
# internet also use, and keeps guest's connections in their conntrack zone, so that a guest host
# of a management host's address cannot pass as the management host's connection, as it does
# without the zone. The loose reverse-path filter drops a source of either family that only the
# default VRF routes, passes it once tenant's table routes it, and passes link-local sources in a
# VRF. The strict one passes the neighbour discovery and router solicitations of link-local
# sources in a VRF.
{
  pkgs,
  checkLib,
  unitCheck,
  ...
}:
let
  inherit (checkLib.router.config.systemd.services.chalkos-firewall.serviceConfig)
    ExecStart
    ExecReload
    ;
  dir = checkLib.router.config.chalkos.outputs.router-nftables.dir;
  inherit (checkLib) routerRendered;
  # The VRF example's ruleset with the strict reverse-path filter, a golden file of chalkos-render.
  strictVRF = ../../../cmd/chalkos-render/testdata/router/vrf-strict.nft;
  # Answers a connection with the address it came from.
  whoami = pkgs.writeShellScript "whoami" ''echo "$SOCAT_PEERADDR"'';
  # rdisc6 sends a router solicitation, radvd router advertisements.
  tools = checkLib.networkTools ++ [
    pkgs.iputils
    pkgs.ndisc6
    pkgs.radvd
  ];
in
unitCheck "router-firewall" tools ''
  debug() {
    nft list ruleset >&2
  }
  ip link set lo up
  netns lan wan
  ip link add lan0 type veth peer name eth0 netns "''${pids[lan]}"
  ip link add wan0 type veth peer name eth0 netns "''${pids[wan]}"
  ip addr add 192.0.2.1/24 dev lan0
  ip addr add 2001:db8:1::1/64 dev lan0 nodad
  ip addr add 198.51.100.1/24 dev wan0
  ip addr add 2001:db8:2::1/64 dev wan0 nodad
  ip link set lan0 up
  ip link set wan0 up
  # The LAN peer reaches the node over dmz0 too, an interface of no zone.
  ip link add dmz0 type veth peer name eth1 netns "''${pids[lan]}"
  ip addr add 203.0.113.1/24 dev dmz0
  ip link set dmz0 up
  on_lan ip link set lo up
  on_lan ip addr add 192.0.2.2/24 dev eth0
  on_lan ip addr add 2001:db8:1::2/64 dev eth0 nodad
  on_lan ip link set eth0 up
  on_lan ip addr add 203.0.113.2/24 dev eth1
  on_lan ip link set eth1 up
  on_lan ip route add default via 192.0.2.1
  on_lan ip route add default via 2001:db8:1::1
  # The WAN peer has an IPv6 route to the LAN and none in IPv4, where the router masquerades.
  on_wan ip link set lo up
  on_wan ip addr add 198.51.100.2/24 dev eth0
  on_wan ip addr add 2001:db8:2::2/64 dev eth0 nodad
  on_wan ip link set eth0 up
  on_wan ip route add 2001:db8:1::/64 via 2001:db8:2::1
  echo 1 >/proc/sys/net/ipv4/ip_forward
  echo 1 >/proc/sys/net/ipv6/conf/all/forwarding
  # The firewall's reverse-path filter is the only one, as router-sysctl sets it.
  for conf in all default lan0 wan0 dmz0; do
    echo 0 >/proc/sys/net/ipv4/conf/$conf/rp_filter
  done

  # Each listener holds a connection open for 5 s, so that a connection is established while
  # another tries the same addresses and ports.
  for port in 50000 22 8080; do
    socat TCP4-LISTEN:$port,fork,reuseaddr EXEC:"sleep 5" &
    socat TCP6-LISTEN:$port,fork,reuseaddr,ipv6only=1 EXEC:"sleep 5" &
  done
  for ns in on_lan on_wan; do
    $ns socat TCP4-LISTEN:80,fork,reuseaddr EXEC:${whoami} &
    $ns socat TCP6-LISTEN:80,fork,reuseaddr,ipv6only=1 EXEC:${whoami} &
  done
  listening() { [ "$(ss -Hltn | wc -l)" = 6 ] && [ "$(on_lan ss -Hltn | wc -l)" = 2 ] && [ "$(on_wan ss -Hltn | wc -l)" = 2 ]; }
  retry 100 "the listeners did not start" listening
  # reaches <namespace> <address> <port>: whether a connection from the namespace is accepted.
  reaches() { "$1" socat -u /dev/null "TCP:$2:$3,connect-timeout=1" 2>/dev/null; }
  refused() { if reaches "$@"; then fail "$1 reached $2 port $3 $where"; fi; }
  accepted() { reaches "$@" || fail "$1 did not reach $2 port $3 $where"; }
  # seen <namespace> <address> [<port>]: the address the listener of port 80 saw the connection
  # to the address and port, 80 unless given, come from.
  seen() { "$1" socat -t 2 - "TCP:$2:''${3:-80},connect-timeout=1" </dev/null 2>/dev/null; }
  pings() { "$1" ping -c 1 -W 1 "$2" >/dev/null || fail "$1 got no answer from $2 $where"; }

  # Counts what reaches the input hook from a spoofed source, and from the WAN peer's own.
  nft -f - <<'NFT'
  table inet count {
    chain input {
      type filter hook input priority filter - 10; policy accept;
      ip saddr 192.0.2.99 counter name spoofed
      ip saddr 198.51.100.2 icmp type echo-request counter name wan
    }
    counter spoofed {}
    counter wan {}
  }
  NFT

  where="with the fallback ruleset"
  ${ExecStart} || fail "chalkos-firewall did not load the fallback ruleset"
  accepted on_lan 192.0.2.1 50000
  accepted on_wan 198.51.100.1 50000
  accepted on_wan '[2001:db8:2::1]' 50000
  refused on_lan '[2001:db8:2::2]' 80
  pings on_wan 198.51.100.1

  where="with the LAN example's ruleset"
  mkdir -p ${dir}
  install -m 644 ${routerRendered "lan"}/router-nftables/ruleset.nft ${dir}/ruleset.nft
  ${ExecReload} || fail "chalkos-firewall did not reload the LAN ruleset"
  [ "$(nft list tables | grep -c 'table inet chalkos$')" = 1 ] || fail "the reload left two tables"
  nft list tables | grep -qx 'table inet count' || fail "the reload removed another table"
  accepted on_lan 192.0.2.1 50000
  accepted on_lan '[2001:db8:1::1]' 50000
  refused on_wan 198.51.100.1 50000
  refused on_wan '[2001:db8:2::1]' 50000
  # lan-local drops everything else, and the protected rules still answer pings.
  refused on_lan 192.0.2.1 22
  pings on_lan 192.0.2.1
  pings on_lan 2001:db8:1::1
  pings on_wan 198.51.100.1
  pings on_wan 2001:db8:2::1
  # The input chain drops what comes from an interface of no zone, past the protected rules.
  refused on_lan 203.0.113.1 22
  pings on_lan 203.0.113.1
  # wan-local accepts 8080 from the trusted AddressSet, an IPv6 address.
  accepted on_wan '[2001:db8:2::1]' 8080
  refused on_wan 198.51.100.1 8080
  # lan-wan forwards, masquerading IPv4; nothing forwards from the WAN to the LAN.
  [ "$(seen on_lan 198.51.100.2)" = 198.51.100.1 ] || fail "the LAN's IPv4 was not masqueraded"
  # socat writes an IPv6 peer in full, in brackets.
  [ "$(seen on_lan '[2001:db8:2::2]')" = '[2001:0db8:0001:0000:0000:0000:0000:0002]' ] || fail "the LAN's IPv6 did not reach the WAN unchanged"
  refused on_wan '[2001:db8:1::2]' 80
  # The destination NAT of 8081 reaches the LAN peer's port 80.
  [ "$(seen on_wan 198.51.100.1 8081)" = 198.51.100.2 ] || fail "the destination NAT did not reach the LAN"
  # The strict reverse-path filter drops a WAN packet from a LAN source before input.
  on_wan ip addr add 192.0.2.99/32 dev eth0
  on_wan ping -c 1 -W 1 -I 192.0.2.99 198.51.100.1 >/dev/null 2>&1 || true
  nft list counter inet count spoofed | grep -q 'packets 0 ' || fail "the reverse-path filter passed a spoofed source"
  if nft list counter inet count wan | grep -q 'packets 0 '; then fail "the WAN peer's pings did not reach input"; fi

  where="after nft refused a ruleset"
  echo 'table inet chalkos { chain input { bogus } }' >${dir}/ruleset.nft
  if ${ExecReload} 2>/dev/null; then fail "the reload of a broken ruleset succeeded"; fi
  refused on_wan 198.51.100.1 50000
  accepted on_lan 192.0.2.1 50000
  # At boot, a ruleset nft refuses fails the unit and leaves the fallback.
  if ${ExecStart} 2>/dev/null; then fail "chalkos-firewall loaded a broken ruleset"; fi
  accepted on_wan 198.51.100.1 50000

  where="with the leaf example's ruleset"
  install -m 644 ${routerRendered "leaf"}/router-nftables/ruleset.nft ${dir}/ruleset.nft
  # At boot, with no table loaded yet, chalkos-firewall loads the rendered ruleset.
  nft delete table inet chalkos
  ${ExecStart} || fail "chalkos-firewall did not load the leaf ruleset"
  accepted on_wan '[2001:db8:1::2]' 80
  refused on_lan 192.0.2.1 50000

  where="with the VRF example's ruleset"
  # The VRF example's links as networkd and FRR leave them: the VRFs of the example's tables,
  # each ending in unreachable default routes, guest's links with the addresses of eth0 and of
  # internet's eth1, and the routes internet's and tenant's imports leak. The earlier phases'
  # links go, as eth0 and eth3 take lan0's address.
  ip link del lan0
  ip link del wan0
  ip link del dmz0
  echo 0 >/proc/sys/net/ipv6/conf/default/accept_dad
  vrf() {
    ip link add "$1" type vrf table "$2"
    ip link set "$1" up
    ip -4 route add unreachable default metric 4278198272 table "$2"
    ip -6 route add unreachable default metric 4278198272 table "$2"
  }
  vrf internet 100
  vrf tenant 200
  vrf guest 300
  netns mgmt isp tenant guest gisp
  # vrflink <link> <VRF or -> <peer's namespace> <router's address> <peer's address>: a veth
  # pair to the peer's eth0 in a /24, the peer's default route through the router. Link-local
  # addresses are usable at once, without duplicate address detection.
  vrflink() {
    "on_$3" sh -c 'echo 0 >/proc/sys/net/ipv6/conf/default/accept_dad'
    ip link add "$1" type veth peer name eth0 netns "''${pids[$3]}"
    if [ "$2" != - ]; then ip link set "$1" master "$2"; fi
    ip addr add "$4/24" dev "$1"
    ip link set "$1" up
    "on_$3" ip link set lo up
    "on_$3" ip addr add "$5/24" dev eth0
    "on_$3" ip link set eth0 up
    "on_$3" ip route add default via "$4"
  }
  vrflink eth0 - mgmt 192.0.2.1 192.0.2.10
  vrflink eth1 internet isp 203.0.113.1 203.0.113.254
  vrflink eth2 tenant tenant 198.51.100.1 198.51.100.10
  vrflink eth3 guest guest 192.0.2.1 192.0.2.10
  vrflink eth4 guest gisp 203.0.113.1 203.0.113.254
  # IPv6 of mgmt, of internet's uplink and of tenant, in the range of the policy from-tenant.
  ip addr add 2001:db8:10::1/64 dev eth0 nodad
  ip addr add 2001:db8:1::1/64 dev eth1 nodad
  ip addr add 2001:db8:100::1/64 dev eth2 nodad
  on_mgmt ip addr add 2001:db8:10::10/64 dev eth0 nodad
  on_mgmt ip route add default via 2001:db8:10::1
  on_isp ip addr add 2001:db8:1::254/64 dev eth0 nodad
  on_isp ip route add default via 2001:db8:1::1
  on_tenant ip addr add 2001:db8:100::10/64 dev eth0 nodad
  on_tenant ip route add default via 2001:db8:100::1
  # What the imports leak: in IPv4 internet's uplink into tenant and tenant's LAN into
  # internet, in IPv6 tenant's LAN into the default VRF, as from-tenant accepts it.
  ip -4 route add 203.0.113.0/24 dev eth1 table 200
  ip -4 route add 198.51.100.0/24 dev eth2 table 100
  ip -6 route add 2001:db8:100::/64 dev eth2
  # guest's default route through its uplink's gateway, behind which lies 198.18.0.1.
  ip -4 route add default via 203.0.113.254 dev eth4 table 300
  on_gisp ip addr add 198.18.0.1/32 dev lo
  # Ranges the default VRF alone routes, which tenant's spoofed sources take.
  ip -4 route add 10.99.0.0/24 dev eth0
  ip -6 route add 2001:db8:99::/64 dev eth0
  for ns in on_isp on_tenant; do
    $ns socat TCP4-LISTEN:80,fork,reuseaddr EXEC:${whoami} &
    $ns socat TCP6-LISTEN:80,fork,reuseaddr,ipv6only=1 EXEC:${whoami} &
  done
  on_mgmt socat TCP6-LISTEN:80,fork,reuseaddr,ipv6only=1 EXEC:${whoami} &
  on_guest socat TCP4-LISTEN:80,fork,reuseaddr EXEC:${whoami} &
  on_gisp socat TCP4-LISTEN:80,fork,reuseaddr EXEC:${whoami} &
  # BGP's port in internet and in guest, whose uplink peer has the transit's address.
  socat TCP4-LISTEN:179,fork,reuseaddr,so-bindtodevice=internet /dev/null &
  socat TCP4-LISTEN:179,fork,reuseaddr,so-bindtodevice=guest /dev/null &
  vrfListening() {
    [ "$(ss -Hltn sport = :179 | wc -l)" = 2 ] && [ "$(on_isp ss -Hltn | wc -l)" = 2 ] && [ "$(on_tenant ss -Hltn | wc -l)" = 2 ] &&
      [ "$(on_mgmt ss -Hltn | wc -l)" = 1 ] && [ "$(on_guest ss -Hltn | wc -l)" = 1 ] && [ "$(on_gisp ss -Hltn | wc -l)" = 1 ]
  }
  retry 100 "the VRF phase's listeners did not start" vrfListening
  # Counts what the input chain accepted on port 22 and the forward chain between mgmt and
  # tenant, at a later priority, what reached input from tenant's spoofed sources, and the
  # forwarded packets of conntrack zone 1.
  nft -f - <<'NFT'
  table inet passed {
    counter mgmt22 {}
    counter guest22 {}
    counter internet22 {}
    counter mgmttenant {}
    counter spoofed {}
    counter zoned {}
    chain input {
      type filter hook input priority filter + 10; policy accept;
      iifname "eth0" tcp dport 22 counter name mgmt22
      iifname "guest" tcp dport 22 counter name guest22
      iifname "internet" tcp dport 22 counter name internet22
    }
    chain before {
      type filter hook input priority filter - 10; policy accept;
      ip saddr 10.99.0.99 icmp type echo-request counter name spoofed
      ip6 saddr 2001:db8:99::99 icmpv6 type echo-request counter name spoofed
    }
    chain forward {
      type filter hook forward priority filter + 10; policy accept;
      ct zone 1 counter name zoned
      iifname "eth0" oifname "eth2" counter name mgmttenant
      iifname "tenant" oifname "eth0" counter name mgmttenant
    }
  }
  NFT
  install -m 644 ${routerRendered "vrf"}/router-nftables/ruleset.nft ${dir}/ruleset.nft
  ${ExecReload} || fail "chalkos-firewall did not reload the VRF ruleset"
  none() { nft list counter inet passed "$1" | grep -q 'packets 0 '; }

  # tenant reaches the internet by the leaked routes, masqueraded out of internet; the
  # internet reaches neither the tenant nor the router's other ports, which the input chain
  # drops before a later chain counts them.
  [ "$(seen on_tenant 203.0.113.254)" = 203.0.113.1 ] || fail "tenant's IPv4 was not masqueraded out of internet"
  refused on_isp 198.51.100.10 80
  refused on_isp 203.0.113.1 22
  refused on_isp '[2001:db8:1::1]' 22
  none internet22 || fail "the input chain passed the internet's connections to port 22"
  # The default VRF routes tenant's IPv6, but no policy forwards between mgmt and tenant;
  # tenant's table holds no route to mgmt.
  refused on_mgmt '[2001:db8:100::10]' 80
  refused on_tenant '[2001:db8:10::10]' 80
  none mgmttenant || fail "the forward chain passed packets between mgmt and tenant"
  # The transit's BGP port opens in internet alone: guest's uplink peer has its address.
  accepted on_isp 203.0.113.1 179
  refused on_gisp 203.0.113.1 179
  # guest masquerades out of eth4 and forwards 8080 to its LAN, in its conntrack zone.
  [ "$(seen on_guest 203.0.113.254)" = 203.0.113.1 ] || fail "guest's IPv4 was not masqueraded out of eth4"
  [ "$(seen on_guest 198.18.0.1)" = 203.0.113.1 ] || fail "guest did not reach past its uplink's gateway"
  [ "$(seen on_gisp 203.0.113.1 8080)" = 203.0.113.254 ] || fail "guest's destination NAT did not reach its LAN"
  if none zoned; then fail "guest's connections are not in conntrack zone 1"; fi
  # mgmt holds a connection from port 40000 to the router's port 22. guest's host has mgmt's
  # address and tries the same ports: in zone 1 it is a new connection, which no policy
  # accepts, not a packet of mgmt's.
  connected() { [ -n "$(ss -Htn state established "( sport = :22 and dport = :$1 )")" ]; }
  established() { retry 50 "mgmt's connection from port $1 to port 22 was not established" connected "$1"; }
  on_mgmt socat -u EXEC:"sleep 3" TCP:192.0.2.1:22,sourceport=40000,connect-timeout=1 &
  established 40000
  on_guest socat -u /dev/null TCP:192.0.2.1:22,sourceport=40000,connect-timeout=1 2>/dev/null || true
  if none mgmt22; then fail "mgmt's connection to port 22 did not pass"; fi
  none guest22 || fail "guest's connection rode mgmt's through the input chain"
  # The loose reverse-path filter drops a source tenant's table does not route, which the
  # default VRF alone does, while tenant's own sources reach input, and passes link-local
  # sources in a VRF. Once tenant's table routes the source, its pings reach input.
  pings on_tenant 198.51.100.1
  pings on_tenant 2001:db8:100::1
  on_tenant ip addr add 10.99.0.99/32 dev eth0
  on_tenant ip addr add 2001:db8:99::99/128 dev eth0 nodad
  spoof() {
    on_tenant ping -c 1 -W 1 -I 10.99.0.99 198.51.100.1 >/dev/null 2>&1 || true
    on_tenant ping -c 1 -W 1 -I 2001:db8:99::99 2001:db8:100::1 >/dev/null 2>&1 || true
  }
  spoof
  none spoofed || fail "the reverse-path filter passed a source only the default VRF routes"
  ip -4 route add 10.99.0.0/24 dev eth2 table 200
  ip -6 route add 2001:db8:99::/64 dev eth2 table 200
  spoof
  [ "$(nft list counter inet passed spoofed | grep -o 'packets [0-9]*')" = 'packets 2' ] ||
    fail "the pings from sources tenant's table routes did not reach input, so the probe shows nothing"
  ip -4 route del 10.99.0.0/24 dev eth2 table 200
  ip -6 route del 2001:db8:99::/64 dev eth2 table 200
  ll=
  linkLocal() {
    read -r _ _ _ ll _ <<<"$(ip -6 -o addr show dev eth2 scope link)" || true
    [ -n "$ll" ]
  }
  retry 50 "eth2 has no link-local address" linkLocal
  pings on_tenant "''${ll%/*}%eth0"

  where="with the VRF example's ruleset and the strict reverse-path filter"
  # A VRF's link passes neighbour discovery through prerouting a second time, with the VRF's
  # device, where the fib check of a link-local source fails. tenant's host answers from an
  # address on its loopback, so it solicits the router from its link-local address, and the
  # router reaches that address through the host's link-local address, which advertises
  # itself from there. Its router solicitations and, from radvd, advertisements reach input.
  install -m 644 ${strictVRF} ${dir}/ruleset.nft
  ${ExecReload} || fail "chalkos-firewall did not reload the VRF ruleset with the strict filter"
  nft -f - <<'NFT'
  table inet solicited {
    counter rs {}
    counter ra {}
    chain input {
      type filter hook input priority filter - 10; policy accept;
      iifname { "eth2", "tenant" } icmpv6 type nd-router-solicit counter name rs
      iifname { "eth2", "tenant" } icmpv6 type nd-router-advert counter name ra
    }
  }
  NFT
  # counted <counter>: whether the counter of table solicited counts a packet within 10 s.
  counts() { nft list counter inet solicited "$1" | grep -q 'packets [1-9]'; }
  counted() { retry 100 "" counts "$1"; }
  read -r _ _ _ tll _ <<<"$(on_tenant ip -6 -o addr show dev eth0 scope link)"
  [ -n "$tll" ] || fail "tenant's eth0 has no link-local address"
  on_tenant ip addr add 2001:db8:ff::1/128 dev lo
  ip -6 route add 2001:db8:ff::/64 via "''${tll%/*}" dev eth2 table 200
  flush() {
    ip -6 neigh flush dev eth2
    on_tenant ip -6 neigh flush dev eth0
  }
  flush
  ping -c 1 -W 1 -I tenant 2001:db8:ff::1 >/dev/null ||
    fail "the router did not resolve tenant's host by its link-local address $where"
  flush
  on_tenant ping -c 1 -W 1 -I 2001:db8:ff::1 2001:db8:100::1 >/dev/null ||
    fail "tenant's host did not resolve the router from its link-local address $where"
  on_tenant rdisc6 -r 1 -w 200 eth0 >/dev/null 2>&1 || true
  counted rs || fail "tenant's router solicitation did not reach input $where"
  # radvd advertises at once when it starts, and wants forwarding on.
  printf 'interface eth0 {\n  AdvSendAdvert on;\n  MinRtrAdvInterval 3;\n  MaxRtrAdvInterval 4;\n};\n' >radvd.conf
  on_tenant sh -c 'echo 1 >/proc/sys/net/ipv6/conf/all/forwarding'
  on_tenant radvd -n -m stderr -C "$PWD/radvd.conf" -p "$PWD/radvd.pid" 2>/dev/null &
  radvd=$!
  counted ra || fail "tenant's router advertisement did not reach input $where"
  kill "$radvd"
  wait "$radvd" || true
  on_tenant sh -c 'echo 0 >/proc/sys/net/ipv6/conf/all/forwarding'
  on_tenant ip addr del 2001:db8:ff::1/128 dev lo
  ip -6 route del 2001:db8:ff::/64 table 200
  nft delete table inet solicited

  where="with the VRF example's ruleset without its conntrack zones"
  # The same probe without the zones: guest's host passes as mgmt's connection.
  sed '/^\tchain ct-\(prerouting\|output\) {$/,/^\t}$/d' ${routerRendered "vrf"}/router-nftables/ruleset.nft >${dir}/ruleset.nft
  if grep -q 'ct zone' ${dir}/ruleset.nft; then fail "the ruleset still sets conntrack zones"; fi
  ${ExecReload} || fail "chalkos-firewall did not reload the VRF ruleset without conntrack zones"
  on_mgmt socat -u EXEC:"sleep 3" TCP:192.0.2.1:22,sourceport=40001,connect-timeout=1 &
  established 40001
  on_guest socat -u /dev/null TCP:192.0.2.1:22,sourceport=40001,connect-timeout=1 2>/dev/null || true
  if none guest22; then fail "guest's connection did not ride mgmt's, so the probe shows nothing"; fi
''
