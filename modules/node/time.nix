# The node's clock, kept by chrony. Certificates are checked against it, so chrony steps a clock
# that is off at boot instead of slewing it for hours; until then chalkd and the control plane
# may refuse certificates that are valid. The servers come with the node's config, which chalkd's
# config loader writes to a sources file, and so does whether the servers DHCP announces count:
# one image serves nodes with different servers.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  # The config loader writes identity.sources here; chalkd reloads it when it changes.
  sources = "/run/chalkos/chrony";
  dhcpSources = "/run/chalkos/chrony-dhcp";
  inherit (config.services.chrony) package;
  dhcpEnabled = config.chalkos.kinds.TimeSync.get { path = "spec.dhcp"; };
  cel = import ./runtime/cel.nix { inherit lib; };
  servers = c: "${cel.specs c "time.chalkos.dev/v1alpha1" "TimeSync"}.map(s, s.servers)";
  hashFiles = pkgs.callPackage ../../nix/chalkos-hash-files.nix { };
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
      sourcedir ${dhcpSources}
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
    "d ${dhcpSources} 0755 root root - -"
  ];

  # The servers DHCP announced, from networkd's state of each link, when the node's TimeSync takes
  # them: once networkd runs at boot, whenever that state changes, and when the TimeSync changes.
  # The service stays active after it ran, so chalkd's try-restart runs it again when the
  # TimeSync changes. A path unit does not start an active service again, so a change of the
  # links' state restarts it.
  chalkos.consumers.chalkos-chrony-dhcp.kinds = [ "TimeSync" ];
  systemd.paths.chalkos-chrony-dhcp = {
    wantedBy = [ "multi-user.target" ];
    after = [ "chalkos-config.service" ];
    pathConfig = {
      PathChanged = "/run/systemd/netif/links";
      Unit = "chalkos-chrony-dhcp-links.service";
    };
  };
  systemd.services.chalkos-chrony-dhcp-links = {
    description = "Pass the time servers DHCP announced to chrony after a link changed";
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${config.systemd.package}/bin/systemctl restart chalkos-chrony-dhcp.service";
    };
  };
  systemd.services.chalkos-chrony-dhcp = {
    description = "Pass the time servers DHCP announced to chrony";
    wantedBy = [ "multi-user.target" ];
    after = [
      "chalkos-config.service"
      "chronyd.service"
      "systemd-networkd.service"
    ];
    path = [
      pkgs.coreutils
      pkgs.gnused
      package
    ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = ''
      # No TimeSync takes no servers from DHCP, and chalkcfg prints nothing for it; one that
      # chalkcfg cannot read takes none either, and its error is logged.
      dhcp=$(${dhcpEnabled}) || dhcp=false
      tmp=$(mktemp ${dhcpSources}/.dhcp.XXXXXX)
      if [ "$dhcp" = true ]; then
        for link in /run/systemd/netif/links/*; do
          [ -f "$link" ] || continue
          for server in $(sed -n 's/^NTP=//p' "$link"); do
            echo "server $server iburst"
          done
        done | sort -u > "$tmp"
      fi
      mv "$tmp" ${dhcpSources}/dhcp.sources
      chronyc reload sources || true
    '';
  };

  # The sources file holds the TimeSync's servers, or chalkos's own without a TimeSync, so chrony
  # reads it again when they change, or when the file differs from the one before the delivery,
  # as one a slot written before the documents were kept gave from its identity alone; whether
  # the servers DHCP announces count is chalkos-chrony-dhcp's. A chronyd that does not run reads
  # it when it starts.
  chalkos.consumers.chronyd.reload = {
    when = "${servers "config"} != ${servers "oldConfig"} || facts.timeSources != oldFacts.timeSources";
    command = "chronyc reload sources";
  };
  chalkos.facts.timeSources.command = "${hashFiles} ${sources}/identity.sources";

  # chalkd reads chrony's tracking state for the node's status.
  systemd.services.chalkd.path = [ package ];
}
