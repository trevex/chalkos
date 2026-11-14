# chalkos-nat64 in network namespaces, over the rendered instances of the edge's NAT64, of the
# default VRF with an explicit pool, and of the VRF example's guest, in VRF guest with an own
# pool. Jool's module does not load here, so a script over a file of instances is mounted over
# the image's jool; it keys them by the caller's network namespace, as Jool does. The unit gives
# each instance its namespace with forwarding off, its veth pair in its VRF, the addresses of its
# .veth and the default routes back, its instance in its namespace, and its prefix's and
# explicit pool's routes in its VRF's table. It removes an older image's instance of the
# router's own namespace with its unreachable route, a route of protocol 64 no file names, and
# the namespace, link and routes of a NAT64 that went. A second run changes nothing. With the VRF
# example's ruleset, a guest's packet to the NAT64 prefix reaches guest's namespace past policies
# and the conntrack zone, and a connection from the namespace's inner address leaves guest's
# uplink masqueraded to the uplink's address with its port. A link's address in an explicit
# pool fails the run before it changes anything. A VRF of another table than two files' fails
# the run after 30 s once, keeps the links out of it and keeps the instances' routes. A device
# of another kind of a link's name fails the run and stays. A
# link whose namespace went while a process held it is made anew. Table 300 has a name, which ip
# prints unless asked for numbers. A NAT64 moved into the default VRF with another prefix and a
# pool of one address leaves guest and table 300, its pool stays routed, and its instance is
# replaced. Stopping the unit removes every namespace, link and route of protocol 64, and
# starting it again brings them back.
{
  pkgs,
  lib,
  checkLib,
  unitCheck,
  ...
}:
let
  inherit (checkLib.router.config.systemd.services.chalkos-nat64.serviceConfig)
    ExecStart
    ExecReload
    ExecStop
    ;
  jool = lib.findFirst (
    p: lib.getName p == "jool-cli"
  ) (throw "chalkd's PATH holds no jool") checkLib.router.config.systemd.services.chalkd.path;
  dir = checkLib.router.config.chalkos.outputs.jool.dir;
  inherit (checkLib) routerRendered;
  # Answers a connection with the address and port it came from.
  whoami = pkgs.writeShellScript "whoami" ''echo "$SOCAT_PEERADDR:$SOCAT_PEERPORT"'';
