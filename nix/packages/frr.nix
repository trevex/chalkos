# FRR without Python in its closure: the images reload FRR with chalkos-frr-reload, so frr-reload.py
# and FRR's other Python scripts go, and libfrr and vtysh no longer name the build's Python, clippy
# or the -dev outputs, which they hold in the configure arguments `show version` prints. The headers
# and pkg-config files, which name them too, go as well.
{
  frr,
  python3,
  removeReferencesTo,
}:
frr.overrideAttrs (old: {
  nativeBuildInputs = old.nativeBuildInputs ++ [ removeReferencesTo ];
  postInstall = (old.postInstall or "") + ''
    rm $out/libexec/frr/{frr-reload,frr-reload.py,generate_support_bundle.py,frr_babeltrace.py,ospfclient.py}
    rm -r $out/include $out/lib/pkgconfig
  '';
  postFixup = (old.postFixup or "") + ''
    for file in $out/lib/libfrr.so.*.*.* $out/bin/vtysh; do
      { grep -aoE "$NIX_STORE/[a-z0-9]{32}-([^/\"' ]*-dev|python3-[0-9.]+|frr-clippy-helper-[0-9.]+)" "$file" || true; } |
        sort -u | while read -r ref; do remove-references-to -t "$ref" "$file"; done
    done
  '';
  # The build fails when Python comes back.
  disallowedRequisites = [ python3 ];
})
