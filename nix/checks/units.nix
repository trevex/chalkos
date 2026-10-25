# Checks that run the units' scripts of the worker and of a router image as on a node, in
# namespaces of their own.
{ pkgs, checkLib, ... }:
let
  inherit (pkgs) lib;
  inherit (checkLib) worker firewall unitCheck;
in
{
  # The worker's VXLAN rule, chalkd vxlan-rule as its preparation unit runs it, with its firewall,
  # in a network namespace: a peer sends UDP to port 8472 over a veth pair, and the node receives
  # what the firewall accepts. The rule reads the cluster's ranges from the cluster file the test
  # writes. chalkd's unit tests cover what the rule makes of the files.
  vxlan-rule =
    let
      rule = builtins.head worker.config.systemd.services.chalkos-kubernetes.serviceConfig.ExecStartPre;
    in
    unitCheck "vxlan-rule" checkLib.networkTools ''
      fail() {
        echo "error: $*" >&2
        cat errors.log >&2 2>/dev/null || true
        nft list ruleset >&2
        exit 1
      }
      ${checkLib.peerNamespace}
      # Addresses of the node on another interface.
      ip link add d0 type dummy
      ip addr add 10.0.1.11/24 dev d0
      ip addr add fd00:1::11/64 dev d0 nodad
      ip link set d0 up
      # An interface of the pod network, over which the peer reaches the node too.
      ip link add cni0 type veth peer name c1 netns "$peer"
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
      unshare -n sh -c 'touch pod-ready; exec sleep 600' &
      pod=$!
      until [ -e pod-ready ]; do sleep 0.1; done
      on_pod() { nsenter -t "$pod" -n "$@"; }
      ip link add p0 type veth peer name p1 netns "$pod"
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
        for _ in $(seq 20); do
          grep -qx "datagram $n" received && return 0
          sleep 0.1
        done
        return 1
      }
      # Whether a datagram the pod sends to the address and port reaches the peer through the
      # node.
      forwarded() {
        n=$((n + 1))
        local to=$1
        case $1 in *:*) to="[$1]" ;; esac
        echo "datagram $n" | on_pod socat -u - "UDP-SENDTO:$to:$2"
        for _ in $(seq 20); do
          grep -qx "datagram $n" peer-received && return 0
          sleep 0.1
        done
        return 1
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
      for _ in $(seq 20); do
        grep -qx "datagram $n" received && break
        sleep 0.1
      done
      grep -qx "datagram $n" received || fail "VXLAN the node sent itself was refused"
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
            until [ -S vxlan ]; do sleep 0.1; done
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
    '';

  # The worker's registry hosts from Mirror and Registry documents: the files containerd reads,
  # kept as they were when chalkd fails, and none without documents.
  registry-mirrors =
    let
      script = worker.config.systemd.services.chalkos-registry-mirrors.serviceConfig.ExecStart;
    in
    unitCheck "registry-mirrors" [ ] ''
      fail() {
        echo "error: $*" >&2
        exit 1
      }
      documents=/run/chalkos/config/oci.chalkos.dev
      certs=/run/containerd/certs.d
      mkdir -p "$documents/Mirror" "$documents/Registry" "$certs/stale"
      cat >"$documents/Mirror/docker-io.json" <<'JSON'
      {"apiVersion":"oci.chalkos.dev/v1alpha1","kind":"Mirror","metadata":{"name":"docker-io"},
       "spec":{"registry":"docker.io","endpoints":["https://mirror.example","https://mirror2.example:5000/cache"]}}
      JSON
      cat >"$documents/Mirror/local.json" <<'JSON'
      {"apiVersion":"oci.chalkos.dev/v1alpha1","kind":"Mirror","metadata":{"name":"local"},
       "spec":{"registry":"[::1]:5000","endpoints":["http://10.0.2.100:5000"]}}
      JSON
      cat >"$documents/Registry/lab.json" <<'JSON'
      {"apiVersion":"oci.chalkos.dev/v1alpha1","kind":"Registry","metadata":{"name":"lab"},
       "spec":{"host":"10.0.2.100:5000","insecure":true,"username":null,"password":null}}
      JSON
      ${script}
      registries=$(find "$certs" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort)
      [ "$registries" = "$(printf '%s\n' 10.0.2.100:5000 docker.io '[::1]:5000' | sort)" ] || fail "the registries are $registries"
      cat >docker.io.toml <<'TOML'
      server = "https://registry-1.docker.io"

      [host."https://mirror.example"]
        capabilities = ["pull", "resolve"]

      [host."https://mirror2.example:5000/cache"]
        capabilities = ["pull", "resolve"]
      TOML
      diff -u docker.io.toml "$certs/docker.io/hosts.toml" || fail "docker.io's hosts.toml"
      cat >local.toml <<'TOML'
      server = "https://[::1]:5000"

      [host."http://10.0.2.100:5000"]
        capabilities = ["pull", "resolve"]
      TOML
      diff -u local.toml "$certs/[::1]:5000/hosts.toml" || fail "[::1]:5000's hosts.toml"
      cat >10.0.2.100:5000.toml <<'TOML'
      server = "http://10.0.2.100:5000"
      TOML
      diff -u 10.0.2.100:5000.toml "$certs/10.0.2.100:5000/hosts.toml" || fail "10.0.2.100:5000's hosts.toml"
      # A document chalkd cannot read leaves the mirrors as they were.
      echo '{' >"$documents/Mirror/local.json"
      if ${script}; then fail "an unreadable document was taken"; fi
      diff -u docker.io.toml "$certs/docker.io/hosts.toml" || fail "the mirrors changed"
      # No documents, no mirrors, and no directory left behind.
      rm -r "''${documents:?}"/*
      ${script}
      [ -z "$(ls "$certs")" ] || fail "mirrors without documents: $(ls "$certs")"
      [ "$(ls -A /run/containerd)" = certs.d ] || fail "left behind: $(ls -A /run/containerd)"
    '';

  # The worker's control-plane ports with its firewall, in a network namespace: a peer connects to
  # 6443, 2379 and 2380 over IPv4 and IPv6, which the firewall accepts once the node's Kubernetes
  # Node document makes it a control plane, and refuses again once it makes it a worker.
  controlplane-ports =
    let
      script = worker.config.systemd.services.chalkos-controlplane.serviceConfig.ExecStart;
    in
    unitCheck "controlplane-ports" checkLib.networkTools ''
      fail() {
        echo "error: $*" >&2
        nft list ruleset >&2
        exit 1
      }
      ${checkLib.peerNamespace}

      # The firewall as NixOS loads it, with the state file it keeps.
      mkdir -p /var/lib/nftables
      touch /var/lib/nftables/deletions.nft
      nft -f ${firewall}
      for port in 6443 2379 2380; do
        socat TCP4-LISTEN:$port,fork,reuseaddr /dev/null &
        socat TCP6-LISTEN:$port,fork,reuseaddr,ipv6only=1 /dev/null &
      done
      for try in $(seq 100); do
        [ "$(ss -Hltn | wc -l)" = 6 ] && break
        [ "$try" != 100 ] || { ss -Hltn >&2; fail "the listeners did not start"; }
        sleep 0.1
      done
      reaches() { on_peer socat -u /dev/null "TCP:$1:$2,connect-timeout=1" 2>/dev/null; }
      open() {
        for port in 6443 2379 2380; do
          reaches 10.0.0.11 $port || fail "$port refused $1"
          reaches '[fd00::11]' $port || fail "$port refused over IPv6 $1"
        done
      }
      closed() {
        for port in 6443 2379 2380; do
          if reaches 10.0.0.11 $port; then fail "$port open $1"; fi
          if reaches '[fd00::11]' $port; then fail "$port open over IPv6 $1"; fi
        done
      }
      node=/run/chalkos/config/kubernetes.chalkos.dev/Node/default.json
      mkdir -p "$(dirname "$node")"
      node_type() {
        printf '{"apiVersion":"kubernetes.chalkos.dev/v1alpha1","kind":"Node","metadata":{"name":"default"},"spec":{"type":"%s"}}\n' "$1" >"$node"
      }

      closed "without the table"
      node_type controlplane
      ${script}
      [ "$(stat -c %a /var/lib/etcd)" = 700 ] || fail "no etcd directory"
      open "on a control plane"
      # The firewall's reload leaves the table alone.
      nft -f ${firewall}
      open "after the firewall's reload"
      node_type worker
      ${script}
      closed "on a worker"
      # A worker without the table stays one.
      ${script}
      closed "on a worker again"
    '';

  # The chrony-dhcp unit's script with the links networkd announced time servers on: the servers
  # are chrony's when the node's TimeSync takes them, and none are otherwise. chrony's effect
  # reloads the sources of a chronyd that runs alone.
  chrony-dhcp =
    let
      script = worker.config.systemd.services.chalkos-chrony-dhcp.serviceConfig.ExecStart;
      path = lib.makeBinPath worker.config.systemd.services.chalkos-chrony-dhcp.path;
      # The script chrony's effect runs from chalkd's PATH.
      reloadSources = lib.getExe (
        lib.findFirst (
          p: lib.getName p == "chalkos-chrony-reload-sources"
        ) null worker.config.systemd.services.chalkd.path
      );
    in
    unitCheck "chrony-dhcp" [ ] ''
      fail() {
        echo "error: $*" >&2
        exit 1
      }
      # chronyc of the unit's path, which this namespace has no chronyd for.
      mkdir -p stub
      cat >stub/chronyc <<'STUB'
      #!/bin/sh
      echo "$@" >>chronyc.log
      STUB
      chmod +x stub/chronyc
      export PATH=$PWD/stub:${path}:$PATH
      mkdir -p /run/systemd/netif/links /run/chalkos/chrony-dhcp
      printf 'ADMIN_STATE=configured\nNTP=10.0.0.1 ntp.example\n' >/run/systemd/netif/links/2
      printf 'NTP=10.0.0.1 10.0.0.2\n' >/run/systemd/netif/links/3
      sources=/run/chalkos/chrony-dhcp/dhcp.sources
      timesync=/run/chalkos/config/time.chalkos.dev/TimeSync/timesync.json
      mkdir -p "$(dirname "$timesync")"
      dhcp() {
        printf '{"apiVersion":"time.chalkos.dev/v1alpha1","kind":"TimeSync","metadata":{"name":"timesync"},"spec":{"servers":[],"dhcp":%s}}\n' "$1" >"$timesync"
      }

      dhcp true
      ${script}
      printf 'server 10.0.0.1 iburst\nserver 10.0.0.2 iburst\nserver ntp.example iburst\n' >want
      diff -u want "$sources" || fail "the servers DHCP announced"
      grep -qx "reload sources" chronyc.log || fail "chrony did not reload its sources"
      dhcp false
      ${script}
      [ ! -s "$sources" ] || fail "servers with dhcp false: $(cat "$sources")"
      dhcp true
      ${script}
      rm "$timesync"
      ${script}
      [ ! -s "$sources" ] || fail "servers without a TimeSync: $(cat "$sources")"
      # A TimeSync chalkcfg cannot read takes no servers, and says why.
      echo '{' >"$timesync"
      ${script} 2>errors.log
      [ ! -s "$sources" ] || fail "servers with an unreadable TimeSync: $(cat "$sources")"
      grep -q timesync errors.log || fail "no error for an unreadable TimeSync"
      [ "$(ls -A /run/chalkos/chrony-dhcp)" = dhcp.sources ] || fail "left behind: $(ls -A /run/chalkos/chrony-dhcp)"
      # chrony's effect reloads the sources of a chronyd that runs, and leaves one that does not
      # run, which reads them when it starts.
      cat >stub/systemctl <<'STUB'
      #!/bin/sh
      exit "$ACTIVE"
      STUB
      chmod +x stub/systemctl
      : >chronyc.log
      ACTIVE=3 ${reloadSources} || fail "the effect failed for a chronyd that does not run"
      [ ! -s chronyc.log ] || fail "reloaded the sources of a chronyd that does not run"
      ACTIVE=0 ${reloadSources}
      grep -qx "reload sources" chronyc.log || fail "did not reload the sources of a running chronyd"
    '';

  # The router's firewall unit in network namespaces, with a LAN peer on lan0 and a WAN peer on
  # wan0, each over a veth pair. Without a rendered ruleset chalkos-firewall loads the fallback,
  # which accepts chalkd's port on every interface and forwards nothing. With the LAN example's
  # ruleset, which chalkos-render's golden test keeps as the renderer writes it, the peers probe
  # chalkd's port on the management interface alone, the protected ping past policies that drop
  # everything else and on dmz0, an interface of no zone, an AddressSet, forwarding by zone,
  # masquerade, destination NAT and the strict reverse-path filter. A reload replaces the table
  # alone, in one transaction, and keeps it when nft refuses the new one. The leaf example, loaded
  # as at boot, forwards everything.
  router-firewall =
    let
      inherit (checkLib.router.config.systemd.services.chalkos-firewall.serviceConfig)
        ExecStart
        ExecReload
        ;
      dir = checkLib.router.config.chalkos.outputs.router-nftables.dir;
      goldens = ../../cmd/chalkos-render/testdata/router;
      # Answers a connection with the address it came from.
      whoami = pkgs.writeShellScript "whoami" ''echo "$SOCAT_PEERADDR"'';
    in
    unitCheck "router-firewall" (checkLib.networkTools ++ [ pkgs.iputils ]) ''
      fail() {
        echo "error: $*" >&2
        nft list ruleset >&2
        exit 1
      }
      ip link set lo up
      unshare -n sh -c 'touch lan-ready; exec sleep 600' &
      lan=$!
      unshare -n sh -c 'touch wan-ready; exec sleep 600' &
      wan=$!
      until [ -e lan-ready ] && [ -e wan-ready ]; do sleep 0.1; done
      on_lan() { nsenter -t "$lan" -n "$@"; }
      on_wan() { nsenter -t "$wan" -n "$@"; }
      ip link add lan0 type veth peer name eth0 netns "$lan"
      ip link add wan0 type veth peer name eth0 netns "$wan"
      ip addr add 192.0.2.1/24 dev lan0
      ip addr add 2001:db8:1::1/64 dev lan0 nodad
      ip addr add 198.51.100.1/24 dev wan0
      ip addr add 2001:db8:2::1/64 dev wan0 nodad
      ip link set lan0 up
      ip link set wan0 up
      # The LAN peer reaches the node over dmz0 too, an interface of no zone.
      ip link add dmz0 type veth peer name eth1 netns "$lan"
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

      for port in 50000 22 8080; do
        socat TCP4-LISTEN:$port,fork,reuseaddr /dev/null &
        socat TCP6-LISTEN:$port,fork,reuseaddr,ipv6only=1 /dev/null &
      done
      for ns in on_lan on_wan; do
        $ns socat TCP4-LISTEN:80,fork,reuseaddr EXEC:${whoami} &
        $ns socat TCP6-LISTEN:80,fork,reuseaddr,ipv6only=1 EXEC:${whoami} &
      done
      for try in $(seq 100); do
        [ "$(ss -Hltn | wc -l)" = 6 ] && [ "$(on_lan ss -Hltn | wc -l)" = 2 ] && [ "$(on_wan ss -Hltn | wc -l)" = 2 ] && break
        [ "$try" != 100 ] || fail "the listeners did not start"
        sleep 0.1
      done
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
      install -m 644 ${goldens}/lan.nft ${dir}/ruleset.nft
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
      install -m 644 ${goldens}/leaf.nft ${dir}/ruleset.nft
      # At boot, with no table loaded yet, chalkos-firewall loads the rendered ruleset.
      nft delete table inet chalkos
      ${ExecStart} || fail "chalkos-firewall did not load the leaf ruleset"
      accepted on_wan '[2001:db8:1::2]' 80
      refused on_lan 192.0.2.1 50000
    '';
}
