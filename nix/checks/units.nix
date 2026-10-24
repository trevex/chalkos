# Checks that run the worker's units' scripts as on a node, in namespaces of their own.
{ pkgs, checkLib, ... }:
let
  inherit (pkgs) lib;
  inherit (checkLib) worker firewall unitCheck;
in
{
  # The worker's VXLAN rule with its firewall, in a network namespace: a peer sends UDP to port
  # 8472 over a veth pair, and the node receives what the firewall accepts. The rule reads the
  # cluster's ranges from the Cluster documents the test writes.
  vxlan-rule =
    let
      rule = lib.getExe worker.config.system.build.chalkosVxlanRule;
      # The rule of an image without IPv6.
      ipv4Rule = lib.getExe (worker.config.system.build.chalkosVxlanRule.override { ipv6 = false; });
    in
    unitCheck "vxlan-rule" checkLib.networkTools ''
      fail() {
        echo "error: $*" >&2
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
      # Runs the script with the arguments, setting status; the rules it prints go to rules.log.
      run() {
        status=0
        ${rule} "$@" >rules.log 2>errors.log || status=$?
      }
      # Runs the script with the file holding the content, with escapes. Without source lines
      # VXLAN may come from any source.
      holding() {
        printf '%b' "$1" >vxlan
        run vxlan
      }
      v4='destination 10.0.0.11 interface\n'
      v6='destination fd00::11 interface\n'
      # The node's Cluster document with the spec given, its Flannel document with the source
      # ranges given, and both of a dual-stack cluster with the source ranges given.
      cluster=/run/chalkos/config/kubernetes.chalkos.dev/Cluster/default.json
      flannel=/run/chalkos/config/kubernetes.chalkos.dev/Flannel/default.json
      mkdir -p "$(dirname "$cluster")" "$(dirname "$flannel")"
      spec() {
        printf '{"apiVersion":"kubernetes.chalkos.dev/v1alpha1","kind":"Cluster","metadata":{"name":"default"},"spec":%s}\n' "$1" >"$cluster"
      }
      sources() {
        printf '{"apiVersion":"kubernetes.chalkos.dev/v1alpha1","kind":"Flannel","metadata":{"name":"default"},"spec":{"vxlanSourceSubnets":%s}}\n' "$1" >"$flannel"
      }
      ranges() {
        spec '{"ipFamilies":["ipv4","ipv6"],"podCIDRs":{"ipv4":"10.244.0.0/16","ipv6":"fd00:10:244::/56"}}'
        sources "$1"
      }
      ranges '["10.0.0.0/24","fd00::/64"]'
      rules() {
        grep -c 'meta mark set meta mark |' rules.log || true
      }
      # The rules in the table now, which may differ from what the script printed.
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

      # Ranges that cannot be read, and anything but ranges, drop all VXLAN the node forwards,
      # and say so; the node still takes VXLAN.
      closed() {
        holding "$v4$v6"
        [ "$status" = 0 ] && [ "$(rules)" = 2 ] || fail "the addresses $1"
        grep -q "$2" errors.log || fail "no warning $1"
        if forwarded 10.0.2.12 8472; then fail "a pod sent VXLAN outside the source ranges $1"; fi
        if forwarded fd00:2::12 8472; then fail "a pod sent IPv6 VXLAN outside the source ranges $1"; fi
        forwarded 10.0.2.12 8473 || fail "a pod's datagram to another port was dropped $1"
        arrives 10.0.0.11 || fail "VXLAN to the node was refused $1"
      }
      rm "$cluster"
      closed "without a Cluster document" "ranges are unknown"
      echo '{' >"$cluster"
      closed "with a document chalkcfg cannot read" "ranges are unknown"
      spec '{"ipFamilies":["ipv4","ipv6"],"podCIDRs":{"ipv4":"10.244.0.0/16"}}'
      closed "with a family without a pod range" "ranges are unknown"
      sources '[]'
      for s in \
        '{"ipFamilies":["ipv4"],"podCIDRs":{"ipv4":"10.0.0.0/8 }"}}' \
        '{"ipFamilies":["ipv4"],"podCIDRs":{"ipv4":null}}' \
        '{"ipFamilies":["ipv4"],"podCIDRs":{"ipv4":"10.244.0.0/16 10.0.0.0/8"}}'; do
        spec "$s"
        closed "with the spec $s" "ranges hold"
      done
      spec '{"ipFamilies":["ipv4"],"podCIDRs":{"ipv4":"10.244.0.0/16"}}'
      for s in '["cafe.be/8"]' '["10.0.0.0/24\nflush 10.0.0.0/8"]'; do
        sources "$s"
        closed "with the source ranges $s" "ranges hold"
      done
      echo '{' >"$flannel"
      closed "with a Flannel document chalkcfg cannot read" "ranges are unknown"
      # Without a Flannel document VXLAN may come from anywhere, so nothing is dropped.
      rm "$flannel"
      holding "$v4$v6"
      [ ! -s errors.log ] || fail "warned without a Flannel document: $(cat errors.log)"
      forwarded 10.0.0.12 8472 || fail "a pod's VXLAN was dropped without a Flannel document"
      ranges '["10.0.0.0/24","fd00::/64"]'
      holding "$v4$v6"
      if forwarded 10.0.0.12 8472; then fail "a pod sent VXLAN to a source range after the ranges came back"; fi
      forwarded 10.0.2.12 8472 || fail "a pod's VXLAN outside the source ranges was dropped after the ranges came back"

      # An image without IPv6 skips the IPv6 ranges, the cluster's and the file's alike.
      printf '%b' "$v4"'source 10.0.0.0/24\nsource fd00::/64\n' >vxlan
      ${ipv4Rule} vxlan >rules.log 2>errors.log || fail "an image without IPv6 refused IPv6 ranges"
      [ ! -s errors.log ] || fail "an image without IPv6 warned of IPv6 ranges: $(cat errors.log)"
      [ "$(rules)" = 1 ] || fail "an image without IPv6"
      arrives 10.0.0.11 || fail "an image without IPv6 refused VXLAN from a source range"
      if forwarded 10.0.0.12 8472; then fail "a pod sent VXLAN to a source range on an image without IPv6"; fi
      forwarded fd00::12 8472 || fail "an image without IPv6 dropped IPv6 VXLAN it forwards"
      holding "$v4$v6"

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

      # Anything but a destination line per family, of a bare address and a mode, and source
      # lines of a range each, adds no rule, empties the table and fails.
      for content in "" '\n' '10.0.0.11\n' 'destination 10.0.0.11\n' 'destination 10.0.0.11 eth0\n' \
        'destination 10.0.0.0/8 interface\n' "$v4"'destination 10.0.0.12 any\n' "$v6"'destination fd00::12 interface\n' \
        "$v4"'destination fd00::11 interface accept\n' 'destination 10.0.0.11 interface accept\n' \
        'destination fd00::11 }\n' 'destination  10.0.0.11 interface\n' 'destination 10.0.0.11 interface \n' \
        'Destination 10.0.0.11 interface\n' 'source 10.0.0.0/24\n' "$v4"'\n' 'destination ::ffff: interface\n' \
        'destination eth0 interface\n' 'destination cafe.be interface\n' 'destination cafe any\n' \
        'destination 10.0.0 interface\n' 'destination 10.0.0.11.12 interface\n' 'destination 10.0.0.256 interface\n' \
        'destination 10.0.0.0x1 interface\n' 'destination 10.0..11 interface\n' 'destination ::: interface\n' \
        "$v4"'source 10.0.0.0\n' "$v4"'source 10.0.0.0/33\n' "$v4"'source fd00::/129\n' "$v4"'source 10.0.0.0/024\n' \
        "$v4"'source 10.0.0.0/\n' "$v4"'source cafe/8\n' "$v4"'source 10.0.0.0/24 accept\n' "$v4"'source 10.0.0.0/24 }\n' \
        "$v4"'source  10.0.0.0/24\n' "$v4"'source 10.0.0.0/8/8\n' "$v4"'source 10.0.0.256/24\n' \
        'destination a:b interface\n' 'destination cafe:babe interface\n' 'destination 1::2::3 interface\n' \
        'destination 12345:: interface\n' 'destination fe80::1%v0 interface\n' 'destination 1:2:3:4:5:6:7:8:9 interface\n' \
        'destination 1:2:3:4:5:6:7::8 interface\n' 'destination :1::2 interface\n' 'destination 1::2: interface\n' \
        'destination ::10.0.0 interface\n' 'destination fd00:10.0.0.11 interface\n' 'destination ::10.0.0.11:1 interface\n' \
        'destination 10.0.0.011 interface\n' "$v4"'source a:b/64\n' "$v4"'source 1::2::3/64\n'; do
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
}
