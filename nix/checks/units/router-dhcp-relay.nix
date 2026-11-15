# The dhcp-relay example's dnsmasq relays, of the default VRF and of the VRF guest, between
# clients and Kea's servers behind the example's firewall. dnsmasq runs as root, the namespace's
# only user, with the units' capabilities alone; on a node it runs as the user dnsmasq.
{
  pkgs,
  lib,
  checkLib,
  routerFirewall,
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
  dir = router.chalkos.outputs.dhcp-relay.dir;
  udhcpc = "${pkgs.busybox}/bin/busybox udhcpc";
  # The DHCP relays' configs the dhcp-relay example renders.
  relays = "${checkLib.routerRendered "dhcp-relay"}/dhcp-relay";
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
  forwarding
  ip link add guest type vrf table 300
  ip link set guest up
  veth client lan0 - 192.0.2.1/24 2001:db8:1::1/64
  veth guest vlan20 guest 198.51.100.1/24
  veth server/s0 wan0 - 203.0.113.2/25 2001:db8:ff::2/64 -- 203.0.113.53/25 2001:db8:ff::53/64
  veth server/s1 vlan30 guest 203.0.113.130/25 -- 203.0.113.154/25
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

  mkdir -p ${dir} /var/run/dhcpcd /var/db/dhcpcd /var/lib/dhcpcd
  install -m 644 -t ${dir} ${relays}/dnsmasq-relay.conf ${relays}/dnsmasq-relay-guest.conf
  grep -qx 'dhcp-relay=192.0.2.1,203.0.113.53' ${dir}/dnsmasq-relay.conf \
    || fail "the dhcp-relay example renders no relay of the default VRF"
  grep -qx 'dhcp-relay=198.51.100.1,203.0.113.154' ${dir}/dnsmasq-relay-guest.conf \
    || fail "the dhcp-relay example renders no relay of the VRF guest"
  ${routerFirewall.load "dhcp-relay"}

  # relays: starts both relays, the one given first first, and checks that both run.
  # bindDevice binds the guest relay's sockets to the VRF's device, as BindNetworkInterface= does.
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

  ${routerFirewall.load "none"}
  : >kea-dhcp4.log
  got=$(leases)
  if [ -n "$got" ] || grep -q DHCP4_LEASE_ALLOC kea-dhcp4.log; then
    fail "the client took a lease through a firewall without the relay's openings: $got"
  fi
''
