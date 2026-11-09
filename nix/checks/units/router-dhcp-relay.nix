# The dhcp-relay example's relays, as dnsmasq-relay.service and dnsmasq-relay@guest.service run
# them, behind the example's firewall, which opens DHCPv4 and DHCPv6 on lan0 and DHCPv4 on
# vlan20, the relays' links. Both relay DHCPv4, the office's in the default VRF and the guests'
# in the VRF guest, whose sockets a library preloaded into dnsmasq binds to the VRF's device as
# BindNetworkInterface= does; they bind beside each other in either order. A client on lan0 takes
# an IPv4 and an IPv6 address and a client on vlan20 an IPv4 address from Kea's servers in a
# namespace behind wan0 and vlan30, which pick the subnets by the relays' addresses on the links
# and answer the relays, which the firewall lets in as replies to their requests. A firewall
# without the openings keeps the client on lan0 from both families. dnsmasq runs with the
# units' capabilities alone, as root, the namespace's only user, which it stays with --user=root;
# on a node it runs as the user dnsmasq with the same capabilities.
{
  pkgs,
  lib,
  checkLib,
  unitCheck,
  boundTo,
  bindDevice,
  ...
}:
let
  router = checkLib.router.config;
  inherit (router.systemd.services.dnsmasq-relay.serviceConfig) ExecStart;
  relayCaps = boundTo router.systemd.services.dnsmasq-relay.serviceConfig.CapabilityBoundingSet;
  vrfStart =
    lib.replaceStrings [ "%I" ] [ "guest" ]
      router.systemd.services."dnsmasq-relay@".serviceConfig.ExecStart;
  kea = lib.head (lib.splitString "/bin/" router.systemd.services.kea-dhcp4.serviceConfig.ExecStart);
  firewall = router.systemd.services.chalkos-firewall.serviceConfig.ExecStart;
  dir = router.chalkos.outputs.dhcp-relay.dir;
  nftDir = router.chalkos.outputs.router-nftables.dir;
  udhcpc = "${pkgs.busybox}/bin/busybox udhcpc";
  goldens = ../../../cmd/chalkos-render/testdata/router;
  tools = checkLib.networkTools ++ [
    pkgs.dhcpcd
    pkgs.procps
  ];