in
unitCheck "router-nat64"
  (
    checkLib.networkTools
    ++ [
      pkgs.iputils
      pkgs.jq
      pkgs.gawk
      pkgs.gnugrep
      pkgs.diffutils
    ]
  )
  ''
    debug() {
      ip -d link show >&2
      ip netns list >&2
      ip route show table all >&2
      cat "$STATE" >&2
    }
    # iproute2 keeps named network namespaces in /var/run/netns, and a node's /var/run is /run.
    ln -s /run /var/run
    # A node may name a table, as guest's here, which ip prints in place of its number. /etc is
    # the build directory's etc.
    mkdir -p etc/iproute2
    printf '255 local\n254 main\n253 default\n300 guest-table\n' >etc/iproute2/rt_tables
    export STATE=$PWD/jool.state
    : >"$STATE"
    mkdir -p stub
    cat >stub/jool <<'STUB'
    #!${pkgs.runtimeShell}
    # jool's command line as chalkos-nat64 uses it, over $STATE: a line per instance of its
    # network namespace, name and prefix. Each call sees the instances of its own namespace,
    # and each is logged to $STATE.log.
    ns=$(readlink /proc/self/ns/net)
    echo "$ns $*" >>"$STATE.log"
    drop() { awk -v ns="$ns" -v name="$1" '$1 != ns || $2 != name' "$STATE" >"$STATE.new" && mv "$STATE.new" "$STATE"; }
    case "$1 $2" in
    "file check") exit 0 ;;
    "file handle")
      name=$(jq -r .instance "$3")
      drop "$name"
      echo "$ns $name $(jq -r .global.pool6 "$3")" >>"$STATE"
      exit 0
      ;;
    "instance display")
      awk '{ print $1 "," $2 ",netfilter" }' "$STATE"
      exit 0
      ;;
    esac
    if [ "$1" = -i ]; then
      name=$2
      shift 2
      line=$(awk -v ns="$ns" -v name="$name" '$1 == ns && $2 == name' "$STATE")
      if [ -z "$line" ]; then
        echo "Error: The kernel module returned error 3: This namespace lacks an instance named '$name'." >&2
        exit 3
      fi
      case "$*" in
      "global display --csv")
        echo "pool6,''${line##* }"
        exit 0
        ;;
      "global display")
        exit 0
        ;;
      "instance remove")
        drop "$name"
        exit 0
        ;;
      esac
    fi
    echo "jool stub: $*" >&2
    exit 2
    STUB
    chmod +x stub/jool
    # The unit's script puts the image's jool ahead of PATH; the check's user namespace owns a
    # mount namespace of its own, in which the stub takes jool's place.
    mount --bind stub/jool ${jool}/bin/jool
    inside() {
      local name=$1
      shift
      nsenter --net="/run/netns/nat64-$name" "$@"
    }
    # The instances' file's key of the namespace of the instance $1.
    nsof() { echo "net:[$(stat -L -c %i "/run/netns/nat64-$1")]"; }
    has() { awk -v ns="$1" -v name="$2" -v pool6="$3" '$1 == ns && $2 == name && $3 == pool6' "$STATE" | grep -q .; }
    # What the unit changes; the kernel's routes of link-local addresses come and go with
    # duplicate address detection.
    state() {
      ip -d link show
      ip -4 addr show
      ip -6 addr show scope global
      ip -4 route show table all
      ip -6 route show table all | grep -v "fe80::.* proto kernel"
      ip netns list
      sort "$STATE"
    }
    # Fails with the message $1 when the state differs from before.txt.
    unchanged() {
      state >now.txt
      diff before.txt now.txt >&2 || fail "$1"
    }

    ip link set lo up
    main=$(readlink /proc/self/ns/net)
    # An older image's instance of the router's own namespace with its unreachable route, a
    # route of protocol 64 no file names, the namespace of a NAT64 that went with its link and
    # a route through it, and the link of one whose namespace went.
    echo "$main edge 64:ff9b::/96" >>"$STATE"
    ip -6 route add unreachable 64:ff9b::/96 proto 64
    ip -4 route add unreachable 198.51.100.64/30 proto 64
    ip netns add nat64-old
    ip link add n64-old type veth peer name nat64 netns nat64-old
    ip addr add 10.98.0.1/30 dev n64-old
    ip link set n64-old up
    ip -4 route add 10.99.0.0/16 via 10.98.0.2 dev n64-old proto 64
    ip link add n64-gone type veth peer name gone
    # The VRF example's guest: its LAN eth3 and its uplink eth4, with a peer behind each.
    ip link add guest type vrf table 300
    ip link set guest up
    ip -4 route add unreachable default metric 4278198272 table 300
    ip -6 route add unreachable default metric 4278198272 table 300
    ip -4 route show table all | grep -q "table guest-table" || fail "ip does not name table 300"
    netns lan wan
    ip link add eth3 type veth peer name eth0 netns "''${pids[lan]}"
    ip link add eth4 type veth peer name eth0 netns "''${pids[wan]}"
    ip link set eth3 master guest
    ip link set eth4 master guest
    ip addr add 2001:db8:3::1/64 dev eth3 nodad
    ip addr add 203.0.113.1/24 dev eth4
    ip link set eth3 up
    ip link set eth4 up
    on_lan ip link set lo up
    on_lan ip addr add 2001:db8:3::2/64 dev eth0 nodad
    on_lan ip link set eth0 up
    on_lan ip route add default via 2001:db8:3::1
    on_wan ip link set lo up
    on_wan ip addr add 203.0.113.254/24 dev eth0
    on_wan ip link set eth0 up
    ip route add default via 203.0.113.254 table 300
    echo 1 >/proc/sys/net/ipv4/ip_forward
    echo 1 >/proc/sys/net/ipv6/conf/all/forwarding
    # The firewall's reverse-path filter is the only one, as router-sysctl sets it.
    for conf in all default; do
      echo 0 >/proc/sys/net/ipv4/conf/$conf/rp_filter
    done

    mkdir -p ${dir}
    install -m 644 -t ${dir} ${routerRendered "edge"}/jool/* ${routerRendered "vrf"}/jool/*
    ${ExecReload} || fail "chalkos-nat64 failed"
    for name in edge guest; do
      veth=${dir}/$name.veth
      outer=$(jq -r .outer "$veth")
      inner=$(jq -r .inner "$veth")
      table=$(jq -r .table "$veth")
      [ -e "/run/netns/nat64-$name" ] || fail "no namespace nat64-$name"
      ip -4 -o addr show dev "n64-$name" | grep -q "inet $outer " || fail "n64-$name lacks $outer"
      inside "$name" ip -4 -o addr show dev nat64 | grep -q "inet $inner " || fail "the inner end of $name lacks $inner"
      inside "$name" ip route show default | grep -q "via ''${outer%/*} dev nat64" || fail "the namespace of $name has no default route to $outer"
      inside "$name" ip -6 route show default | grep -q "via fe80::1 dev nat64" || fail "the namespace of $name has no IPv6 default route"
      ip -6 route show table "$table" proto 64 | grep -q "^64:ff9b::/96 via fe80::2 dev n64-$name" || fail "table $table does not route 64:ff9b::/96 to n64-$name"
      has "$(nsof "$name")" "$name" 64:ff9b::/96 || fail "no instance $name in its namespace"
      for f in ipv4/conf/all/forwarding ipv4/conf/default/forwarding ipv6/conf/all/forwarding ipv6/conf/default/forwarding; do
        [ "$(inside "$name" cat "/proc/sys/net/$f")" = 0 ] || fail "the namespace of $name forwards: $f"
      done
    done
    [ "$(ip -d -j link show n64-guest | jq -r '.[0].master')" = guest ] || fail "n64-guest is not in guest"
    [ "$(ip -d -j link show n64-edge | jq -r '.[0].master // "none"')" = none ] || fail "n64-edge is in a VRF"
    edge=$(jq -r .inner ${dir}/edge.veth)
    ip -4 route show proto 64 | grep -q "^192.0.2.252/30 via ''${edge%/*} dev n64-edge" || fail "the main table does not route the edge's pool to n64-edge"
    [ "$(ip -4 route show table all proto 64 | wc -l)" = 1 ] || fail "routes of protocol 64 beside the edge's pool: $(ip -4 route show table all proto 64)"
    [ "$(ip -6 route show table all proto 64 | wc -l)" = 2 ] || fail "IPv6 routes of protocol 64 beside the two prefixes: $(ip -6 route show table all proto 64)"
    if ip -6 route show table all proto 64 | grep -q unreachable; then fail "kept the older image's unreachable route"; fi
    if [ -e /run/netns/nat64-old ]; then fail "kept the namespace of a NAT64 that went"; fi
    if ip link show n64-old >/dev/null 2>&1; then fail "kept the link of a NAT64 that went"; fi
    if ip link show n64-gone >/dev/null 2>&1; then fail "kept the link of a NAT64 whose namespace went"; fi
    if awk -v ns="$main" '$1 == ns' "$STATE" | grep -q .; then fail "kept an instance of the router's own namespace"; fi

    # A namespace that forwards again is set back; nothing else changes.
    inside guest sh -c 'echo 1 >/proc/sys/net/ipv4/conf/all/forwarding; echo 1 >/proc/sys/net/ipv6/conf/all/forwarding'
    state >before.txt
    ${ExecReload} || fail "a second run failed"
    unchanged "a second run changed the links, routes or instances"
    [ "$(inside guest cat /proc/sys/net/ipv4/conf/all/forwarding)" = 0 ] || fail "a second run left guest's namespace forwarding IPv4"
    [ "$(inside guest cat /proc/sys/net/ipv6/conf/all/forwarding)" = 0 ] || fail "a second run left guest's namespace forwarding IPv6"

    # With the VRF example's ruleset, guest's NAT64 takes its LAN's traffic to the prefix and
    # leaves through its uplink from the uplink's address.
    nft -f ${routerRendered "vrf"}/router-nftables/ruleset.nft || fail "nft refused the VRF example's ruleset"
    inside guest nft -f - <<'NFT'
    table ip6 count {
      counter reached {}
      chain prerouting {
        type filter hook prerouting priority raw; policy accept;
        ip6 daddr 64:ff9b::/96 counter name reached
      }
    }
    NFT
    on_lan ping -c 1 -W 1 64:ff9b::cb00:71fe >/dev/null 2>&1 || true
    inside guest nft list counter ip6 count reached | grep -q "packets 1 " || fail "a guest's packet to the NAT64 prefix did not reach guest's namespace"
    on_wan socat TCP-LISTEN:80,fork,reuseaddr EXEC:${whoami} &
    serving() { on_wan ss -ltn | grep -q ':80 '; }
    retry 600 "the uplink peer's server did not start" serving
    inner=$(jq -r .inner ${dir}/guest.veth)
    got=$(inside guest socat -u "TCP4:203.0.113.254:80,bind=''${inner%/*}:61500,connect-timeout=2" -) ||
      fail "a connection from guest's namespace did not leave through guest's uplink"
    [ "$got" = 203.0.113.1:61500 ] || fail "guest's uplink peer saw $got, not the uplink's address with the NAT64's port"
    nft delete table inet chalkos

    # A link of the router holds an address of the edge's pool, as from DHCP: the run fails
    # before it changes anything, naming the NAT64, the entry and the link.
    ip addr add 192.0.2.253/24 dev eth4
    state >before.txt
    if ${ExecReload} 2>err.log; then fail "a run succeeded with a pool that holds a link's address"; fi
    grep -q "NAT64 edge: the pool entry 192.0.2.252/30 holds 192.0.2.253, an address of the link eth4" err.log ||
      fail "the run did not name the NAT64, the entry and the link: $(cat err.log)"
    unchanged "a run refused for a link's address changed the links, routes or instances"
    ip addr del 192.0.2.253/24 dev eth4

    # The edge's and guest's VRF has another table: the run fails after waiting for it once,
    # leaves the links out of the VRF, and keeps the edge's routes. guest's file names table
    # 254 as the edge's does, so guest's route in table 300 goes.
    ip link add wrong type vrf table 301
    ip link set wrong up
    cp ${dir}/edge.veth edge.veth
    cp ${dir}/guest.veth guest.veth
    jq '.vrf = "wrong"' edge.veth >${dir}/edge.veth
    jq '.vrf = "wrong" | .table = 254' guest.veth >${dir}/guest.veth
    state >before.txt
    start=$(date +%s)
    if ${ExecReload} 2>err.log; then fail "a run succeeded with a VRF that lacks its table"; fi
    took=$(($(date +%s) - start))
    [ "$took" -lt 50 ] || fail "two NAT64s of a VRF that lacks its table waited $took s"
    grep -q "VRF wrong has no table 254 after 30 s" err.log || fail "the run did not name the VRF: $(cat err.log)"
    grep -q "failed to bring up the namespace and link of edge" err.log || fail "the run did not name the edge: $(cat err.log)"
    grep -q "failed to bring up the namespace and link of guest" err.log || fail "the run did not name guest: $(cat err.log)"
    state >now.txt
    grep -v "dev n64-guest table guest-table proto 64" before.txt | diff - now.txt >&2 ||
      fail "a failed run changed the links, routes or instances"
    cp edge.veth ${dir}/edge.veth
    cp guest.veth ${dir}/guest.veth
    ip link del wrong

    # A device of another kind takes guest's link's name: the run leaves it and fails guest,
    # and with the device gone makes the link anew.
    ip link del n64-guest
    ip link add n64-guest type dummy
    if ${ExecReload} 2>err.log; then fail "a run succeeded beside a dummy device of guest's link's name"; fi
    grep -q "n64-guest is a device of kind dummy" err.log || fail "the run did not name the device: $(cat err.log)"
    [ "$(ip -d -j link show n64-guest | jq -r '.[0].linkinfo.info_kind')" = dummy ] || fail "the run removed the dummy device"
    ip link del n64-guest
    ${ExecReload} || fail "a run after the dummy device went failed"
    inside guest ip link show dev nat64 >/dev/null || fail "guest's namespace lacks the link's end after the dummy device went"

    # guest's namespace goes while a process holds it, which keeps its link: the next run makes
    # the link anew into the new namespace.
    nsenter --net=/run/netns/nat64-guest sleep 600 &
    holder=$!
    moved() { [ "$(readlink /proc/$holder/ns/net)" != "$main" ]; }
    retry 600 "the holder did not enter nat64-guest" moved
    ip netns delete nat64-guest
    ip link show n64-guest >/dev/null 2>&1 || fail "the held namespace took its link along"
    ${ExecReload} || fail "a run after guest's namespace went failed"
    inside guest ip link show dev nat64 >/dev/null || fail "guest's new namespace lacks the link's end"
    inside guest ip route show default | grep -q "dev nat64" || fail "guest's new namespace has no default route"
    has "$(nsof guest)" guest 64:ff9b::/96 || fail "no instance guest in its new namespace"
    ip -6 route show table 300 proto 64 | grep -q "^64:ff9b::/96 via fe80::2 dev n64-guest" || fail "table 300 does not route guest's prefix after the link was made anew"
    kill "$holder"

    # guest moves into the default VRF, with another prefix and a pool of one address.
    : >"$STATE.log"
    jq '.vrf = null | .table = 254 | .routes = ["198.51.100.9/32"]' ${dir}/guest.veth >guest.veth
    cp guest.veth ${dir}/guest.veth
    jq '.global.pool6 = "2001:db8:64::/96"' ${dir}/guest.json >guest.json
    cp guest.json ${dir}/guest.json
    ${ExecReload} || fail "moving guest failed"
    [ "$(ip -d -j link show n64-guest | jq -r '.[0].master // "none"')" = none ] || fail "n64-guest stayed in guest"
    [ -z "$(ip -6 route show table 300 proto 64)" ] || fail "table 300 kept a route of protocol 64"
    ip -6 route show proto 64 | grep -q "^2001:db8:64::/96 via fe80::2 dev n64-guest" || fail "the main table does not route guest's new prefix"
    ip -6 route show proto 64 | grep -q "^64:ff9b::/96 via fe80::2 dev n64-edge" || fail "the main table lost the edge's prefix"
    grep -qxF "$(nsof guest) -i guest instance remove" "$STATE.log" || fail "guest's instance of the old prefix was not removed first"
    has "$(nsof guest)" guest 2001:db8:64::/96 || fail "guest's instance kept its old prefix"
    ip -4 route show proto 64 | grep -q "^198.51.100.9 via .* dev n64-guest" || fail "the main table does not route guest's pool of one address"

    ${ExecStop} || fail "stopping chalkos-nat64 failed"
    if ls /run/netns/nat64-* >/dev/null 2>&1; then fail "kept a namespace after stopping"; fi
    if ip link show n64-edge >/dev/null 2>&1 || ip link show n64-guest >/dev/null 2>&1; then fail "kept a link after stopping"; fi
    [ -z "$(ip route show table all proto 64; ip -6 route show table all proto 64)" ] || fail "kept routes of protocol 64 after stopping"
    if awk -v ns="$main" '$1 == ns' "$STATE" | grep -q .; then fail "an instance of the router's own namespace after stopping"; fi
    # Jool drops the instances of a namespace that goes.
    : >"$STATE"

    # Started again at once, the unit brings everything back, and stops again.
    ${ExecStart} || fail "starting chalkos-nat64 again failed"
    for name in edge guest; do
      has "$(nsof "$name")" "$name" "$(jq -r .global.pool6 ${dir}/$name.json)" || fail "no instance $name after starting again"
      inside "$name" ip link show dev nat64 >/dev/null || fail "no link of $name after starting again"
    done
    ip -6 route show proto 64 | grep -q "^2001:db8:64::/96 via fe80::2 dev n64-guest" || fail "no route of guest's prefix after starting again"
    ${ExecStop} || fail "stopping chalkos-nat64 again failed"
    [ -z "$(ip netns list; ip route show table all proto 64; ip -6 route show table all proto 64)" ] || fail "kept state after stopping again"
  ''
