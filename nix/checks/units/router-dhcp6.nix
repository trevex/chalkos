# The dhcp6 example's DHCPv6 servers, the default VRF's and the VRF access's, as kea-dhcp6.service
# and kea-dhcp6@access.service run them, with their units' capabilities alone and access's bound
# to its VRF's device as BindNetworkInterface= does, behind the example's firewall, whose
# openings let DHCPv6 from link-local addresses in on the servers' links. A client on lan0, of
# the printer's MAC address in its DUID, gets its reserved address, the DNS server and the
# search domain; a home router on cust0, in the VRF access, gets a /56, which chalkos-dhcp6-pd
# records and chalkos-dhcp6-routes routes in access's table to the home router's link-local
# address. The diagnostic dhcp-leases lists both leases. A release removes the record and the route. The
# routes run as root with the unit's capabilities alone and trust no record: an ended record,
# one of a prefix no subnet delegates, a link to a record, a FIFO, a record too long and a
# records' directory that is a link route nothing, block no run and lose no file. The dhcp
# example's firewall, which opens no DHCPv6, keeps the client from its address.
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
  kea6 = lib.head (lib.splitString " " router.systemd.services.kea-dhcp6.serviceConfig.ExecStart);
  inherit (router.systemd.services.kea-dhcp6.serviceConfig) Environment;
  keaCaps = boundTo router.systemd.services.kea-dhcp6.serviceConfig.CapabilityBoundingSet;
  pdHook = lib.last (lib.splitString " " router.chalkos.outputs.kea.render);
  routesUnit = router.systemd.services.chalkos-dhcp6-routes.serviceConfig;
  # chalkos-dhcp6-routes as its unit runs it: root with the unit's capabilities alone, and no
  # longer than the unit lets it.
  routes = "timeout ${routesUnit.TimeoutStartSec} ${boundTo routesUnit.CapabilityBoundingSet} ${routesUnit.ExecStart}";
  records = router.systemd.paths.chalkos-dhcp6-routes.pathConfig.PathChanged;
  firewall = router.systemd.services.chalkos-firewall.serviceConfig.ExecStart;
  dir = router.chalkos.outputs.kea.dir;
  nftDir = router.chalkos.outputs.router-nftables.dir;
  leases = lib.elemAt router.chalkos.diagnostics.dhcp-leases.command 2;
  goldens = ../../../cmd/chalkos-render/testdata/router;
