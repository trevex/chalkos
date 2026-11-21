# The dhcp6 example's kea-dhcp6 servers, of the default VRF and of the VRF access, with dhcpcd
# clients behind the example's firewall: a reservation, a delegated prefix that chalkos-dhcp6-pd
# records and chalkos-dhcp6-routes routes, and records that route nothing. Kea and the routes run
# as root with their units' capabilities alone.
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
  kea6 = lib.head (lib.splitString " " router.systemd.services.kea-dhcp6.serviceConfig.ExecStart);
  inherit (router.systemd.services.kea-dhcp6.serviceConfig) Environment;
  keaCaps = boundTo router.systemd.services.kea-dhcp6.serviceConfig.CapabilityBoundingSet;
  pdHook = lib.last (lib.splitString " " router.chalkos.outputs.kea.render);
  routesUnit = router.systemd.services.chalkos-dhcp6-routes.serviceConfig;
  # chalkos-dhcp6-routes as its unit runs it: root with the unit's capabilities alone, and no
  # longer than the unit lets it.
  routes = "timeout ${routesUnit.TimeoutStartSec} ${boundTo routesUnit.CapabilityBoundingSet} ${routesUnit.ExecStart}";
  records = router.systemd.paths.chalkos-dhcp6-routes.pathConfig.PathChanged;
  dir = router.chalkos.outputs.kea.dir;
  # The diagnostic's command without its parameters' arguments.
  leases = lib.concatStringsSep " " (lib.take 3 router.chalkos.diagnostics.dhcp-leases.command);
  inherit (checkLib) routerRendered;
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
    veth pc lan0 - 2001:db8:1::1/64
    veth cpe cust0 access 2001:db8:2::1/64
    on_pc ip link set eth0 address 52:54:00:12:34:56

    mkdir -p ${dir} /var/lib /var/run/dhcpcd /var/db/dhcpcd /var/lib/dhcpcd
    mkdir -m 0750 /run/kea /var/lib/kea
    mkdir -m 0755 ${records}
    mkdir -m 0700 ${records}.new
    ${routerFirewall.load "dhcp6"}
    # take <file>: a config the dhcp6 example renders, with the image's hook in place of the
    # test result's, whose store path has its hash written as e's.
    take() {
      local rendered=${routerRendered "dhcp6"}/kea/$1
      jq -e .Dhcp6 "$rendered" >/dev/null || fail "the dhcp6 example renders no DHCPv6 server in $1"
      jq --arg hook ${pdHook} \
        '(.Dhcp6["hooks-libraries"][] | select(.library == "libdhcp_run_script.so")
          | .parameters.name) = $hook' \
        "$rendered" >"${dir}/$1"
    }
    take kea-dhcp6.json
    take kea-dhcp6-access.json
    ${lib.concatMapStrings (e: "export ${lib.escapeShellArg e}\n") Environment}
    ${keaCaps} ${kea6} -c ${dir}/kea-dhcp6.json >kea-default.log 2>&1 &
    # bindDevice binds access's sockets to the VRF's device, as BindNetworkInterface= does.
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
    listed() { ${leases} "$@" | tail -n +2 | cut -f 1-5; }
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
    ${routerFirewall.load "dhcp"}
    on_pc ip -6 addr flush dev eth0 scope global
    lease pc "  ia_na 1"
    if on_pc ip -6 addr show dev eth0 | grep -q 'inet6 2001:db8:'; then
      fail "the client got an address through a firewall without DHCPv6's opening"
    fi
  ''
