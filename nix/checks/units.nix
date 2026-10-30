# Checks that run the units' scripts of the worker and of a router image as on a node, in
# namespaces of their own.
{ pkgs, checkLib, ... }:
let
  inherit (pkgs) lib;
  inherit (checkLib) worker firewall unitCheck;
in
{
  # The worker's VXLAN rule, chalkd kubernetes vxlan-rule as its preparation unit runs it, with its
  # firewall, in a network namespace: a peer sends UDP to port 8472 over a veth pair, and the node
  # receives what the firewall accepts. The rule reads the cluster's ranges from the cluster file
  # the test writes. chalkd's unit tests cover what the rule makes of the files.
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

  # chalkos-vrf in a network namespace: it removes the VRFs that no .netdev of Kind=vrf declares
  # and those whose table changed, flushes networkd's unreachable defaults of their tables alone,
  # keeps a declared VRF with its member, the unreachable defaults of its table and every link of
  # another kind, and with no rendered units, as in maintenance mode, removes every VRF the
  # rendered units declared. It reads /etc's units before the rendered ones, by name, as networkd
  # does. When it removed a VRF it reloads a running networkd, through the systemctl and networkctl
  # of the unit's PATH that the check stubs, and otherwise does not.
  router-vrf =
    let
      script = checkLib.router.config.systemd.services.chalkos-vrf.serviceConfig.ExecReload;
      dir = checkLib.router.config.chalkos.outputs.networkd.dir;
    in
    unitCheck "router-vrf" checkLib.networkTools ''
      fail() {
        echo "error: $*" >&2
        ip -d link show >&2
        exit 1
      }
      exists() { ip link show "$1" >/dev/null 2>&1; }
      reloaded() {
        [ "$(cat networkctl.log)" = reload ] || fail "$1: networkctl was called with: $(cat networkctl.log)"
        : >networkctl.log
      }
      not_reloaded() { [ ! -s networkctl.log ] || fail "$1: networkctl was called with: $(cat networkctl.log)"; }
      mkdir -p stub
      cat >stub/networkctl <<'STUB'
      #!/bin/sh
      echo "$@" >>networkctl.log
      STUB
      cat >stub/systemctl <<'STUB'
      #!/bin/sh
      exit "''${ACTIVE:-0}"
      STUB
      chmod +x stub/networkctl stub/systemctl
      : >networkctl.log
      export PATH=$PWD/stub:$PATH
      mkdir -p ${dir} etc/systemd/network
      printf '[NetDev]\nKind=vrf\nName=keep\n\n[VRF]\nTable=100\n\n' >${dir}/00-vrf-keep.netdev
      printf '[NetDev]\nKind=vrf\nName=moved\n\n[VRF]\nTable=300\n\n' >${dir}/00-vrf-moved.netdev
      printf '[NetDev]\nKind=dummy\nName=gone\n\n' >${dir}/20-gone.netdev
      # /etc's units, which networkd reads before the rendered ones of the same name.
      printf '# A VRF of the image.\n[NetDev]\nName = local\nKind = vrf\n\n[VRF]\nTable = 500\n' >etc/systemd/network/10-local.netdev
      printf '[NetDev]\nKind=vrf\nName=masked\n\n[VRF]\nTable=600\n\n' >${dir}/00-vrf-masked.netdev
      ln -s /dev/null etc/systemd/network/00-vrf-masked.netdev
      ip link add keep type vrf table 100
      ip link add moved type vrf table 200
      ip link add gone type vrf table 400
      ip link add local type vrf table 500
      ip link add masked type vrf table 600
      ip link add d0 type dummy
      ip link set d0 master keep
      # The unreachable defaults that networkd installs into each VRF's table, of protocol static,
      # which stay in the table when the VRF goes, and unreachable routes of another prefix or
      # another protocol, which are not networkd's defaults.
      for table in 100 200 400; do
        ip -4 route add unreachable default metric 4278198272 proto static table "$table"
        ip -6 route add unreachable default metric 4278198272 proto static table "$table"
      done
      ip -4 route add unreachable 198.51.100.0/24 table 400
      ip -6 route add unreachable 2001:db8::/32 table 400
      ip -4 route add unreachable default metric 100 proto boot table 400
      ip -6 route add unreachable default metric 100 proto boot table 400
      ${script} || fail "chalkos-vrf failed"
      exists keep || fail "removed a VRF its .netdev declares"
      ip -d link show keep | grep -q 'vrf table 100' || fail "changed the table of a declared VRF"
      [ "$(ip -o link show master keep | wc -l)" = 1 ] || fail "a declared VRF lost its member"
      for family in -4 -6; do
        ip "$family" route show table 100 | grep -q '^unreachable default.* proto static' ||
          fail "removed the unreachable default ($family) of a declared VRF's table"
        for table in 200 400; do
          if ip "$family" route show table "$table" | grep -q '^unreachable default.* proto static'; then
            fail "kept the unreachable default ($family) of table $table, whose VRF it removed"
          fi
        done
        [ "$(ip "$family" route show table 400 | wc -l)" = 2 ] ||
          fail "flushed more than the unreachable default ($family) of table 400 that networkd installs"
      done
      if exists moved; then fail "kept a VRF whose table changed"; fi
      if exists gone; then fail "kept a VRF that no .netdev of Kind=vrf declares"; fi
      exists local || fail "removed a VRF that a unit of /etc declares"
      if exists masked; then fail "kept a VRF whose rendered unit /etc masks"; fi
      exists d0 || fail "removed a link of another kind"
      reloaded "after removing VRFs"
      ${script} || fail "a second run failed"
      exists keep || fail "a second run removed a declared VRF"
      not_reloaded "a run that removed nothing"
      rm ${dir}/*.netdev
      ${script} || fail "chalkos-vrf without units failed"
      if exists keep; then fail "kept a VRF without rendered units"; fi
      exists local || fail "removed a VRF of /etc without rendered units"
      exists d0 || fail "removed a link of another kind without rendered units"
      reloaded "after removing VRFs without rendered units"
      ip link add other type vrf table 700
      ACTIVE=3 ${script} || fail "chalkos-vrf failed while networkd does not run"
      if exists other; then fail "kept a VRF while networkd does not run"; fi
      not_reloaded "a run while networkd does not run"
    '';

  # The router's firewall unit in network namespaces, with a LAN peer on lan0 and a WAN peer on
  # wan0, each over a veth pair. Without a rendered ruleset chalkos-firewall loads the fallback,
  # which accepts chalkd's port on every interface and forwards nothing. With the LAN example's
  # ruleset, which chalkos-render's golden test keeps as the renderer writes it, the peers probe
  # chalkd's port on the management interface alone, the protected ping past policies that drop
  # everything else and on dmz0, an interface of no zone, an AddressSet, forwarding by zone,
  # masquerade, destination NAT and the strict reverse-path filter. A reload replaces the table
  # alone, in one transaction, and keeps it when nft refuses the new one. The leaf example, loaded
  # as at boot, forwards everything. The VRF example's ruleset, with its links in VRFs as networkd
  # leaves them and the routes its imports leak, forwards and masquerades between zones of whole
  # VRFs in both families, opens the transit's BGP port in its VRF alone, masquerades and
  # translates in guest, whose addresses the default VRF and internet also use, and keeps guest's
  # connections in their conntrack zone, so that a guest host of a management host's address
  # cannot pass as the management host's connection, as it does without the zone. The loose
  # reverse-path filter drops a source of either family that only the default VRF routes, and
  # passes link-local sources in a VRF.
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
      unshare -n sh -c 'touch mgmt-ready; exec sleep 600' &
      mgmt=$!
      unshare -n sh -c 'touch isp-ready; exec sleep 600' &
      isp=$!
      unshare -n sh -c 'touch tenant-ready; exec sleep 600' &
      tenant=$!
      unshare -n sh -c 'touch guest-ready; exec sleep 600' &
      guest=$!
      unshare -n sh -c 'touch gisp-ready; exec sleep 600' &
      gisp=$!
      until [ -e mgmt-ready ] && [ -e isp-ready ] && [ -e tenant-ready ] && [ -e guest-ready ] && [ -e gisp-ready ]; do sleep 0.1; done
      on_mgmt() { nsenter -t "$mgmt" -n "$@"; }
      on_isp() { nsenter -t "$isp" -n "$@"; }
      on_tenant() { nsenter -t "$tenant" -n "$@"; }
      on_guest() { nsenter -t "$guest" -n "$@"; }
      on_gisp() { nsenter -t "$gisp" -n "$@"; }
      # vrflink <link> <VRF or -> <peer's PID> <router's address> <peer's address>: a veth pair to
      # the peer's eth0 in a /24, the peer's default route through the router. Link-local
      # addresses are usable at once, without duplicate address detection.
      vrflink() {
        nsenter -t "$3" -n sh -c 'echo 0 >/proc/sys/net/ipv6/conf/default/accept_dad'
        ip link add "$1" type veth peer name eth0 netns "$3"
        if [ "$2" != - ]; then ip link set "$1" master "$2"; fi
        ip addr add "$4/24" dev "$1"
        ip link set "$1" up
        nsenter -t "$3" -n ip link set lo up
        nsenter -t "$3" -n ip addr add "$5/24" dev eth0
        nsenter -t "$3" -n ip link set eth0 up
        nsenter -t "$3" -n ip route add default via "$4"
      }
      vrflink eth0 - "$mgmt" 192.0.2.1 192.0.2.10
      vrflink eth1 internet "$isp" 203.0.113.1 203.0.113.254
      vrflink eth2 tenant "$tenant" 198.51.100.1 198.51.100.10
      vrflink eth3 guest "$guest" 192.0.2.1 192.0.2.10
      vrflink eth4 guest "$gisp" 203.0.113.1 203.0.113.254
      ip addr add 2001:db8:1::1/64 dev eth1 nodad
      ip addr add 2001:db8:2::1/64 dev eth2 nodad
      on_isp ip addr add 2001:db8:1::254/64 dev eth0 nodad
      on_isp ip route add 2001:db8:2::/64 via 2001:db8:1::1
      on_tenant ip addr add 2001:db8:2::10/64 dev eth0 nodad
      on_tenant ip route add default via 2001:db8:2::1
      # What the imports leak: internet's uplink into tenant, tenant's LAN into internet.
      ip -4 route add 203.0.113.0/24 dev eth1 table 200
      ip -6 route add 2001:db8:1::/64 dev eth1 table 200
      ip -4 route add 198.51.100.0/24 dev eth2 table 100
      ip -6 route add 2001:db8:2::/64 dev eth2 table 100
      # guest's default route through its uplink's gateway.
      ip -4 route add default via 203.0.113.254 dev eth4 table 300
      # Ranges the default VRF alone routes, which tenant's spoofed sources take.
      ip -4 route add 10.99.0.0/24 dev eth0
      ip -6 route add 2001:db8:99::/64 dev eth0
      for ns in on_isp on_tenant; do
        $ns socat TCP4-LISTEN:80,fork,reuseaddr EXEC:${whoami} &
        $ns socat TCP6-LISTEN:80,fork,reuseaddr,ipv6only=1 EXEC:${whoami} &
      done
      on_guest socat TCP4-LISTEN:80,fork,reuseaddr EXEC:${whoami} &
      on_gisp socat TCP4-LISTEN:80,fork,reuseaddr EXEC:${whoami} &
      # BGP's port in internet and in guest, whose uplink peer has the transit's address.
      socat TCP4-LISTEN:179,fork,reuseaddr,so-bindtodevice=internet /dev/null &
      socat TCP4-LISTEN:179,fork,reuseaddr,so-bindtodevice=guest /dev/null &
      for try in $(seq 100); do
        [ "$(ss -Hltn sport = :179 | wc -l)" = 2 ] && [ "$(on_isp ss -Hltn | wc -l)" = 2 ] && [ "$(on_tenant ss -Hltn | wc -l)" = 2 ] &&
          [ "$(on_guest ss -Hltn | wc -l)" = 1 ] && [ "$(on_gisp ss -Hltn | wc -l)" = 1 ] && break
        [ "$try" != 100 ] || fail "the VRF phase's listeners did not start"
        sleep 0.1
      done
      # Counts what the input chain accepted on port 22, at a later priority, what reached input
      # from tenant's spoofed sources, and the forwarded packets of conntrack zone 1.
      nft -f - <<'NFT'
      table inet passed {
        counter mgmt22 {}
        counter guest22 {}
        counter spoofed {}
        counter zoned {}
        chain input {
          type filter hook input priority filter + 10; policy accept;
          iifname "eth0" tcp dport 22 counter name mgmt22
          iifname "guest" tcp dport 22 counter name guest22
        }
        chain before {
          type filter hook input priority filter - 10; policy accept;
          ip saddr 10.99.0.99 counter name spoofed
          ip6 saddr 2001:db8:99::99 counter name spoofed
        }
        chain forward {
          type filter hook forward priority filter + 10; policy accept;
          ct zone 1 counter name zoned
        }
      }
      NFT
      install -m 644 ${goldens}/vrf.nft ${dir}/ruleset.nft
      ${ExecReload} || fail "chalkos-firewall did not reload the VRF ruleset"
      none() { nft list counter inet passed "$1" | grep -q 'packets 0 '; }

      # tenant reaches the internet by the leaked routes, masqueraded out of internet in IPv4 and
      # unchanged in IPv6; the internet reaches neither the tenant nor the router's other ports.
      [ "$(seen on_tenant 203.0.113.254)" = 203.0.113.1 ] || fail "tenant's IPv4 was not masqueraded out of internet"
      [ "$(seen on_tenant '[2001:db8:1::254]')" = '[2001:0db8:0002:0000:0000:0000:0000:0010]' ] || fail "tenant's IPv6 did not reach internet unchanged"
      refused on_isp 198.51.100.10 80
      refused on_isp '[2001:db8:2::10]' 80
      refused on_isp 203.0.113.1 22
      # The transit's BGP port opens in internet alone: guest's uplink peer has its address.
      accepted on_isp 203.0.113.1 179
      refused on_gisp 203.0.113.1 179
      # guest masquerades out of eth4 and forwards 8080 to its LAN, in its conntrack zone.
      [ "$(seen on_guest 203.0.113.254)" = 203.0.113.1 ] || fail "guest's IPv4 was not masqueraded out of eth4"
      [ "$(seen on_gisp 203.0.113.1 8080)" = 203.0.113.254 ] || fail "guest's destination NAT did not reach its LAN"
      if none zoned; then fail "guest's connections are not in conntrack zone 1"; fi
      # mgmt holds a connection from port 40000 to the router's port 22. guest's host has mgmt's
      # address and tries the same ports: in zone 1 it is a new connection, which no policy
      # accepts, not a packet of mgmt's.
      established() {
        for try in $(seq 50); do
          [ -n "$(ss -Htn state established "( sport = :22 and dport = :$1 )")" ] && return
          [ "$try" != 50 ] || fail "mgmt's connection from port $1 to port 22 was not established"
          sleep 0.1
        done
      }
      on_mgmt socat -u EXEC:"sleep 3" TCP:192.0.2.1:22,sourceport=40000,connect-timeout=1 &
      established 40000
      on_guest socat -u /dev/null TCP:192.0.2.1:22,sourceport=40000,connect-timeout=1 2>/dev/null || true
      if none mgmt22; then fail "mgmt's connection to port 22 did not pass"; fi
      none guest22 || fail "guest's connection rode mgmt's through the input chain"
      # The loose reverse-path filter drops a source tenant's table does not route, which the
      # default VRF alone does, while tenant's own sources reach input, and passes link-local
      # sources in a VRF.
      pings on_tenant 198.51.100.1
      pings on_tenant 2001:db8:2::1
      on_tenant ip addr add 10.99.0.99/32 dev eth0
      on_tenant ip addr add 2001:db8:99::99/128 dev eth0 nodad
      on_tenant ping -c 1 -W 1 -I 10.99.0.99 198.51.100.1 >/dev/null 2>&1 || true
      on_tenant ping -c 1 -W 1 -I 2001:db8:99::99 2001:db8:2::1 >/dev/null 2>&1 || true
      none spoofed || fail "the reverse-path filter passed a source only the default VRF routes"
      ll=
      for try in $(seq 50); do
        read -r _ _ _ ll _ <<<"$(ip -6 -o addr show dev eth2 scope link)" || true
        [ -n "$ll" ] && break
        [ "$try" != 50 ] || fail "eth2 has no link-local address"
        sleep 0.1
      done
      pings on_tenant "''${ll%/*}%eth0"

      where="with the VRF example's ruleset without its conntrack zones"
      # The same probe without the zones: guest's host passes as mgmt's connection.
      sed '/^\tchain ct-\(prerouting\|output\) {$/,/^\t}$/d' ${goldens}/vrf.nft >${dir}/ruleset.nft
      if grep -q 'ct zone' ${dir}/ruleset.nft; then fail "the ruleset still sets conntrack zones"; fi
      ${ExecReload} || fail "chalkos-firewall did not reload the VRF ruleset without conntrack zones"
      on_mgmt socat -u EXEC:"sleep 3" TCP:192.0.2.1:22,sourceport=40001,connect-timeout=1 &
      established 40001
      on_guest socat -u /dev/null TCP:192.0.2.1:22,sourceport=40001,connect-timeout=1 2>/dev/null || true
      if none guest22; then fail "guest's connection did not ride mgmt's, so the probe shows nothing"; fi
    '';

  # frr.service fills the rendered config's password placeholders in from its credentials into
  # /run/frr/frr.conf, readable by root alone and replaced whole, and loads the fallback config
  # without a rendered one. Only a peer group's password line takes a value: a placeholder
  # elsewhere stays as written. A password with a line break, a trailing one too, white space, a
  # NUL or more than 80 characters, and a credential the unit lacks, fail the unit and leave the
  # filled config as it was, as does a fill without a credentials directory.
  frr-credentials =
    let
      fill = checkLib.router.config.systemd.services.frr.serviceConfig.ExecStartPre;
      dir = checkLib.router.config.chalkos.outputs.frr.dir;
      golden = ../../cmd/chalkos-render/testdata/router/edge.frr;
    in
    unitCheck "frr-credentials" [ pkgs.diffutils ] ''
      fail() {
        echo "error: $*" >&2
        exit 1
      }
      filled=' neighbor peer-fabric password example-password'
      mkdir -p /run/frr ${dir} credentials
      export CREDENTIALS_DIRECTORY=$PWD/credentials
      # Placeholders the renderer never writes outside a peer group's password line.
      sed 's/^\( neighbor peer-fabric password .*\)$/\1\n neighbor peer-fabric description x password @secret_bgp.fabric@\n neighbor 203.0.113.254 password @secret_bgp.fabric@/' ${golden} >${dir}/frr.conf
      grep -c '@secret_bgp.fabric@' ${dir}/frr.conf | grep -qx 3 || fail "the input lacks the placeholders"
      printf 'example-password' >credentials/secret_bgp.fabric
      ${fill} || fail "the credentials of the edge example were not filled in"
      grep -qx -- "$filled" /run/frr/frr.conf || fail "the password was not filled in"
      [ "$(stat -c %a:%u:%g /run/frr/frr.conf)" = 600:0:0 ] || fail "the filled config is not root's alone"
      diff <(grep -vx -- "$filled" /run/frr/frr.conf) <(grep -vx ' neighbor peer-fabric password @secret_bgp.fabric@' ${dir}/frr.conf) || fail "the fill changed more than the peer group's password"
      grep -qx ' neighbor peer-fabric description x password @secret_bgp.fabric@' /run/frr/frr.conf || fail "the fill replaced a placeholder in a description"
      [ "$(find /run/frr -name 'frr.conf.*' | wc -l)" = 0 ] || fail "the fill left a temporary file"
      cp /run/frr/frr.conf good.conf
      if env -u CREDENTIALS_DIRECTORY ${fill} 2>err; then fail "the fill took a placeholder without credentials"; fi
      grep -q 'frr.service has no credentials' err || fail "the fill without credentials failed for another reason: $(cat err)"
      cmp -s good.conf /run/frr/frr.conf || fail "a fill without credentials changed the filled config"

      for bad in $'two\nlines' $'trailing\n' 'two words' $'tab\there' "$(printf 'x%.0s' $(seq 81))" ''' 'ünïcode'; do
        printf '%s' "$bad" >credentials/secret_bgp.fabric
        if ${fill} 2>err; then fail "the fill took the password $bad"; fi
        grep -q 'FRR takes a password of 1 to 80 printable characters' err || fail "the fill refused $bad for another reason: $(cat err)"
        cmp -s good.conf /run/frr/frr.conf || fail "a refused password changed the filled config"
      done
      printf 'nul\0byte' >credentials/secret_bgp.fabric
      if ${fill} 2>err; then fail "the fill took a password with a NUL"; fi
      grep -q 'FRR takes a password of 1 to 80 printable characters' err || fail "the fill refused a NUL for another reason: $(cat err)"
      [ "$(find /run/frr -name 'frr.conf.*' | wc -l)" = 0 ] || fail "a refused fill left a temporary file"
      rm credentials/secret_bgp.fabric
      if ${fill} 2>err; then fail "the fill took a credential the unit lacks"; fi
      grep -q 'names the credential secret_bgp.fabric, which frr.service does not have' err || fail "a missing credential was refused for another reason: $(cat err)"
      cmp -s good.conf /run/frr/frr.conf || fail "a missing credential changed the filled config"

      # Without a rendered config, as in maintenance mode, FRR runs no BGP.
      rm ${dir}/frr.conf
      ${fill} || fail "the fallback config was not written"
      grep -qx 'frr defaults traditional' /run/frr/frr.conf || fail "the fallback config is not FRR's"
      if grep -q '^router bgp' /run/frr/frr.conf; then fail "the fallback config runs BGP"; fi
    '';
}
