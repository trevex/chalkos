# ip alone, from iproute2, which the image's diagnostics run. iproute2's other tools, and arpd's
# Berkeley DB, would add about 8 MiB to every image.
{ lib, iproute2 }:
iproute2.overrideAttrs (old: {
  pname = "iproute2-ip";
  # Without db, iproute2 builds no arpd.
  buildInputs = lib.filter (p: lib.getName p != "db") old.buildInputs;
  postInstall = old.postInstall + ''
    find $out/sbin -mindepth 1 ! -name ip -delete
    rm -r $out/lib $out/share/bash-completion
  '';
  meta = old.meta // {
    mainProgram = "ip";
  };
})
