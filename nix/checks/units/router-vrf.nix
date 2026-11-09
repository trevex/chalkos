# chalkos-vrf in a network namespace: it removes the VRFs that no .netdev of Kind=vrf declares
# and those whose table changed, flushes networkd's unreachable defaults of their tables alone,
# keeps a declared VRF with its member, the unreachable defaults of its table and every link of
# another kind, and with no rendered units, as in maintenance mode, removes every VRF the
# rendered units declared. It reads /etc's units before the rendered ones, by name, as networkd
# does. When it removed a VRF it reloads a running networkd and, without waiting, a running
# chalkos-resolvers, through the systemctl and networkctl of the unit's PATH that the check
# stubs, and otherwise does not.
{
  checkLib,
  unitCheck,
  ...
}:
let
  script = checkLib.router.config.systemd.services.chalkos-vrf.serviceConfig.ExecReload;
  dir = checkLib.router.config.chalkos.outputs.networkd.dir;
in
unitCheck "router-vrf" checkLib.networkTools ''
  debug() {
    ip -d link show >&2
  }
  exists() { ip link show "$1" >/dev/null 2>&1; }
  reloaded() {
    [ "$(cat networkctl.log)" = reload ] || fail "$1: networkctl was called with: $(cat networkctl.log)"
    grep -qx 'reload --no-block chalkos-resolvers.service' systemctl.log ||
      fail "$1: chalkos-resolvers was not reloaded: $(cat systemctl.log)"
    : >networkctl.log
    : >systemctl.log
  }
  not_reloaded() {
    [ ! -s networkctl.log ] || fail "$1: networkctl was called with: $(cat networkctl.log)"
    if grep -q '^reload' systemctl.log; then fail "$1: systemctl was called with: $(cat systemctl.log)"; fi
    : >systemctl.log
  }
  mkdir -p stub
  cat >stub/networkctl <<'STUB'
  #!/bin/sh
  echo "$@" >>networkctl.log
  STUB
  cat >stub/systemctl <<'STUB'
  #!/bin/sh
  echo "$@" >>systemctl.log
  [ "$1" != is-active ] || exit "''${ACTIVE:-0}"
  STUB
  chmod +x stub/networkctl stub/systemctl
  : >networkctl.log
  : >systemctl.log
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
''
