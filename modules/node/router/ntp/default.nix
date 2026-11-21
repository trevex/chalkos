# The router's time server: the chronyd every node runs (modules/node/time.nix) answers NTP from
# the clients of the TimeServer. The output chrony-server renders its allow lines, a rate limit
# and its local reference into a fragment that chrony's confdir reads when chronyd starts, so a
# change restarts chronyd. A node without a TimeServer has no fragment, and chrony answers no one.
#
# chrony binds one address per family, so it answers on every address of the default VRF; the
# firewall accepts NTP from allow to the addresses of listen alone, as for a Resolver.
{
  config,
  lib,
  pkgs,
  routerLib,
  ...
}:
let
  cfg = config.chalkos.router.ntp;
  kinds = routerLib.kinds ./kinds.nix;
  chrony = config.services.chrony.package;
  dir = config.chalkos.outputs.chrony-server.dir;
in
{
  options.chalkos.router = routerLib.feature "ntp" {
    carries = "an NTP server, the node's chrony,";
    kinds = lib.attrNames kinds;
  };

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;

    services.chrony.extraConfig = ''
      confdir ${dir}
      # The diagnostic ntp-clients runs chronyc in a sandbox whose /run is read-only, where it
      # cannot bind the socket chronyd's own socket answers; chronyd answers the commands below on
      # 127.0.0.1 and ::1 as well, the default ones and clients and serverstats.
      opencommands activity manual rtcdata smoothing sourcename sources sourcestats tracking clients serverstats
    '';

    chalkos.outputs.chrony-server = {
      inputs = lib.attrNames kinds;
      render = "${lib.getExe pkgs.chalkos.chalkos-render} chrony-server";
      # chronyd -p parses the directives of the files in the confdir, but takes any subnet of
      # allow, so the check also refuses a line of a form the renderer does not write.
      check = "${lib.getExe pkgs.chalkos.chalkos-render} check chrony-server ${chrony}/bin/chronyd";
    };
    # chronyd reads its config when it starts. A new port is open before chrony answers on it.
    chalkos.effects.chrony-server = {
      outputs = [ "chrony-server" ];
      restart = [ "chronyd.service" ];
      after = [ "router-nftables" ];
    };

    # The clients that chrony answered lately, and how many requests it served and dropped.
    chalkos.diagnostics.ntp-clients = {
      description = "The NTP clients the router's time server answered, as chronyc -n clients prints them, and its counts of requests, as chronyc serverstats prints them.";
      # chalkd runs chronyc -h 127.0.0.1,::1 -n clients, prints an empty line, and runs chronyc
      # serverstats, whose exit status it takes.
      command = [
        (lib.getExe pkgs.chalkos.chalkd)
        "router"
        "ntp-clients"
        "--chronyc"
        "${chrony}/bin/chronyc"
      ];
      role = "reader";
      timeout = "10s";
    };
  };
}