in
unitCheck "router-dhcp-relay" tools ''
  debug() {
    cat relay*.log kea-*.log >&2
  }
  ip link set lo up
  netns client guest server
  echo 1 >/proc/sys/net/ipv4/ip_forward
  echo 1 >/proc/sys/net/ipv6/conf/all/forwarding
  ip link add guest type vrf table 300
  ip link set guest up
  ip link add lan0 type veth peer name eth0 netns "''${pids[client]}"
  ip link add vlan20 type veth peer name eth0 netns "''${pids[guest]}"
  ip link add wan0 type veth peer name s0 netns "''${pids[server]}"
  ip link add vlan30 type veth peer name s1 netns "''${pids[server]}"
  ip link set vlan20 master guest
  ip link set vlan30 master guest
  for l in lan0 wan0; do echo 0 >"/proc/sys/net/ipv6/conf/$l/accept_dad"; done
  ip addr add 192.0.2.1/24 dev lan0
  ip addr add 2001:db8:1::1/64 dev lan0 nodad
  ip addr add 198.51.100.1/24 dev vlan20
  ip addr add 203.0.113.2/25 dev wan0
  ip addr add 2001:db8:ff::2/64 dev wan0 nodad
  ip addr add 203.0.113.130/25 dev vlan30
  for l in lan0 vlan20 wan0 vlan30; do ip link set "$l" up; done
  for c in on_client on_guest; do
    $c ip link set lo up
    $c sh -c 'echo 0 >/proc/sys/net/ipv6/conf/eth0/accept_dad'
    $c ip link set eth0 up
  done
  on_server ip link set lo up
  on_server sh -c 'echo 0 >/proc/sys/net/ipv6/conf/s0/accept_dad'
  on_server ip addr add 203.0.113.53/25 dev s0
  on_server ip addr add 2001:db8:ff::53/64 dev s0 nodad
  on_server ip addr add 203.0.113.154/25 dev s1
  on_server ip link set s0 up
  on_server ip link set s1 up
  on_server ip route add 192.0.2.0/24 via 203.0.113.2
  on_server ip route add 2001:db8:1::/64 via 2001:db8:ff::2
  on_server ip route add 198.51.100.0/24 via 203.0.113.130

  # The central servers: Kea's DHCPv4 server on UDP sockets, as relayed requests come by
  # unicast, and its DHCPv6 server on s0's address, which relays send to.
  mkdir -p kea4 kea6 /var/lib
  mkdir -m 0750 /var/lib/kea
  cat >kea-dhcp4.json <<'JSON'
  {"Dhcp4": {"interfaces-config": {"interfaces": ["s0", "s1"], "dhcp-socket-type": "udp", "service-sockets-max-retries": 100, "service-sockets-retry-wait-time": 100},
    "lease-database": {"type": "memfile", "persist": false},
    "subnet4": [{"id": 1, "subnet": "192.0.2.0/24", "pools": [{"pool": "192.0.2.100 - 192.0.2.199"}], "option-data": [{"name": "routers", "data": "192.0.2.1"}]},
                {"id": 2, "subnet": "198.51.100.0/24", "pools": [{"pool": "198.51.100.100 - 198.51.100.199"}]}]}}
  JSON
  cat >kea-dhcp6.json <<'JSON'
  {"Dhcp6": {"interfaces-config": {"interfaces": ["s0/2001:db8:ff::53"], "service-sockets-max-retries": 100, "service-sockets-retry-wait-time": 100},
    "lease-database": {"type": "memfile", "persist": false},
    "subnet6": [{"id": 1, "subnet": "2001:db8:1::/64", "pools": [{"pool": "2001:db8:1::100 - 2001:db8:1::1ff"}]}]}}
  JSON
  on_server env KEA_LOCKFILE_DIR=none KEA_PIDFILE_DIR="$PWD/kea4" ${kea}/bin/kea-dhcp4 -c kea-dhcp4.json >kea-dhcp4.log 2>&1 &
  on_server env KEA_LOCKFILE_DIR=none KEA_PIDFILE_DIR="$PWD/kea6" ${kea}/bin/kea-dhcp6 -c kea-dhcp6.json >kea-dhcp6.log 2>&1 &

  mkdir -p ${dir} ${nftDir} /var/run/dhcpcd /var/db/dhcpcd /var/lib/dhcpcd
  for f in dnsmasq-relay.conf dnsmasq-relay-guest.conf; do
    awk -v f="# $f" '$0 == f { take = 1; next } /^# dnsmasq-relay/ { take = 0 } take' ${goldens}/dhcp-relay.relay >"${dir}/$f"
  done
  grep -qx 'dhcp-relay=192.0.2.1,203.0.113.53' ${dir}/dnsmasq-relay.conf || fail "the dhcp-relay example's golden holds no relay of the default VRF"
  grep -qx 'dhcp-relay=198.51.100.1,203.0.113.154' ${dir}/dnsmasq-relay-guest.conf || fail "the dhcp-relay example's golden holds no relay of the VRF guest"
  install -m 644 ${goldens}/dhcp-relay.nft ${nftDir}/ruleset.nft
  ${firewall} || fail "chalkos-firewall did not load the dhcp-relay example's ruleset"

  # relays: starts both relays, the one given first first, and checks that both run.
  relays() {
    pkill -x dnsmasq 2>/dev/null || true
    sleep 0.5
    for r in "$@"; do
      case $r in
      default) ${relayCaps} ${ExecStart} -d --user=root >relay.log 2>&1 & ;;
      guest) env BIND_DEVICE=guest LD_PRELOAD=${bindDevice} ${relayCaps} ${vrfStart} -d --user=root >relay-guest.log 2>&1 & ;;
      esac
      sleep 1
    done
    [ "$(pgrep -cx dnsmasq)" = 2 ] || fail "the relays of the default VRF and the VRF guest do not run beside each other, started $*"
  }
  # leases: the addresses the client takes through the relay, of both families.
  printf 'ipv6only\nnoipv6rs\nduid ll\ninterface eth0\n  ia_na 1\n' >dhcpcd.conf
  leases() {
    on_client ip addr flush dev eth0 scope global
    rm -f /var/lib/dhcpcd/*
    on_client ${udhcpc} -i eth0 -n -q -t 4 -T 1 -s /bin/true >/dev/null 2>&1 || true
    on_client timeout 10 dhcpcd -f "$PWD/dhcpcd.conf" -c /bin/true -B -1 -6 eth0 >dhcpcd.log 2>&1 || true
    on_client ip -o addr show dev eth0 scope global | awk '{print $4}' | sort | tr '\n' ' '
  }
  for order in "default guest" "guest default"; do
    # shellcheck disable=SC2086
    relays $order
    : >kea-dhcp4.log
    got=$(leases)
    echo "$got" | grep -q '2001:db8:1::1[0-9a-f][0-9a-f]/128' || fail "the client on lan0 took no IPv6 address through the relay: $got"
    # udhcpc with -s /bin/true sets no address; the server's lease says it got one.
    grep -q 'DHCP4_LEASE_ALLOC.*192.0.2.1[0-9][0-9]' kea-dhcp4.log || fail "the client on lan0 took no IPv4 lease through the relay"
    on_guest ${udhcpc} -i eth0 -n -q -t 4 -T 1 -s /bin/true >/dev/null 2>&1 || true
    grep -q 'DHCP4_LEASE_ALLOC.*198.51.100.1[0-9][0-9]' kea-dhcp4.log || fail "the client on vlan20 took no IPv4 lease through the VRF's relay"
  done

  install -m 644 ${goldens}/none.nft ${nftDir}/ruleset.nft
  ${firewall} || fail "chalkos-firewall did not load the none example's ruleset"
  : >kea-dhcp4.log
  got=$(leases)
  if [ -n "$got" ] || grep -q DHCP4_LEASE_ALLOC kea-dhcp4.log; then
    fail "the client took a lease through a firewall without the relay's openings: $got"
  fi
''
