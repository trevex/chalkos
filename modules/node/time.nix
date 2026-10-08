# The node's clock, kept by chrony. Certificates are checked against it, so chrony starts before
# chalkd and steps the clock at boot. The servers come with the node's identity, which chalkd's
# identity loader writes to a sources file: one image serves nodes with different servers.
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
      ntsdumpdir ${config.services.chrony.directory}
    '';
  };

  systemd.tmpfiles.rules = [
    "d ${sources} 0755 root root - -"
  ]
  ++ lib.optional dhcp "d ${dhcpSources} 0755 root root - -";

  # The servers DHCP announced, from networkd's state of each link, whenever it changes.
  systemd.paths.chalkos-chrony-dhcp = lib.mkIf dhcp {
    wantedBy = [ "multi-user.target" ];
    pathConfig.PathChanged = "/run/systemd/netif/links";
  };
  systemd.services.chalkos-chrony-dhcp = lib.mkIf dhcp {
    description = "Pass the time servers DHCP announced to chrony";
    after = [ "chronyd.service" ];
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
