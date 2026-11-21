# The chrony-dhcp unit's command with the links networkd announced time servers on: the servers
# are chrony's when the node's TimeSync takes them, and none are otherwise. chrony's effect
# reloads the sources of a chronyd that runs alone.
{
  lib,
  worker,
  unitCheck,
  ...
}:
let
  unit = worker.config.systemd.services.chalkos-chrony-dhcp;
  command = unit.serviceConfig.ExecStart;
  path = lib.makeBinPath unit.path;
  # The command chrony's effect runs, from chalkd's PATH.
  reloadSources = worker.config.chalkos.effects.chrony.run;
  chalkdPath = lib.makeBinPath worker.config.systemd.services.chalkd.path;
in
unitCheck "chrony-dhcp" [ ] ''
  # chronyc of the unit's path, which this namespace has no chronyd for.
  stub chronyc <<'STUB'
  echo "$@" >>chronyc.log
  STUB
  export PATH=$PWD/stub:${path}:$PATH
  mkdir -p /run/systemd/netif/links /run/chalkos/chrony-dhcp
  printf 'ADMIN_STATE=configured\nNTP=10.0.0.1 ntp.example\n' >/run/systemd/netif/links/2
  printf 'NTP=10.0.0.1 10.0.0.2\n' >/run/systemd/netif/links/3
  sources=/run/chalkos/chrony-dhcp/dhcp.sources
  timesync=/run/chalkos/config/time.chalkos.dev/TimeSync/timesync.json
  dhcp() {
    document time.chalkos.dev TimeSync timesync '{"servers":[],"dhcp":'"$1"'}'
  }

  dhcp true
  ${command}
  printf 'server 10.0.0.1 iburst\nserver 10.0.0.2 iburst\nserver ntp.example iburst\n' >want
  diff -u want "$sources" || fail "the servers DHCP announced"
  grep -qx "reload sources" chronyc.log || fail "chrony did not reload its sources"
  dhcp false
  ${command}
  [ ! -s "$sources" ] || fail "servers with dhcp false: $(cat "$sources")"
  dhcp true
  ${command}
  rm "$timesync"
  ${command}
  [ ! -s "$sources" ] || fail "servers without a TimeSync: $(cat "$sources")"
  # A TimeSync chalkd cannot read takes no servers, and says why.
  echo '{' >"$timesync"
  ${command} 2>errors.log
  [ ! -s "$sources" ] || fail "servers with an unreadable TimeSync: $(cat "$sources")"
  grep -q timesync errors.log || fail "no error for an unreadable TimeSync"
  [ "$(ls -A /run/chalkos/chrony-dhcp)" = dhcp.sources ] || fail "left behind: $(ls -A /run/chalkos/chrony-dhcp)"
  # chrony's effect reloads the sources of a chronyd that runs, and leaves one that does not
  # run, which reads them when it starts.
  stub systemctl <<'STUB'
  exit "$ACTIVE"
  STUB
  : >chronyc.log
  ACTIVE=3 PATH=$PWD/stub:${chalkdPath} ${reloadSources} || fail "the effect failed for a chronyd that does not run"
  [ ! -s chronyc.log ] || fail "reloaded the sources of a chronyd that does not run"
  ACTIVE=0 PATH=$PWD/stub:${chalkdPath} ${reloadSources}
  grep -qx "reload sources" chronyc.log || fail "did not reload the sources of a running chronyd"
''
