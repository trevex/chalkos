# frr.service fills the rendered config's password placeholders in from its credentials into
# /run/frr/frr.conf, readable by root alone and replaced whole, and loads the fallback config
# without a rendered one. Only a peer group's password line takes a value: a placeholder
# elsewhere stays as written. A password with a line break, a trailing one too, white space, a
# NUL or more than 80 characters, and a credential the unit lacks, fail the unit and leave the
# filled config as it was, as does a fill without a credentials directory.
{
  pkgs,
  checkLib,
  unitCheck,
  ...
}:
let
  fill = checkLib.router.config.systemd.services.frr.serviceConfig.ExecStartPre;
  dir = checkLib.router.config.chalkos.outputs.frr.dir;
  golden = ../../../cmd/chalkos-render/testdata/router/edge.frr;
in
unitCheck "frr-credentials" [ pkgs.diffutils ] ''
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
''
