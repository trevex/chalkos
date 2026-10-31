# Kea's DHCP servers, kea-dhcp4 and kea-dhcp6, and kea-lfc, which cleans up their lease files,
# with the lease commands' hook alone: the router layer keeps its leases in memfiles. Without the
# MySQL, PostgreSQL and Kerberos backends, the other hooks, kea-shell's Python and the manuals,
# it adds 8 MiB to the router image's compressed store rather than 18.
{
  lib,
  kea,
  python3,
  bison,
  flex,
  removeReferencesTo,
}:
(kea.override {
  withKrb5 = false;
  withMysql = false;
  withPostgresql = false;
}).overrideAttrs
  (old: {
    pname = "kea-dhcp";
    nativeBuildInputs =
      lib.filter (p: !lib.hasInfix "sphinx" (lib.getName p)) old.nativeBuildInputs
      ++ [ removeReferencesTo ];
    # The manuals.
    postBuild = "";
    outputs = [
      "out"
      "python"
    ];
    postInstall = (old.postInstall or "") + ''
      for tool in kea-shell perfdhcp kea-admin keactrl kea-dhcp-ddns kea-msg-compiler; do
        rm -f $out/bin/$tool $out/sbin/$tool
      done
      find $out/lib/kea/hooks -type f ! -name libdhcp_lease_cmds.so -delete
      rm -rf $out/share/kea/scripts $out/include $out/lib/pkgconfig $out/share/doc $out/share/man
    '';
    # libkea-cfgrpt holds the build's report, which names the tools of the build.
    postFixup = (old.postFixup or "") + ''
      remove-references-to -t ${python3} -t ${bison} -t ${flex} -t $python $out/lib/libkea-cfgrpt.so.*.*.*
    '';
    meta = old.meta // {
      mainProgram = "kea-dhcp4";
    };
  })