in
unitCheck "router-dhcp6"
  (
    checkLib.networkTools
    ++ [
      pkgs.dhcpcd
      pkgs.jq
    ]
  )
  ''
    debug() {
      cat kea-*.log >&2
    }
    ip link set lo up
    # The namespaces of the client on lan0 and of the home router on cust0.
    netns pc cpe
    ip link add access type vrf table 500
    ip link set access up
    ip link add lan0 type veth peer name eth0 netns "''${pids[pc]}"
    ip link add cust0 type veth peer name eth0 netns "''${pids[cpe]}"
    ip link set cust0 master access
    for l in lan0:2001:db8:1::1 cust0:2001:db8:2::1; do
      link=''${l%%:*}
      echo 0 >"/proc/sys/net/ipv6/conf/$link/accept_dad"
      ip addr add "''${l#*:}/64" dev "$link" nodad
      ip link set "$link" up
    done
    for ns in pc cpe; do
      "on_$ns" ip link set lo up
      "on_$ns" sh -c 'echo 0 >/proc/sys/net/ipv6/conf/eth0/accept_dad'
      "on_$ns" ip link set eth0 up
    done
    on_pc ip link set eth0 address 52:54:00:12:34:56

    mkdir -p ${dir} ${nftDir} /var/lib /var/run/dhcpcd /var/db/dhcpcd /var/lib/dhcpcd
    mkdir -m 0750 /run/kea /var/lib/kea
    mkdir -m 0755 ${records}
    mkdir -m 0700 ${records}.new
    install -m 644 ${goldens}/dhcp6.nft ${nftDir}/ruleset.nft
    ${firewall} || fail "chalkos-firewall did not load the dhcp6 example's ruleset"
    # take <file>: a config of the dhcp6 example's golden, whose hook is the image's.
    take() {
      awk -v want="# $1" '$0 == want { take = 1; next } /^# / { take = 0 } take' ${goldens}/dhcp6.kea |
        jq --arg hook ${pdHook} '(.Dhcp6["hooks-libraries"][] | select(.library == "libdhcp_run_script.so") | .parameters.name) = $hook' >"${dir}/$1"
      grep -q '"Dhcp6"' "${dir}/$1" || fail "the dhcp6 example's golden holds no $1"
    }
    take kea-dhcp6.json
    take kea-dhcp6-access.json
    ${lib.concatMapStrings (e: "export ${lib.escapeShellArg e}\n") Environment}
    ${keaCaps} ${kea6} -c ${dir}/kea-dhcp6.json >kea-default.log 2>&1 &
    BIND_DEVICE=access LD_PRELOAD=${bindDevice} ${keaCaps} ${kea6} -c ${dir}/kea-dhcp6-access.json >kea-access.log 2>&1 &
    for s in dhcp6 dhcp6-access; do
      retry 100 "Kea opened no control socket $s.sock" test -S /run/kea/$s.sock
    done
    boundToAccess() { ss -Hlun | grep -q "\[ff05::1:3\]%access:547 "; }
    retry 50 "" boundToAccess || fail "the server of access is not bound to its VRF's device: $(ss -Hlun)"

    # What a client's dhcpcd got: its DNS servers and search domains. dhcpcd runs the script
    # with its own environment and directory, so the script names the file whole.
    cat >bound <<SCRIPT
    #!${pkgs.runtimeShell}
    case \$reason in BOUND6 | REBIND6 | RENEW6) ;; *) exit 0 ;; esac
    echo "dns=\$new_dhcp6_name_servers search=\$new_dhcp6_domain_search" >>"$PWD/got"
    SCRIPT
    chmod +x bound
    # lease <namespace> <ia lines>: asks for the IAs once, as the client of the namespace.
    lease() {
      local ns=$1
      shift
      rm -f /var/lib/dhcpcd/* got
      printf 'ipv6only\nnoipv6rs\npersistent\nduid ll\noption dhcp6_name_servers, dhcp6_domain_search\ninterface eth0\n%s\n' "$*" >"dhcpcd-$ns.conf"
      "on_$ns" timeout 15 dhcpcd -f "$PWD/dhcpcd-$ns.conf" -c "$PWD/bound" -B -1 -6 eth0 >"dhcpcd-$ns.log" 2>&1 || true
    }
    lease pc "  ia_na 1"
    on_pc ip -6 addr show dev eth0 | grep -q 'inet6 2001:db8:1::10/128' ||
      fail "the printer did not get its reserved address: $(cat dhcpcd-pc.log)"
    [ "$(cat got)" = "dns=2001:db8:1::1 search=lan.example.org" ] || fail "the printer got $(cat got)"

    # The home router keeps its prefix, so a second dhcpcd releases it below.
    rm -f /var/lib/dhcpcd/*
    printf 'ipv6only\nnoipv6rs\npersistent\nduid ll\ninterface eth0\n  ia_pd 2 -\n' >cpe.conf
    on_cpe dhcpcd -f "$PWD/cpe.conf" -B -6 eth0 >cpe.log 2>&1 &
    retry 100 "" grep -q 'delegated prefix 2001:db8:100::/56' cpe.log || fail "the home router got no prefix: $(cat cpe.log)"
    cpeLL=$(on_cpe ip -6 -o addr show dev eth0 scope link | awk '{print $4}' | cut -d/ -f1)
    retry 50 "" test -e ${records}/cust0_2001:db8:100::_56 || fail "chalkos-dhcp6-pd recorded no prefix: $(ls -A ${records} 2>&1)"
    read -r via _ <${records}/cust0_2001:db8:100::_56
    [ "$via" = "$cpeLL" ] || fail "the record routes to $via, not the home router's $cpeLL"
    ${routes} || fail "chalkos-dhcp6-routes failed"
    ip -6 route show table 500 proto 65 | grep -q "^2001:db8:100::/56 via $cpeLL dev cust0 " ||
      fail "access's table routes no prefix to the home router: $(ip -6 route show table 500)"

    # listed: the leases dhcp-leases lists, without the header and their ends.
    listed() { bash -c ${lib.escapeShellArg leases} dhcp-leases "$@" | tail -n +2 | cut -f 1-5; }
    [ "$(listed --subnet=customers | cut -f 1-3)" = "$(printf 'access\tcustomers\t2001:db8:100::/56')" ] ||
      fail "dhcp-leases lists no delegated prefix: $(listed)"
    [ "$(listed --mac=52:54:00:12:34:56 --subnet=office6)" = "$(printf 'default\toffice6\t2001:db8:1::10\t52:54:00:12:34:56\tprinter')" ] ||
      fail "dhcp-leases lists no lease of the printer: $(listed)"

    on_cpe dhcpcd -6 -k eth0 >/dev/null 2>&1 || true
    retry 50 "the release left the record" test ! -e ${records}/cust0_2001:db8:100::_56
    ${routes} || fail "chalkos-dhcp6-routes failed after the release"
    [ -z "$(ip -6 route show table 500 proto 65)" ] || fail "the release left the route: $(ip -6 route show table 500 proto 65)"
    # Records that route nothing: ended, of a prefix no subnet delegates, a link to a record, a
    # FIFO, which no one writes, and a record too long.
    later=$(($(date +%s) + 60))
    echo "fe80::1 $(($(date +%s) - 1))" >${records}/cust0_2001:db8:100:100::_56
    echo "fe80::1 $later" >${records}/cust0_2001:db9::_56
    mkdir elsewhere
    echo "fe80::1 $later" >elsewhere/cust0_2001:db8:100:200::_56
    ln -s "$PWD/elsewhere/cust0_2001:db8:100:200::_56" ${records}/cust0_2001:db8:100:200::_56
    mkfifo ${records}/cust0_2001:db8:100:300::_56
    printf 'fe80::1 %s%0200d\n' "$later" 0 >${records}/cust0_2001:db8:100:400::_56
    before=$(ls -A ${records})
    ${routes} || fail "chalkos-dhcp6-routes failed or blocked on records that route nothing"
    [ "$(ls -A ${records})" = "$before" ] || fail "records were removed: $(ls -A ${records})"
    [ -e elsewhere/cust0_2001:db8:100:200::_56 ] || fail "the record a link names was removed"
    [ -z "$(ip -6 route show table 500 proto 65)" ] ||
      fail "a record that routes nothing was routed: $(ip -6 route show table 500 proto 65)"
    # A records' directory that is a link to another directory is refused whole.
    mv ${records} records.real
    mkdir target
    echo "fe80::1 $later" >target/cust0_2001:db8:100:500::_56
    echo junk >target/junk
    ln -s "$PWD/target" ${records}
    if ${routes} 2>routes.log; then fail "chalkos-dhcp6-routes read a records' directory that is a link"; fi
    [ "$(ls -A target)" = "$(printf 'cust0_2001:db8:100:500::_56\njunk')" ] ||
      fail "files of the linked directory were removed: $(ls -A target)"
    [ -z "$(ip -6 route show table 500 proto 65)" ] || fail "a record of a linked directory was routed"
    rm ${records}
    mv records.real ${records}

    # Without the opening the client gets nothing.
    install -m 644 ${goldens}/dhcp.nft ${nftDir}/ruleset.nft
    ${firewall} || fail "chalkos-firewall did not load the dhcp example's ruleset"
    on_pc ip -6 addr flush dev eth0 scope global
    lease pc "  ia_na 1"
    if on_pc ip -6 addr show dev eth0 | grep -q 'inet6 2001:db8:'; then
      fail "the client got an address through a firewall without DHCPv6's opening"
    fi
  ''
