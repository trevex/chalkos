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
  # changes anything, so a file jool or the link's form refuses, or an explicit pool that holds an
  # address of one of the router's links, changes no instance. Each instance runs in the network
  # namespace nat64-<name>, joined by the veth pair n64-<name>, in the instance's VRF, and nat64,
  # in the namespace: Jool's netfilter hook takes the packets of every link of its namespace, so
  # an instance of the router's own namespace would translate other VRFs' packets too. The
  # namespace forwards nothing itself: Jool sends what it translates, and a packet it leaves alone
  # would go back to the router, which routes it to the namespace again, until its hop limit ran
  # out. A link whose other end is not in the namespace, as when a namespace was removed while a
  # process held it, is made anew, and a device of its name and another kind fails the instance.
  # A VRF networkd has not given the file's table within 30 s fails the instance, and the other
  # instances of that VRF at once. A file's instance is created, or replaced in place, which keeps
  # its sessions; Jool keeps an instance's pool6, so an instance of another prefix is removed first
  # and loses its sessions. The VRF's table routes the prefix and an explicit pool's prefixes to
  # the link, with protocol 64, which BGP announces when a network names it. Instances of the
  # router's own namespace, routes of protocol 64, links and namespaces that no file names are
  # removed, in that order, so no route is gone with its link before its removal; with the argument
  # stop, every one is. Without the output's directory, as in maintenance mode, no file names one.
  # A step that fails leaves the others to run, and the script then fails, naming each failed step;
  # an instance that failed keeps the routes its file names.
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
      # Per instance: its prefix, VRF (empty for the default one), table, outer and inner address
      # and IPv4 routes; keep holds "<table> <destination>" of each route a file names.
      declare -A want=() pool6=() vrfs=() tables=() outers=() inners=() ipv4=() keep=()
      # Per VRF and table, "<vrf> <table>": 1 when the VRF has the table, empty when it had not
      # after 30 s.
      declare -A tabled=()
      failed=()
      files=()
      if [[ ''${1:-} != stop && -d $dir ]]; then
        shopt -s nullglob
        files=("$dir"/*.json)
        shopt -u nullglob
      fi
      # The form of a link's file as chalkos-render writes it; a VRF's name is an interface's.
      valid='(.table | type == "number")
        and (.vrf == null or ((.vrf | type == "string") and (.vrf | test("^[a-z0-9][-a-z0-9.]{0,14}$"))))
        and (.outer | type == "string") and (.inner | type == "string")
        and (.routes | type == "array" and all(type == "string" and test("^[0-9]{1,3}([.][0-9]{1,3}){3}/[0-9]{1,2}$")))'
      for file in "''${files[@]}"; do
        name=$(basename "$file" .json)
        veth=$dir/$name.veth
        if ! jool file check "$file" || ! jq -e "$valid" "$veth" >/dev/null; then
          echo "chalkos-nat64: jool refuses $file, or $veth is no link's file; no instance changed" >&2
          exit 1
        fi
        want[$name]=1
        pool6[$name]=$(jq -er .global.pool6 "$file")
        vrfs[$name]=$(jq -r '.vrf // ""' "$veth")
        tables[$name]=$(jq -r .table "$veth")
        outers[$name]=$(jq -r .outer "$veth")
        inners[$name]=$(jq -r .inner "$veth")
        ipv4[$name]=$(jq -r '.routes[]' "$veth")
        keep["''${tables[$name]} ''${pool6[$name]}"]=1
        mapfile -t pools < <(printf '%s' "''${ipv4[$name]}")
        for route in "''${pools[@]}"; do
          keep["''${tables[$name]} $route"]=1
        done
      done
      # The IPv4 address $1 as a number.
      number() {
        local IFS=. b
        read -r -a b <<<"$1"
        echo $(((b[0] << 24) | (b[1] << 16) | (b[2] << 8) | b[3]))
      }
      # The router answers its own addresses before any route to a NAT64's link, so a pool that
      # holds an address of one of the router's links, such as one from DHCP, which the rules
      # cannot see, would translate nothing: the run fails before it changes anything.
      if ! out=$(ip -4 -j addr show | jq -r '.[] | .ifname as $link | .addr_info[] | "\(.local) \($link)"'); then
        echo "chalkos-nat64: cannot list the router's addresses; no instance changed" >&2
        exit 1
      fi
      mapfile -t hosts < <(printf '%s' "$out")
      for file in "''${files[@]}"; do
        name=$(basename "$file" .json)
        mapfile -t pools < <(printf '%s' "''${ipv4[$name]}")
        for route in "''${pools[@]}"; do
          mask=$(((0xffffffff << (32 - ''${route#*/})) & 0xffffffff))
          net=$(($(number "''${route%/*}") & mask))
          for host in "''${hosts[@]}"; do
            if ((($(number "''${host% *}") & mask) == net)); then
              echo "chalkos-nat64: NAT64 $name: the pool entry $route holds ''${host% *}, an address of the link ''${host#* }, which the router answers itself; leave pool4 empty to take the address of the link the connections leave through; no instance changed" >&2
              exit 1
            fi
          done
        done
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
        local name=$1 vrf=$2 table=$3 outer=$4 inner=$5 link=n64-$1 a addrs out current kind
        if [[ ! -e /run/netns/nat64-$name ]]; then
          ip netns add "nat64-$name" || return
        fi
        # A namespace takes the router's IPv4 forwarding when it is created.
        inside "$name" sysctl -q -w net.ipv4.conf.all.forwarding=0 net.ipv4.conf.default.forwarding=0 \
          net.ipv6.conf.all.forwarding=0 net.ipv6.conf.default.forwarding=0 || return
        if ! ip link show dev "$link" >/dev/null 2>&1 || ! inside "$name" ip link show dev "$peer" >/dev/null 2>&1; then
          # Either end without the other is no link of this namespace.
          if ip link show dev "$link" >/dev/null 2>&1; then
            # A device of another kind is none of the unit's links.
            kind=$(ip -d -j link show dev "$link" | jq -r '.[0].linkinfo.info_kind // "none"') || return
            if [[ $kind != veth ]]; then
              echo "chalkos-nat64: $link is a device of kind $kind, not a NAT64's link; remove or rename it" >&2
              return 1
            fi
            ip link del "$link" || return
          fi
          if inside "$name" ip link show dev "$peer" >/dev/null 2>&1; then
            inside "$name" ip link del "$peer" || return
          fi
          ip link add "$link" mtu 65535 type veth peer name "$peer" mtu 65535 netns "nat64-$name" || return
        fi
        if [[ -n $vrf ]]; then
          # networkd creates a VRF without waiting, at boot and when a delivery changed its table.
          # The NAT64s of a VRF that timed out fail at once.
          if [[ -z ''${tabled["$vrf $table"]+x} ]]; then
            tabled["$vrf $table"]=
            for _ in $(seq 150); do
              current=$(ip -d -j link show dev "$vrf" 2>/dev/null | jq -r '.[0].linkinfo.info_data.table // empty') || current=
              if [[ $current == "$table" ]]; then
                tabled["$vrf $table"]=1
                break
              fi
              sleep 0.2
            done
          fi
          if [[ -z ''${tabled["$vrf $table"]} ]]; then
            echo "chalkos-nat64: VRF $vrf has no table $table after 30 s" >&2
            return 1
          fi
          ip link set dev "$link" master "$vrf" || return
        else
          ip link set dev "$link" nomaster || return
        fi
        out=$(ip -4 -j addr show dev "$link" | jq -r '.[].addr_info[] | "\(.local)/\(.prefixlen)"') || return
        mapfile -t addrs < <(printf '%s' "$out")
        for a in "''${addrs[@]}"; do
          if [[ $a != "$outer" ]]; then
            ip addr del "$a" dev "$link" || return
          fi
        done
        ip addr replace "$outer" dev "$link" || return
        ip -6 addr replace fe80::1/64 dev "$link" nodad || return
        ip link set dev "$link" up || return
        inside "$name" ip link set dev lo up || return
        out=$(inside "$name" ip -4 -j addr show dev "$peer" | jq -r '.[].addr_info[] | "\(.local)/\(.prefixlen)"') || return
        mapfile -t addrs < <(printf '%s' "$out")
        for a in "''${addrs[@]}"; do
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
        table=''${tables[$name]}
        if ! plumb "$name" "''${vrfs[$name]}" "$table" "''${outers[$name]}" "''${inners[$name]}"; then
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
        mapfile -t pools < <(printf '%s' "''${ipv4[$name]}")
        for route in "''${pools[@]}"; do
          ip -4 route replace "$route" via "''${inners[$name]%/*}" dev "n64-$name" table "$table" proto "$proto" ||
            failed+=("route $route")
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
      for family in -4 -6; do
        # ip -N gives each table's number, and leaves out the table of a route of main.
        routes=$(ip -N "$family" -j route show table all proto "$proto" | jq -r '.[] | "\(.table // "254") \(.dst)"') ||
          failed+=("list routes")
        while read -r table dst; do
          # ip prints a route to one address without its length, which the rendered files give.
          if [[ -n $dst && $dst != */* ]]; then
            if [[ $family == -4 ]]; then
              dst+=/32
            else
              dst+=/128
            fi
          fi
          if [[ -n $dst && -z ''${keep["$table $dst"]+x} ]]; then
            ip "$family" route del "$dst" table "$table" proto "$proto" || failed+=("remove route $dst of table $table")
          fi
        done <<<"$routes"
      done
      # ip link show type veth lists every link without a kind, as empty objects, while the
      # kernel's veth module is not loaded, so jq picks the veth links from all links.
      out=$(ip -d -j link show | jq -r '.[] | select(.linkinfo.info_kind == "veth") | .ifname | select(startswith("n64-"))') ||
        failed+=("list links")
      mapfile -t links < <(printf '%s' "$out")
      for link in "''${links[@]}"; do
        if [[ -z ''${want[''${link#n64-}]+x} ]]; then
          ip link del "$link" || failed+=("remove the link $link")
        fi
      done
      shopt -s nullglob
      for ns in /run/netns/nat64-*; do
        name=''${ns#/run/netns/nat64-}
        if [[ -z ''${want[$name]+x} ]]; then
          ip netns delete "nat64-$name" || failed+=("remove the namespace of $name")
        fi
      done
      shopt -u nullglob
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
