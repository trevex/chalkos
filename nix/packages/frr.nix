# FRR without Python, SNMP, Lua, watchfrr or a shell script in its closure. modules/node/frr runs
# the daemons as units of their own, which systemd restarts, so watchfrr and the scripts that start
# FRR, all of which run /bin/sh, go. The images reload FRR with chalkos-frr-reload, so frr-reload.py
# and FRR's other Python scripts go, and libfrr and vtysh no longer name the build's Python, clippy
# or the -dev outputs, which they hold in the configure arguments `show version` prints. The headers
# and pkg-config files, which name them too, go as well. Without SNMP and Lua, net-snmp and Lua,
# which run commands through a shell, are gone.
{
  frr,
  python3,
  bash,
  net-snmp,
  lua5_3,
  removeReferencesTo,
}:
(frr.override {
  snmpSupport = false;
  scriptingSupport = false;
  watchfrrSupport = false;
}).overrideAttrs
  (old: {
    nativeBuildInputs = old.nativeBuildInputs ++ [ removeReferencesTo ];
    postInstall = (old.postInstall or "") + ''
      rm $out/libexec/frr/{frr-reload,frr-reload.py,generate_support_bundle.py,frr_babeltrace.py,ospfclient.py}
      rm $out/libexec/frr/{frr,frrcommon.sh,frrinit.sh,watchfrr.sh,ssd}
      rm -r $out/include $out/lib/pkgconfig
    '';
    postFixup = (old.postFixup or "") + ''
      for file in $out/lib/libfrr.so.*.*.* $out/bin/vtysh; do
        { grep -aoE "$NIX_STORE/[a-z0-9]{32}-([^/\"' ]*-dev|python3-[0-9.]+|frr-clippy-helper-[0-9.]+)" "$file" || true; } |
          sort -u | while read -r ref; do remove-references-to -t "$ref" "$file"; done
      done
    '';
    # The build fails when Python, a shell, SNMP or Lua comes back.
    disallowedRequisites = [
      python3
      bash
      net-snmp
      net-snmp.lib
      lua5_3
    ];
  })
