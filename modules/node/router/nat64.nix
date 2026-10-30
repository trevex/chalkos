# NAT64 on router images: Jool's kernel module and tool, the output jool, which renders an
# instance of Jool and its link per NAT64 document, and chalkos-nat64.service, which brings Jool's
# instances, their namespaces and links and the routes to them to the rendered files at boot and
# on each delivery that changes them.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  jool = pkgs.jool-cli;
  # Brings Jool's instances, their namespaces and links and the routes to them to the files of the
  # output jool: <name>.json is Jool's, <name>.veth the link's. It checks every file before it
  # changes anything, so a file jool refuses changes no instance. Each instance runs in the network
  # namespace nat64-<name>, joined by the veth pair n64-<name>, in the instance's VRF, and nat64,
  # in the namespace: Jool's netfilter hook takes the packets of every link of its namespace, so an
  # instance of the router's own namespace would translate other VRFs' packets too. The namespace
  # forwards nothing itself: Jool sends what it translates, and a packet it leaves alone would go
  # back to the router, which routes it to the namespace again, until its hop limit ran out. A
  # file's instance is created, or replaced in place, which keeps its
  # sessions; Jool keeps an instance's pool6, so an instance of another prefix is removed first and
  # loses its sessions. The VRF's table routes the prefix and an explicit pool's prefixes to the
  # link, with protocol 64, which BGP announces when a network names it. Namespaces, instances of
  # the router's own namespace and routes of protocol 64 that no file names are removed; with the
  # argument stop, every one is. Removing a namespace removes its link and its instance. Without
  # the output's directory, as in maintenance mode, no file names one. A step that fails leaves the
  # others to run, and the script then fails, naming each failed step.
  apply = pkgs.writeShellApplication {
    name = "chalkos-nat64";
    runtimeInputs = [
      jool
      pkgs.iproute2
      pkgs.util-linux
      pkgs.procps
      pkgs.jq
      pkgs.gnused
      pkgs.coreutils
    ];
    text = ''
      dir=${config.chalkos.outputs.jool.dir}
      proto=64
      # The end of each veth pair in the instance's namespace.
      peer=nat64
      declare -A want=() pool6=() keep=()
      failed=()
      files=()
      if [[ ''${1:-} != stop && -d $dir ]]; then
        shopt -s nullglob
        files=("$dir"/*.json)
        shopt -u nullglob
      fi
      for file in "''${files[@]}"; do
        name=$(basename "$file" .json)
        if ! jool file check "$file" ||
          ! jq -e '(.table | type == "number") and (.outer | type == "string") and (.inner | type == "string") and (.routes | type == "array")' "$dir/$name.veth" >/dev/null; then
          echo "chalkos-nat64: jool refuses $file, or $dir/$name.veth is no link's file; no instance changed" >&2
          exit 1
        fi
        want[$name]=1
        pool6[$name]=$(jq -er .global.pool6 "$file")
      done
      # Runs a command in the namespace of the instance $1.
      inside() {
        local ns=/run/netns/nat64-$1
        shift
        nsenter --net="$ns" "$@"
      }
      # Brings up the namespace of the instance $1 and its veth pair: $2 the VRF, empty for the
      # default one, of the table $3; $4 and $5 the outer and the inner address.
      plumb() {
        local name=$1 vrf=$2 table=$3 outer=$4 inner=$5 link=n64-$1 a
        if [[ ! -e /run/netns/nat64-$name ]]; then
          ip netns add "nat64-$name" || return
        fi
        # A namespace takes the router's IPv4 forwarding when it is created.
        inside "$name" sysctl -q -w net.ipv4.conf.all.forwarding=0 net.ipv4.conf.default.forwarding=0 \
          net.ipv6.conf.all.forwarding=0 net.ipv6.conf.default.forwarding=0 || return
        if ! ip link show dev "$link" >/dev/null 2>&1; then
          ip link add "$link" mtu 65535 type veth peer name "$peer" mtu 65535 netns "nat64-$name" || return
        fi
        if [[ -n $vrf ]]; then
          # networkd creates a VRF without waiting, at boot and when a delivery changed its table.
          for _ in $(seq 150); do
            [[ $(ip -d -j link show dev "$vrf" 2>/dev/null | jq -r '.[0].linkinfo.info_data.table // empty') == "$table" ]] && break
            sleep 0.2
          done
          ip link set dev "$link" master "$vrf" || return
        else
          ip link set dev "$link" nomaster || return
        fi
        for a in $(ip -4 -j addr show dev "$link" | jq -r '.[].addr_info[] | "\(.local)/\(.prefixlen)"'); do
          if [[ $a != "$outer" ]]; then
            ip addr del "$a" dev "$link" || return
          fi
        done
        ip addr replace "$outer" dev "$link" || return
        ip -6 addr replace fe80::1/64 dev "$link" nodad || return
        ip link set dev "$link" up || return
        inside "$name" ip link set dev lo up || return
        for a in $(inside "$name" ip -4 -j addr show dev "$peer" | jq -r '.[].addr_info[] | "\(.local)/\(.prefixlen)"'); do
          if [[ $a != "$inner" ]]; then
            inside "$name" ip addr del "$a" dev "$peer" || return
          fi
        done
        inside "$name" ip addr replace "$inner" dev "$peer" || return
        inside "$name" ip -6 addr replace fe80::2/64 dev "$peer" nodad || return
        inside "$name" ip link set dev "$peer" up || return
        inside "$name" ip route replace default via "''${outer%/*}" dev "$peer" || return
        inside "$name" ip -6 route replace default via fe80::1 dev "$peer"
      }
      for file in "''${files[@]}"; do
        name=$(basename "$file" .json)
        veth=$dir/$name.veth
        vrf=$(jq -r '.vrf // ""' "$veth")
        table=$(jq -r .table "$veth")
        outer=$(jq -r .outer "$veth")
        inner=$(jq -r .inner "$veth")
        if ! plumb "$name" "$vrf" "$table" "$outer" "$inner"; then
          failed+=("bring up the namespace and link of $name")
          continue
        fi
        current=$( (inside "$name" jool -i "$name" global display --csv 2>/dev/null || true) | sed -n 's/^pool6,//p')
        if [[ -n $current && $current != "''${pool6[$name]}" ]]; then
          inside "$name" jool -i "$name" instance remove || failed+=("remove instance $name of $current")
        fi
        inside "$name" jool file handle "$file" || failed+=("apply instance $name")
        ip -6 route replace "''${pool6[$name]}" via fe80::2 dev "n64-$name" table "$table" proto "$proto" ||
          failed+=("route ''${pool6[$name]}")
        keep["$table ''${pool6[$name]}"]=1
        for route in $(jq -r '.routes[]' "$veth"); do
          ip -4 route replace "$route" via "''${inner%/*}" dev "n64-$name" table "$table" proto "$proto" ||
            failed+=("route $route")
          keep["$table $route"]=1
        done
      done
      # An instance of the router's own namespace, such as one an older image ran, takes every
      # link's packets; jool lists the instances of every namespace, and global display finds those
      # of the unit's own alone.
      instances=$(jool instance display --csv --no-headers) || failed+=("list instances")
      while IFS=, read -r _ name _; do
        if [[ -n $name ]] && jool -i "$name" global display >/dev/null 2>&1; then
          jool -i "$name" instance remove || failed+=("remove instance $name of the router's namespace")
        fi
      done <<<"$instances"
      shopt -s nullglob
      for ns in /run/netns/nat64-*; do
        name=''${ns#/run/netns/nat64-}
        if [[ -z ''${want[$name]+x} ]]; then
          ip netns delete "nat64-$name" || failed+=("remove the namespace of $name")
        fi
      done
      shopt -u nullglob
      for family in -4 -6; do
        # ip leaves out the table of a route of main and the type of a unicast route.
        routes=$(ip "$family" -j route show table all proto "$proto" | jq -r '.[] | "\(.table // "main") \(.type // "unicast") \(.dst)"') ||
          failed+=("list routes")
        while read -r table type dst; do
          if [[ $table == main ]]; then
            table=254
          fi
          # ip prints a route to one address without its length, which the rendered files give.
          if [[ -n $dst && $dst != */* ]]; then
            if [[ $family == -4 ]]; then
              dst+=/32
            else
              dst+=/128
            fi
          fi
          if [[ -n $dst && -z ''${keep["$table $dst"]+x} ]]; then
            ip "$family" route del "$type" "$dst" table "$table" proto "$proto" || failed+=("remove route $dst of table $table")
          fi
        done <<<"$routes"
      done
      if ((''${#failed[@]} > 0)); then
        printf 'chalkos-nat64: failed to %s\n' "''${failed[@]}" >&2
        exit 1
      fi
    '';
  };
in
{
  config = lib.mkIf config.chalkos.router.enable {
    boot.extraModulePackages = [ config.boot.kernelPackages.jool ];
    # Loaded at boot, so the image's module check proves the tree holds it and what it depends on.
    boot.kernelModules = [ "jool" ];

    chalkos.outputs.jool = {
      # The VRFs' tables and the links' membership, and the Networks' static addresses, which a
      # pool must not hold.
      inputs = [
        "NAT64"
        "VRF"
        "Network"
      ];
      render = "${lib.getExe chalkosPackages.chalkos-render} jool";
      # jool file check reads a file as file handle does, without the kernel.
      check = ''for f in "$OUT"/*.json; do [ ! -e "$f" ] || ${jool}/bin/jool file check "$f" || exit; done'';
      examples = ../examples/router;
    };
    chalkos.effects.jool = {
      outputs = [ "jool" ];
      reload = [ "chalkos-nat64.service" ];
    };
    # The firewall forwards, translates and tracks the NAT64s' links, and networkd leaves them
    # alone.
    chalkos.outputs.router-nftables.inputs = [ "NAT64" ];
    chalkos.outputs.networkd.inputs = [ "NAT64" ];

    systemd.services.chalkos-nat64 = {
      description = "Run the router's NAT64 instances";
      # Within one delivery the firewall and networkd reload first, then the instances and their
      # routes, then FRR, which announces a prefix once its route exists. networkd creates a VRF the
      # instance's link joins.
      after = [
        "chalkos-config.service"
        "chalkos-firewall.service"
        "systemd-networkd.service"
      ];
      before = [ "frr.service" ];
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = lib.getExe apply;
        ExecReload = lib.getExe apply;
        ExecStop = "${lib.getExe apply} stop";
        # ip netns add mounts a namespace on /run/netns, which the host must see, so the unit has no
        # mount namespace of its own: no ProtectSystem, PrivateTmp, PrivateDevices, ProtectHome,
        # ProtectKernelLogs, ProtectKernelModules, ProtectControlGroups or ProtectKernelTunables,
        # each of which gives it one and keeps the namespaces from the host. Mounting a namespace and
        # entering it take CAP_SYS_ADMIN and the mount system calls, and RestrictNamespaces lets
        # network namespaces alone through; jool, ip and the namespaces' sysctls take CAP_NET_ADMIN.
        CapabilityBoundingSet = [
          "CAP_NET_ADMIN"
          "CAP_SYS_ADMIN"
        ];
        RestrictAddressFamilies = [
          "AF_NETLINK"
          "AF_UNIX"
        ];
        RestrictNamespaces = "net";
        SystemCallFilter = [
          "@system-service"
          "@mount"
        ];
        NoNewPrivileges = true;
        MemoryDenyWriteExecute = true;
        RestrictSUIDSGID = true;
        ProtectClock = true;
        ProtectHostname = true;
        RestrictRealtime = true;
        LockPersonality = true;
        SystemCallArchitectures = "native";
      };
    };
    # chalkd reads Jool's sessions, bindings and pools for the node's status, with nsenter in each
    # instance's namespace.
    systemd.services.chalkd.path = [
      jool
      pkgs.util-linux
    ];
  };
}
