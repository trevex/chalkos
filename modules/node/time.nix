# The node's clock, kept by chrony. Certificates are checked against it, so chrony steps a clock
# that is off at boot instead of slewing it for hours; until then chalkd and the control plane
# may refuse certificates that are valid. The servers come with the node's identity, which
# chalkd's identity loader writes to a sources file: one image serves nodes with different
# servers.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  # The identity loader writes identity.sources here; chalkd reloads it when it changes.
  sources = "/run/chalkos/chrony";
  dhcpSources = "/run/chalkos/chrony-dhcp";
  inherit (config.services.chrony) package;
  dhcp = config.chalkos.time.dhcpServers or false;
in
{
  services.chrony = {
    enable = true;
    # Every server is in a sources file; NTS is chosen per server there.
    servers = [ ];
    enableNTS = false;
    # A clock more than a second off is stepped, during the first three updates only.
    makestep = {
      threshold = 1;
      limit = 3;
    };
    extraConfig = ''
      sourcedir ${sources}
      ${lib.optionalString dhcp "sourcedir ${dhcpSources}"}
      # A clock far off at boot would fail the NTS servers' certificates; their time check is
      # skipped until the clock was first set.
      nocerttimecheck 1
      # On VAR, so the NTS cookies survive a reboot and the node needs no new key exchange with
      # each server, which a clock that is far off at boot could fail.
      ntsdumpdir ${config.services.chrony.directory}
    '';
  };

  systemd.tmpfiles.rules = [
    "d ${sources} 0755 root root - -"
  ]
  ++ lib.optional dhcp "d ${dhcpSources} 0755 root root - -";

  # The servers DHCP announced, from networkd's state of each link: once networkd runs at boot,
  # and whenever that state changes.
  systemd.paths.chalkos-chrony-dhcp = lib.mkIf dhcp {
    wantedBy = [ "multi-user.target" ];
    pathConfig.PathChanged = "/run/systemd/netif/links";
  };
  systemd.services.chalkos-chrony-dhcp = lib.mkIf dhcp {
    description = "Pass the time servers DHCP announced to chrony";
    wantedBy = [ "multi-user.target" ];
    after = [
      "chronyd.service"
      "systemd-networkd.service"
    ];
    path = [
      pkgs.coreutils
      pkgs.gnused
      package
    ];
    serviceConfig.Type = "oneshot";
    script = ''
      tmp=$(mktemp ${dhcpSources}/.dhcp.XXXXXX)
      for link in /run/systemd/netif/links/*; do
        [ -f "$link" ] || continue
        for server in $(sed -n 's/^NTP=//p' "$link"); do
          echo "server $server iburst"
        done
      done | sort -u > "$tmp"
      mv "$tmp" ${dhcpSources}/dhcp.sources
      chronyc reload sources || true
    '';
  };

  # chalkd reads chrony's tracking state for the node's status.
  systemd.services.chalkd.path = [ package ];
}
