# wg alone, from wireguard-tools, which the router's WireGuard diagnostic runs. wg-quick is a bash
# script, and the wrappers of wireguard-tools put iptables and openresolv on wg's PATH, which wg
# does not use on Linux.
{ wireguard-tools }:
wireguard-tools.overrideAttrs (old: {
  pname = "wireguard-wg";
  outputs = [ "out" ];
  buildInputs = [ ];
  makeFlags = [
    "DESTDIR=$(out)"
    "PREFIX=/"
    "WITH_BASHCOMPLETION=no"
    "WITH_SYSTEMDUNITS=no"
    "WITH_WGQUICK=no"
  ];
  postInstall = ''
    rm -rf $out/share
  '';
  postFixup = "";
  meta = old.meta // {
    mainProgram = "wg";
    outputsToInstall = [ "out" ];
  };
})
