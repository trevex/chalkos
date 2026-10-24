# The node's clock, kept by chrony. Certificates are checked against it, so chrony steps a clock
# that is off at boot instead of slewing it for hours; until then chalkd and the control plane
# may refuse certificates that are valid. The servers come with the node's config, which the
# built-in output chrony renders into a sources file, and so does whether the servers DHCP
# announces count: one image serves nodes with different servers.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  # The sources file of the TimeSync's servers, which chrony reads from the output's dir.
  sources = config.chalkos.outputs.chrony.dir;
  dhcpSources = "/run/chalkos/chrony-dhcp";
  inherit (config.services.chrony) package;
  dhcpEnabled = config.chalkos.kinds.TimeSync.get { path = "spec.dhcp"; };
  cel = import ../../lib/cel.nix { inherit lib; };
  # A server as a line of chrony's sources file.
  server =
    s:
    "server ${s.host} iburst${lib.optionalString (s.nts or true) " nts"}${
      lib.optionalString ((s.port or null) != null && s.port != 0) " port ${toString s.port}"
    }\n";
  # The TimeSync's servers, each as server renders it, or the kind's default servers without a
  # TimeSync, whose input holds no document. A port of 0 is NTP's, as the kind says of null.
  template = pkgs.writeText "servers.sources.tmpl" ''
    {{- with .documents.TimeSync }}{{ range (index . 0).spec.servers -}}
    server {{ .host }} iburst{{ if .nts }} nts{{ end }}{{ with .port }}{{ if ne (print .) "0" }} port {{ . }}{{ end }}{{ end }}
    {{ end }}{{ else }}${lib.concatMapStrings server config.chalkos.kinds.TimeSync.spec.servers.default}{{ end -}}
  '';
  # What chrony's effect runs: chronyd reads its sources when it starts, so one that does not run
  # needs no reload, and a reload it cannot take must not fail the delivery. systemctl and chronyc
  # come from chalkd's PATH.
  reloadSources = pkgs.writeShellScriptBin "chalkos-chrony-reload-sources" ''
    systemctl is-active --quiet chronyd.service || exit 0
    exec chronyc reload sources
  '';
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

  systemd.tmpfiles.rules = [ "d ${dhcpSources} 0755 root root - -" ];

  # The servers DHCP announced, from networkd's state of each link, when the node's TimeSync takes
  # them: once networkd runs at boot, whenever that state changes, and when the TimeSync changes.
  # The service stays active after it ran, so chalkd's try-restart runs it again when the
  # TimeSync changes. A path unit does not start an active service again, so a change of the
  # links' state restarts it.
  chalkos.effects.chrony-dhcp = {
    when = cel.specsChanged "time.chalkos.dev/v1alpha1" "TimeSync";
    restart = [ "chalkos-chrony-dhcp.service" ];
  };
  # A CNI brings up several links at once at boot, each a change of /run/systemd/netif/links, and
  # every change restarts chalkos-chrony-dhcp. systemd's default limits fail the path and both
  # services on such a burst, and a failed path passes on no later change; the units are cheap
  # and run the same each time, so none of them is limited.
  systemd.paths.chalkos-chrony-dhcp = {
    wantedBy = [ "multi-user.target" ];
    after = [ "chalkos-config.service" ];
    pathConfig = {
      PathChanged = "/run/systemd/netif/links";
      Unit = "chalkos-chrony-dhcp-links.service";
      TriggerLimitIntervalSec = 0;
    };
  };
  systemd.services.chalkos-chrony-dhcp-links = {
    description = "Pass the time servers DHCP announced to chrony after a link changed";
    startLimitIntervalSec = 0;
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${config.systemd.package}/bin/systemctl restart chalkos-chrony-dhcp.service";
    };
  };
  systemd.services.chalkos-chrony-dhcp = {
    description = "Pass the time servers DHCP announced to chrony";
    startLimitIntervalSec = 0;
    chalkos.kinds = [ "TimeSync" ];
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

  # The sources file holds the TimeSync's servers, or chalkos's own without a TimeSync. chronyd
  # reads sourcedir files only when it starts or reloads its sources, and its -p parses files of
  # include alone, so the check includes the file. A sourcedir file takes server, pool and peer
  # directives alone, and the template checks nothing of its own, so the check refuses any other
  # line too. The examples run the check when the image is built.
  chalkos.outputs.chrony = {
    inputs = [ "TimeSync" ];
    render = lib.chalkos.render.template template;
    check = ''
      conf=$(mktemp)
      echo "include $OUT/servers.sources" >"$conf"
      ${package}/bin/chronyd -p -f "$conf" >/dev/null
      if grep -qvE '^server [A-Za-z0-9][A-Za-z0-9.:-]* iburst( nts)?( port [0-9]+)?$' "$OUT/servers.sources"; then
        echo "servers.sources holds a line that is no server of a host" >&2
        exit 1
      fi
    '';
    examples = ./examples/chrony;
  };
  # chrony reads the sources again when they change; whether the servers DHCP announces count is
  # chalkos-chrony-dhcp's. The effect tells the chronyd every node runs, while it runs.
  chalkos.effects.chrony = {
    outputs = [ "chrony" ];
    run = "chalkos-chrony-reload-sources";
  };

  # chalkd reads chrony's tracking state for the node's status, and runs chrony's effect.
  systemd.services.chalkd.path = [
    package
    reloadSources
  ];
}
