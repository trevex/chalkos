# Checks that run the units' scripts of the worker and of a router image as on a node, in
# namespaces of their own, one file each: the check of <name>.nix is the check <name>.
{ pkgs, checkLib, ... }:
let
  inherit (pkgs) lib;
  inherit (checkLib) worker firewall unitCheck;
  # setpriv's arguments that leave root with the capabilities caps alone, as a unit's
  # CapabilityBoundingSet= does; an empty string, as an empty set holds, adds none. The checks'
  # namespaces map root alone, so a unit's own user, such as kea or dnsmasq, is no user they can
  # run as.
  boundTo =
    caps:
    "${pkgs.util-linux}/bin/setpriv --bounding-set=-all${
      lib.concatMapStrings (c: ",+" + lib.toLower (lib.removePrefix "CAP_" c)) (lib.remove "" caps)
    } --no-new-privs --";
  # Binds each IPv4 and IPv6 socket to the device $BIND_DEVICE names when it is created.
  bindDevice = pkgs.runCommandCC "bind-device.so" { } ''
    cat >bind.c <<'C'
    #define _GNU_SOURCE
    #include <dlfcn.h>
    #include <stdlib.h>
    #include <string.h>
    #include <sys/socket.h>
    int socket(int domain, int type, int protocol) {
      static int (*real)(int, int, int);
      if (!real) real = dlsym(RTLD_NEXT, "socket");
      int fd = real(domain, type, protocol);
      const char *dev = getenv("BIND_DEVICE");
      if (fd >= 0 && dev && (domain == AF_INET || domain == AF_INET6))
        setsockopt(fd, SOL_SOCKET, SO_BINDTODEVICE, dev, strlen(dev) + 1);
      return fd;
    }
    C
    $CC -shared -fPIC -o $out bind.c -ldl
  '';
  # The router's chalkos-firewall.service: its scripts, the directory of its ruleset, and load
  # <example>, the lines of a check that load the ruleset the router image renders from the
  # example as the unit's start does.
  routerFirewall =
    let
      unit = checkLib.router.config.systemd.services.chalkos-firewall;
      # The start runs chalkd, which runs nft from the unit's path.
      ExecStart = "PATH=${lib.makeBinPath unit.path}:$PATH ${unit.serviceConfig.ExecStart}";
      inherit (unit.serviceConfig) ExecReload;
      dir = checkLib.router.config.chalkos.outputs.router-nftables.dir;
    in
    {
      inherit ExecStart ExecReload dir;
      load = example: ''
        mkdir -p ${dir}
        install -m 644 ${checkLib.routerRuleset example} ${dir}/ruleset.nft
        ${ExecStart} || fail "chalkos-firewall did not load the ${example} example's ruleset"
      '';
    };
  arguments = {
    inherit
      pkgs
      lib
      checkLib
      worker
      firewall
      unitCheck
      boundTo
      bindDevice
      routerFirewall
      ;
  };
in
lib.mapAttrs' (
  file: _: lib.nameValuePair (lib.removeSuffix ".nix" file) (import (./. + "/${file}") arguments)
) (removeAttrs (builtins.readDir ./.) [ "default.nix" ])
