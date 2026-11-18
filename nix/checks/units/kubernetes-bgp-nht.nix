# A numbered BGP session over networkd's SLAAC state: a noprefixroute address and a subnet
# route of protocol ra. The old connected-only NHT check refuses the neighbour although the
# kernel reaches it. The rendered fix establishes the session and installs its default route.
{
  pkgs,
  lib,
  unitCheck,
  ...
}:
let
  frr = pkgs.chalkos.frr;
  # Normalised documents as chalkd hands them to the renderer; BFD and authentication are
  # independent of neighbour route resolution and are left out here.
  input = pkgs.writeText "bgp-nht.json" (
    builtins.toJSON {
      node.name = "node";
      documents = {
        BGPSpeaker = [
          {
            metadata.name = "bgp";
            spec = {
              asn = 65001;
              routerID = "0.0.0.11";
              export.interfaces = [ "node0" ];
            };
          }
        ];
        BGPPeer = [
          {
            metadata.name = "router";
            spec = {
              asn = 65000;
              address = "fdc8:a1b2:c3d4:1::2";
              interfaces = [ "enp3s0" ];
              password = null;
              bfd = false;
              import = "default";
            };
          }
        ];
      };
    }
  );
  rendered = pkgs.runCommand "bgp-nht-config" { } ''
    OUT=$out ${lib.getExe pkgs.chalkos.chalkos-render} kubernetes-frr <${input}
  '';
  nodeVtysh = pkgs.writeShellScript "node-vtysh" ''
    exec ${frr}/bin/vtysh --config_dir /run/node --vty_socket /run/node "$@"
  '';
  routerConfig = pkgs.writeText "bgp-nht-router.conf" ''
    frr defaults traditional
    hostname router
    log stdout informational
    router bgp 65000
     bgp router-id 0.0.0.1
     no bgp default ipv4-unicast
     no bgp ebgp-requires-policy
     neighbor fdc8:a1b2:c3d4:1::11 remote-as 65001
     neighbor fdc8:a1b2:c3d4:1::11 passive
     neighbor fdc8:a1b2:c3d4:1::11 timers connect 1
     address-family ipv6 unicast
      neighbor fdc8:a1b2:c3d4:1::11 activate
      neighbor fdc8:a1b2:c3d4:1::11 default-originate
     exit-address-family
    exit
    end
  '';
in
unitCheck "kubernetes-bgp-nht"
  [
    pkgs.iproute2
    pkgs.util-linux
    pkgs.jq
    pkgs.iputils
    frr
  ]
  ''
    # FRR's compiled VTY group needs an entry; all daemons remain namespace root.
    printf 'root:x:0:\nfrrvty:x:0:\n' >group
    mount --bind "$PWD/group" /etc/group
    mkdir -p /run/node /run/router
    echo "service integrated-vtysh-config" >/run/node/vtysh.conf
    debug() {
      ip -6 address >&2
      ip -6 route >&2
      on_router ip -6 address >&2
      on_router ip -6 route >&2
      cat /run/node/*.log /run/router/*.log >&2 || true
      ${nodeVtysh} -c 'show bgp neighbors' >&2 || true
    }
    trap 'kill $(jobs -pr) 2>/dev/null || true' EXIT
    ip link set lo up
    netns router
    veth router enp3s0 - -- fdc8:a1b2:c3d4:1::2/64
    ip -6 address add fdc8:a1b2:c3d4:1::11/64 dev enp3s0 noprefixroute
    ip -6 route add fdc8:a1b2:c3d4:1::/64 dev enp3s0 proto ra metric 1024
    ip link add node0 type dummy
    ip link set node0 up
    ip -6 address add fdc8:a1b2:c3d4:ff::11/128 dev node0
    ip -6 address show dev enp3s0 | grep -q noprefixroute || fail "the fabric address has a prefix route"
    retry 3 "the kernel could not reach the router over the RA route" ping -6 -c 1 -W 2 fdc8:a1b2:c3d4:1::2

    # Each namespace has its own zebra and BGP daemon, with separate sockets and logs.
    sed '/ disable-connected-check$/d' ${rendered}/frr.conf >/run/node/frr.conf
    ${frr}/libexec/frr/zebra -u root -g root -f /dev/null -i /run/node/zebra.pid \
      -z /run/node/zserv.api --vty_socket /run/node --log stdout >/run/node/zebra.log 2>&1 &
    on_router ${frr}/libexec/frr/zebra -u root -g root -f /dev/null -i /run/router/zebra.pid \
      -z /run/router/zserv.api --vty_socket /run/router --log stdout >/run/router/zebra.log 2>&1 &
    retry 100 "node zebra did not start" test -S /run/node/zserv.api
    retry 100 "router zebra did not start" test -S /run/router/zserv.api
    start_node() {
      ${frr}/libexec/frr/bgpd -u root -g root -f /run/node/frr.conf -i /run/node/bgpd.pid \
        -z /run/node/zserv.api --vty_socket /run/node --log stdout >>/run/node/bgpd.log 2>&1 &
      node_pid=$!
    }
    start_node
    on_router ${frr}/libexec/frr/bgpd -u root -g root -f ${routerConfig} -i /run/router/bgpd.pid \
      -z /run/router/zserv.api --vty_socket /run/router --log stdout >/run/router/bgpd.log 2>&1 &
    unresolved() {
      ${nodeVtysh} -c 'show bgp neighbors fdc8:a1b2:c3d4:1::2' | grep -q 'No path to specified Neighbor'
    }
    retry 200 "the old config did not reproduce the NHT failure" unresolved
    [ -z "$(ip -6 route show default)" ] || fail "the old config installed a default route"

    # Start the same daemon on the actual rendered config. Reload parsing is covered by the
    # golden transition tests in pkg/frr; this check needs only zebra and bgpd, without mgmtd.
    kill "$node_pid"
    wait "$node_pid" || true
    cp ${rendered}/frr.conf /run/node/frr.conf
    start_node
    established() {
      ${nodeVtysh} -c 'show bgp neighbors fdc8:a1b2:c3d4:1::2' | grep -q 'BGP state = Established'
    }
    default_route() { ip -j -6 route show default | jq -e 'any(.[]; .protocol == "bgp")' >/dev/null; }
    advertised() {
      on_router ip -j -6 route show fdc8:a1b2:c3d4:ff::11/128 |
        jq -e 'any(.[]; .protocol == "bgp")' >/dev/null
    }
    retry 300 "the rendered config did not establish BGP over the RA route" established
    retry 100 "BGP did not install the default route" default_route
    retry 100 "the router did not learn the node's address" advertised

    ip link set enp3s0 down
    withdrawn() { [ -z "$(ip -6 route show default)" ]; }
    retry 100 "the default route remained after the fabric link went down" withdrawn
  ''
