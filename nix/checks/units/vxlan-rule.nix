# The worker's VXLAN rule, chalkd kubernetes vxlan-rule as its preparation unit runs it, with its
# firewall, in a network namespace: a peer sends UDP to port 8472 over a veth pair, and the node
# receives what the firewall accepts. The rule reads the cluster's ranges from the cluster file
# the test writes. chalkd's unit tests cover what the rule makes of the files.
{
  checkLib,
  worker,
  firewall,
  unitCheck,
  ...
}:
let
  rule = builtins.head worker.config.systemd.services.chalkos-kubernetes.serviceConfig.ExecStartPre;
in
unitCheck "vxlan-rule" checkLib.networkTools ''
  debug() {
    cat errors.log >&2 2>/dev/null || true
    nft list ruleset >&2
  }
  ${checkLib.peerNamespace}
  # Addresses of the node on another interface.
  ip link add d0 type dummy
  ip addr add 10.0.1.11/24 dev d0
  ip addr add fd00:1::11/64 dev d0 nodad
  ip link set d0 up
  # An interface of the pod network, over which the peer reaches the node too.
  ip link add cni0 type veth peer name c1 netns "''${pids[peer]}"
  ip addr add 10.0.2.11/24 dev cni0
  ip addr add fd00:2::11/64 dev cni0 nodad
  ip link set cni0 up
  on_peer ip route add 10.0.1.0/24 via 10.0.0.11
  on_peer ip route add fd00:1::/64 via fd00::11
  on_peer ip addr add 10.0.2.12/24 dev c1
  on_peer ip addr add fd00:2::12/64 dev c1 nodad
  on_peer ip link set c1 up
  # The routes of datagrams the peer sends out of c1.
  on_peer ip route add 10.0.1.0/24 via 10.0.2.11 dev c1 metric 100
  on_peer ip route add fd00:1::/64 via fd00:2::11 dev c1 metric 2048
  # A pod on the node, whose datagrams the node forwards to the peer.
  netns pod
  ip link add p0 type veth peer name p1 netns "''${pids[pod]}"
  ip addr add 10.244.0.1/24 dev p0
  ip addr add fd00:10:244::1/64 dev p0 nodad
  ip link set p0 up
  on_pod ip link set lo up
  on_pod ip addr add 10.244.0.2/24 dev p1
  on_pod ip addr add fd00:10:244::2/64 dev p1 nodad
  on_pod ip link set p1 up
  on_pod ip route add default via 10.244.0.1
  on_pod ip route add default via fd00:10:244::1
  echo 1 >/proc/sys/net/ipv4/ip_forward
  echo 1 >/proc/sys/net/ipv6/conf/all/forwarding
  # The peer has no route back to the pod; it takes the pod's datagrams nonetheless.
  for conf in all v1 c1; do
    on_peer sh -c "echo 0 >/proc/sys/net/ipv4/conf/$conf/rp_filter"
  done

  # The firewall as NixOS loads it, with the state file it keeps.
  mkdir -p /var/lib/nftables
  touch /var/lib/nftables/deletions.nft
  nft -f ${firewall}
  # Counts the packets the firewall accepted, and those still marked then.
  nft -f - <<'NFT'
  table inet count {
    chain input {
      type filter hook input priority filter + 10; policy accept;
      udp dport 8472 counter name accepted
      udp dport 8472 meta mark & 0x01000000 != 0 counter name marked
    }
    counter accepted {}
    counter marked {}
  }
  NFT

  socat -u UDP4-RECV:8472 OPEN:received,creat,append &
  socat -u UDP6-RECV:8472,ipv6only=1 OPEN:received,creat,append &
  for port in 8472 8473; do
    on_peer socat -u UDP4-RECV:$port OPEN:peer-received,creat,append &
    on_peer socat -u UDP6-RECV:$port,ipv6only=1 OPEN:peer-received,creat,append &
  done
  sleep 0.5
  n=0
  # Whether a datagram the peer sends to the address arrives, with further socat options
  # for the sender, such as the interface it goes out of.
  arrives() {
    n=$((n + 1))
    local to=$1
    case $1 in *:*) to="[$1]" ;; esac
    echo "datagram $n" | on_peer socat -u - "UDP-SENDTO:$to:8472''${2:-}"
    retry 20 "" grep -qx "datagram $n" received
  }
  # Whether a datagram the pod sends to the address and port reaches the peer through the
  # node.
  forwarded() {
    n=$((n + 1))
    local to=$1
    case $1 in *:*) to="[$1]" ;; esac
    echo "datagram $n" | on_pod socat -u - "UDP-SENDTO:$to:$2"
    retry 20 "" grep -qx "datagram $n" peer-received
  }
  # Runs the rule with the arguments, setting status; the table it prints goes to rules.log.
  run() {
    status=0
    ${rule} "$@" >rules.log 2>errors.log || status=$?
  }
  # Runs the rule with the file holding the content, with escapes. Without source lines
  # VXLAN may come from any source.
  holding() {
    printf '%b' "$1" >vxlan
    run vxlan
  }
  v4='destination 10.0.0.11 interface\n'
  v6='destination fd00::11 interface\n'
  # The node's cluster file, as chalkd writes it from the Cluster and Flannel documents: with
  # the spec given, or of a dual-stack cluster whose flannel has the source ranges given.
  cluster=/run/chalkos/kubernetes/cluster.json
  mkdir -p "$(dirname "$cluster")"
  spec() {
    printf '%s\n' "$1" >"$cluster"
  }
  ranges() {
    spec '{"ipFamilies":["ipv4","ipv6"],"podCIDRs":{"ipv4":"10.244.0.0/16","ipv6":"fd00:10:244::/56"},"flannel":{"vxlanSourceSubnets":'"$1"'}}'
  }
  ranges '["10.0.0.0/24","fd00::/64"]'
  rules() {
    grep -c 'meta mark set meta mark |' rules.log || true
  }
  # The rules in the table now, which may differ from what the rule printed.
  marking() {
    nft list table inet chalkos-vxlan 2>/dev/null | grep -c 'meta mark set meta mark |' || true
  }
  marked() {
    nft list counter inet count marked | grep -o 'packets [0-9]*'
  }

  if arrives 10.0.0.11; then fail "VXLAN arrived without the rule"; fi

  holding "$v4$v6"
  [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "an address of each family"
  arrives 10.0.0.11 || fail "IPv4 VXLAN to the node's address was refused"
  arrives fd00::11 || fail "IPv6 VXLAN to the node's address was refused"
  # The node's other addresses, and the picked ones arriving on another interface.
  if arrives 10.0.1.11; then fail "VXLAN arrived at another address"; fi
  if arrives fd00:1::11; then fail "IPv6 VXLAN arrived at another address"; fi
  [ "$(marked)" = "packets 0" ] || fail "accepted packets keep the mark"

  # A mark another table set earlier opens nothing.
  nft -f - <<'NFT'
  table inet premark {
    chain input {
      type filter hook input priority filter - 10; policy accept;
      udp dport 8472 meta mark set meta mark | 0x01000000
    }
  }
  NFT
  if arrives 10.0.1.11; then fail "VXLAN marked by another table arrived"; fi
  if arrives fd00:1::11; then fail "IPv6 VXLAN marked by another table arrived"; fi
  arrives 10.0.0.11 || fail "VXLAN marked by another table too was refused"
  nft delete table inet premark

  # Pods cannot send VXLAN through the node to the source ranges, where the node's
  # masquerade would give it a source the other nodes take VXLAN from.
  forwarded 10.0.0.12 8473 || fail "a pod's datagram to another port was dropped"
  forwarded fd00::12 8473 || fail "a pod's IPv6 datagram to another port was dropped"
  if forwarded 10.0.0.12 8472; then fail "a pod sent VXLAN to a source range"; fi
  if forwarded fd00::12 8472; then fail "a pod sent IPv6 VXLAN to a source range"; fi
  forwarded 10.0.2.12 8472 || fail "a pod's VXLAN outside the source ranges was dropped"
  forwarded fd00:2::12 8472 || fail "a pod's IPv6 VXLAN outside the source ranges was dropped"
  # Without source ranges VXLAN may come from anywhere, so nothing is dropped.
  ranges '[]'
  holding "$v4$v6"
  forwarded 10.0.0.12 8472 || fail "a pod's VXLAN was dropped without source ranges"
  forwarded fd00::12 8472 || fail "a pod's IPv6 VXLAN was dropped without source ranges"

  # Ranges that cannot be read drop all VXLAN the node forwards, and say so; the node still
  # takes VXLAN.
  closed() {
    holding "$v4$v6"
    [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "the addresses $1"
    grep -q "ranges are unknown" errors.log || fail "no warning $1"
    if forwarded 10.0.2.12 8472; then fail "a pod sent VXLAN outside the source ranges $1"; fi
    if forwarded fd00:2::12 8472; then fail "a pod sent IPv6 VXLAN outside the source ranges $1"; fi
    forwarded 10.0.2.12 8473 || fail "a pod's datagram to another port was dropped $1"
    arrives 10.0.0.11 || fail "VXLAN to the node was refused $1"
  }
  rm "$cluster"
  closed "without a cluster file"
  echo '{' >"$cluster"
  closed "with a cluster file that cannot be read"
  spec '{"ipFamilies":["ipv4","ipv6"],"podCIDRs":{"ipv4":"10.244.0.0/16"}}'
  closed "with a family without a pod range"
  # Without flannel VXLAN may come from anywhere, so nothing is dropped.
  spec '{"ipFamilies":["ipv4","ipv6"],"podCIDRs":{"ipv4":"10.244.0.0/16","ipv6":"fd00:10:244::/56"}}'
  holding "$v4$v6"
  [ ! -s errors.log ] || fail "warned without flannel: $(cat errors.log)"
  forwarded 10.0.0.12 8472 || fail "a pod's VXLAN was dropped without flannel"
  ranges '["10.0.0.0/24","fd00::/64"]'
  holding "$v4$v6"
  if forwarded 10.0.0.12 8472; then fail "a pod sent VXLAN to a source range after the ranges came back"; fi
  forwarded 10.0.2.12 8472 || fail "a pod's VXLAN outside the source ranges was dropped after the ranges came back"

  # A reload of the firewall replaces its own table alone.
  nft add table ip kube-proxy
  nft add table ip6 flannel-ipv6
  nft -f ${firewall}
  nft list tables | grep -qx 'table ip kube-proxy' || fail "the firewall's reload removed kube-proxy's table"
  nft list tables | grep -qx 'table ip6 flannel-ipv6' || fail "the firewall's reload removed flannel's table"
  arrives 10.0.0.11 || fail "VXLAN was refused after the firewall's reload"
  arrives fd00::11 || fail "IPv6 VXLAN was refused after the firewall's reload"

  # Addresses on d0, taking VXLAN there alone, refuse it arriving on v0.
  holding 'destination 10.0.1.11 interface\ndestination fd00:1::11 interface\n'
  [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "addresses on another interface"
  if arrives 10.0.1.11; then fail "VXLAN arrived on an interface not holding the address"; fi
  if arrives fd00:1::11; then fail "IPv6 VXLAN arrived on an interface not holding the address"; fi

  holding "$v6"
  [ "$status" = 0 ] && [ "$(rules)" = 1 ] || fail "the IPv6 address alone"
  if arrives 10.0.0.11; then fail "VXLAN arrived at an address no longer picked"; fi
  arrives fd00::11 || fail "IPv6 VXLAN to the node's address was refused"

  # An address on a dummy interface, as a routing daemon announces it, takes VXLAN that
  # arrives on another interface.
  holding 'destination 10.0.1.11 any\ndestination fd00:1::11 any\n'
  [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "addresses on a dummy interface"
  arrives 10.0.1.11 || fail "VXLAN to the address on the dummy interface was refused"
  arrives fd00:1::11 || fail "IPv6 VXLAN to the address on the dummy interface was refused"
  if arrives 10.0.0.11; then fail "VXLAN arrived at an address no longer picked"; fi
  # Not on an interface of the pod network, where pods could send it.
  if arrives 10.0.1.11 ,so-bindtodevice=c1; then fail "VXLAN arrived on the pod network"; fi
  if arrives fd00:1::11 ,so-bindtodevice=c1; then fail "IPv6 VXLAN arrived on the pod network"; fi
  # The mark goes once the firewall saw the packet, even one it accepts before looking at
  # the mark, such as those the node sends itself over lo.
  nft reset counters table inet count >/dev/null
  n=$((n + 1))
  echo "datagram $n" | socat -u - UDP-SENDTO:10.0.1.11:8472
  retry 20 "VXLAN the node sent itself was refused" grep -qx "datagram $n" received
  [ "$(marked)" = "packets 0" ] || fail "packets the firewall accepted on lo keep the mark"

  # With source ranges VXLAN comes from them alone; a family without one takes none.
  holding "$v4$v6"'source 10.0.0.0/24\nsource fd00::/64\n'
  [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "sources of each family"
  arrives 10.0.0.11 || fail "VXLAN from a source in the ranges was refused"
  arrives fd00::11 || fail "IPv6 VXLAN from a source in the ranges was refused"
  holding "$v4$v6"'source 10.0.9.0/24\nsource fd00:9::/64\nsource 192.168.0.0/16\n'
  [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "sources elsewhere"
  if arrives 10.0.0.11; then fail "VXLAN arrived from a source outside the ranges"; fi
  if arrives fd00::11; then fail "IPv6 VXLAN arrived from a source outside the ranges"; fi
  holding "$v4$v6"'source 10.0.0.0/24\n'
  [ "$status" = 0 ] && [ "$(rules)" = 1 ] || fail "sources of one family"
  arrives 10.0.0.11 || fail "VXLAN from a source in the ranges was refused"
  if arrives fd00::11; then fail "IPv6 VXLAN arrived without an IPv6 source range"; fi

  # A file holding anything but the node's addresses and source ranges adds no rule, empties
  # the table and fails.
  for content in "" 'destination 10.0.0.11 eth0\n' 'destination cafe.be interface\n' \
    'destination fe80::1%v0 interface\n' "$v4"'source 10.0.0.0/24 }\n'; do
    holding "$v4"
    holding "$content"
    [ "$status" != 0 ] && [ "$(rules)" = 0 ] || fail "accepted a file holding '$content'"
    if arrives 10.0.0.11; then fail "VXLAN arrived after the file held '$content'"; fi
  done

  # IPv6 addresses written out in full, and with an IPv4 address at the end.
  holding "$v4"'destination fd00:0:0:0:0:0:0:11 interface\n'
  [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "an IPv6 address in full"
  arrives fd00::11 || fail "IPv6 VXLAN to the address in full was refused"
  holding 'destination ::ffff:10.0.0.11 interface\n'
  [ "$status" = 0 ] && [ "$(rules)" = 1 ] || fail "an IPv6 address ending in an IPv4 one"

  # A file that cannot be read empties the table and fails.
  for kind in directory socket; do
    holding "$v4"
    rm vxlan
    case $kind in
      directory) mkdir vxlan ;;
      socket)
        socat UNIX-LISTEN:vxlan /dev/null &
        listener=$!
        retry 600 "socat opened no socket vxlan" test -S vxlan
        ;;
    esac
    run vxlan
    [ "$status" != 0 ] && [ "$(marking)" = 0 ] || fail "accepted a $kind"
    if arrives 10.0.0.11; then fail "VXLAN arrived after the file was a $kind"; fi
    if [ "$kind" = socket ]; then kill "$listener"; wait "$listener" || true; fi
    rm -rf vxlan
  done

  # A lock that cannot be taken empties the table and fails.
  holding "$v4"
  rm /run/chalkos-vxlan-rule.lock
  mkdir /run/chalkos-vxlan-rule.lock
  run vxlan
  [ "$status" != 0 ] && [ "$(marking)" = 0 ] || fail "kept its rules without the lock"
  if arrives 10.0.0.11; then fail "VXLAN arrived after the lock could not be taken"; fi
  rmdir /run/chalkos-vxlan-rule.lock

  # Without the file or an argument the table is emptied.
  holding "$v4"
  rm vxlan
  run vxlan
  [ "$status" = 0 ] && [ "$(rules)" = 0 ] || fail "without the file"
  holding "$v4"
  run
  [ "$status" = 0 ] && [ "$(rules)" = 0 ] || fail "without an argument"
  if arrives 10.0.0.11; then fail "VXLAN arrived without the file"; fi
  if forwarded 10.0.0.12 8472; then fail "a pod sent VXLAN to a source range without the file"; fi
''
