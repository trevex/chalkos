# The router's DNS resolvers: Unbound, whose whole config the output unbound renders from each
# Resolver document, unbound.conf for the default VRF's and unbound-<vrf>.conf for a VRF's. The
# default VRF's runs as unbound.service, each VRF's as unbound@<vrf>.service, whose sockets systemd
# binds to the VRF's device. A node without a Resolver runs no Unbound; a delivery that adds or
# removes a resolver starts or stops it, and one that changes it reloads Unbound and keeps its
# cache.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  unbound = config.services.unbound.package;
  dir = config.chalkos.outputs.unbound.dir;
  rendered = "${dir}/unbound.conf";
  cel = import ../../../lib/cel.nix { inherit lib; };
  inherit (import ../../../lib/kinds/api.nix) apiVersions;
  # The number of the default VRF's resolvers in the config c: a running config of an image whose
  # Resolver had no vrf lacks the field.
  defaultResolvers =
    c:
    "cel.bind(r, ${
      cel.documents c apiVersions.router "Resolver"
    }, size(r.filter(n, !has(r[n].spec.vrf) || r[n].spec.vrf == null)))";
  # Starts the resolver of each VRF with a rendered config that does not run, reloads those that
  # run and stops those without a config; with the argument stop, it stops all of them. systemd
  # binds a resolver's sockets to the index its VRF's device had when the resolver started, which
  # the resolver keeps in /run/unbound-<vrf>/ifindex. A VRF that chalkos-vrf removed and networkd
  # created anew has a new index, or no device yet, so its resolver restarts on the new device. A
  # unit that requires a device that never comes waits for it until the device's job times out, so
  # the script waits 30 seconds at most for the devices and 30 more for the resolvers it started,
  # and fails naming those that are not running then; a start still queued goes on once the device
  # appears. systemctl and systemd-escape come from the unit's PATH.
  reconcile = pkgs.writeShellApplication {
    name = "chalkos-resolvers";
    runtimeInputs = [ pkgs.coreutils ];
    text = ''
      dir=${dir}
      declare -A want=()
      failed=()
      started=()
      if [[ ''${1:-} != stop ]]; then
        shopt -s nullglob
        for file in "$dir"/unbound-*.conf; do
          vrf=''${file#"$dir"/unbound-}
          want[''${vrf%.conf}]=1
        done
        shopt -u nullglob
      fi
      deadline=$((SECONDS + 30))
      prop() { systemctl show --value --property="$2" "$1"; }
      # Starts, or with restart restarts, the resolver of the VRF $1 once its device exists.
      launch() {
        local vrf=$1 unit=$2 verb=$3
        until [[ -e /sys/class/net/$vrf ]] || ((SECONDS >= deadline)); do
          sleep 0.2
        done
        if [[ ! -e /sys/class/net/$vrf ]]; then
          failed+=("$verb $unit, whose VRF has no device")
          return
        fi
        systemctl reset-failed "$unit" 2>/dev/null || true
        if systemctl "$verb" --no-block "$unit"; then
          started+=("$unit")
        else
          failed+=("$verb $unit")
        fi
      }
      for vrf in "''${!want[@]}"; do
        unit=$(systemd-escape --template=unbound@.service "$vrf")
        if ! systemctl is-active --quiet "$unit"; then
          launch "$vrf" "$unit" start
          continue
        fi
        ifindex=$(cat "/sys/class/net/$vrf/ifindex" 2>/dev/null) || ifindex=
        if [[ -z $ifindex || $ifindex != "$(cat "/run/unbound-$vrf/ifindex" 2>/dev/null)" ]]; then
          launch "$vrf" "$unit" restart
          continue
        fi
        systemctl reload "$unit" || failed+=("reload $unit")
      done
      deadline=$((SECONDS + 30))
      for unit in "''${started[@]}"; do
        # Each unit is asked at least once, also after the wait for another used up the deadline.
        state=$(systemctl is-active "$unit") || true
        until [[ $state == active || $state == failed ]] || ((SECONDS >= deadline)); do
          sleep 0.2
          state=$(systemctl is-active "$unit") || true
        done
        [[ $state == active ]] || failed+=("start $unit, which is $state ($(prop "$unit" SubState))")
      done
      while read -r unit _; do
        [[ -n $unit ]] || continue
        vrf=$(systemd-escape --unescape --instance "$unit")
        if [[ -z ''${want[$vrf]+x} ]]; then
          if systemctl stop "$unit"; then
            systemctl reset-failed "$unit" 2>/dev/null || true
          else
            failed+=("stop $unit")
          fi
        fi
      done < <(systemctl list-units --plain --no-legend --all 'unbound@*.service')
      if ((''${#failed[@]} > 0)); then
        printf 'chalkos-resolvers: failed to %s\n' "''${failed[@]}" >&2
        exit 1
      fi
    '';
  };
  # Keeps the index of the device of the VRF $1 in the resolver's runtime directory, and fails
  # while the VRF has no device. systemd looked the index up when it set the unit's cgroup up, a
  # moment before: a VRF removed and created anew within that moment leaves a record of the new
  # index for sockets bound to the old one. Only chalkos-vrf removes a VRF, and chalkos-resolvers,
  # which starts the resolvers, runs after it.
  recordDevice = pkgs.writeShellScript "unbound-vrf-device" ''
    set -e
    read -r ifindex <"/sys/class/net/$1/ifindex"
    echo "$ifindex" >"$RUNTIME_DIRECTORY/ifindex"
  '';
  # RFC 8880's answer for ipv4only.arpa, which a DNS64 resolver gives itself; the dns64 module
  # synthesizes its AAAA records from these A records.
  ipv4only = pkgs.writeText "ipv4only.arpa.zone" ''
    ipv4only.arpa. 3600 IN SOA localhost. nobody.invalid. 1 3600 1200 604800 3600
    ipv4only.arpa. 3600 IN NS localhost.
    ipv4only.arpa. 3600 IN A 192.0.0.170
    ipv4only.arpa. 3600 IN A 192.0.0.171
  '';
in
{
  config = lib.mkIf config.chalkos.router.enable {
    services.unbound = {
      enable = true;
      # The rendered config holds the trust anchor of the image's root keys, which updates with
      # the image.
      enableRootTrustAnchor = false;
      # The node resolves its own names as before; the resolver serves the Resolver's clients.
      resolveLocalQueries = false;
      # The module's own config goes unused.
      checkconf = false;
    };
    environment.etc."unbound/unbound.conf".source = lib.mkForce rendered;

    chalkos.outputs.unbound = {
      # The VRFs and the Networks tell which VRF holds each listen address.
      inputs = [
        "Resolver"
        "VRF"
        "Network"
      ];
      render = "${lib.getExe chalkosPackages.chalkos-render} unbound ${pkgs.dns-root-data}/root.key ${ipv4only}";
      check = ''for f in "$OUT"/*.conf; do [ ! -e "$f" ] || ${unbound}/bin/unbound-checkconf "$f" || exit; done'';
      examples = ../examples/router;
    };
    chalkos.effects.unbound = {
      outputs = [ "unbound" ];
      reload = [ "unbound.service" ];
    };
    # The default VRF's resolver added starts Unbound, and one removed stops it: the unit's
    # condition no longer holds once it restarts.
    chalkos.effects.unbound-start = {
      when = "${defaultResolvers "config"} != ${defaultResolvers "oldConfig"}";
      restart = [ "unbound.service" ];
      start = true;
    };
    # A VRF's resolver added, changed or removed, and a VRF networkd creates anew, which stopped the
    # resolver bound to it.
    chalkos.effects.resolvers = {
      outputs = [
        "unbound"
        "networkd"
      ];
      reload = [ "chalkos-resolvers.service" ];
    };

    systemd.services.unbound = {
      # A new resolver's port is open before it answers.
      after = [
        "chalkos-config.service"
        "chalkos-firewall.service"
        "chalkos-nat64.service"
      ];
      # Without a rendered config, as in maintenance mode, the node runs no resolver.
      unitConfig.ConditionPathExists = rendered;
      serviceConfig = {
        # unbound-control reaches Unbound through the socket the rendered config names in
        # /run/unbound, which Unbound makes its user's and group's alone; the directory admits
        # them and root, so chalkd.
        ExecReload = lib.mkForce "${unbound}/bin/unbound-control reload_keep_cache";
        RuntimeDirectoryMode = "0750";
        # Unbound binds port 53 and needs no raw sockets, which only ip-transparent takes.
        AmbientCapabilities = lib.mkForce [ "CAP_NET_BIND_SERVICE" ];
        CapabilityBoundingSet = lib.mkForce [ "CAP_NET_BIND_SERVICE" ];
      };
    };
    # A VRF's resolver: Unbound with the default VRF's hardening, whose sockets systemd binds to the
    # VRF's device, as ip vrf exec would. systemd binds them to the index the device has when the
    # unit starts, which the unit keeps for chalkos-resolvers. A device that goes and comes back
    # stops nothing: chalkos-resolvers restarts the resolver once the device is back. systemd may
    # see the old device go only after that, and BindsTo= would then stop the restarted resolver.
    systemd.services."unbound@" = {
      description = "Unbound of the VRF %I";
      after = [
        "sys-subsystem-net-devices-%i.device"
        "chalkos-config.service"
        "chalkos-firewall.service"
        "chalkos-nat64.service"
      ];
      unitConfig = {
        Requires = "sys-subsystem-net-devices-%i.device";
        ConditionPathExists = "${dir}/unbound-%I.conf";
      };
      # The default VRF's unit prepares nothing a VRF's needs.
      serviceConfig = removeAttrs config.systemd.services.unbound.serviceConfig [ "ExecStartPre" ] // {
        ExecStart = "${unbound}/bin/unbound -p -d -c ${dir}/unbound-%I.conf";
        ExecReload = "${unbound}/bin/unbound-control -c ${dir}/unbound-%I.conf reload_keep_cache";
        # systemd starts a unit whose interface it cannot find without binding its sockets: this
        # unit fails instead.
        ExecStartPre = "${recordDevice} %I";
        BindNetworkInterface = "%I";
        # unbound-control reaches it through the socket the rendered config names in
        # /run/unbound-<vrf>, which root, and so chalkd, may enter, as may the group unbound, and
        # so chalkos-resolvers, which reads the device's index there.
        RuntimeDirectory = "unbound-%I";
        RuntimeDirectoryMode = "0750";
      };
    };
    # Runs the VRFs' resolvers of the rendered configs, after networkd, which creates the VRFs, and
    # the NAT64s, as unbound.service does.
    systemd.services.chalkos-resolvers = {
      description = "Run the resolvers of the router's VRFs";
      after = [
        "chalkos-config.service"
        "chalkos-vrf.service"
        "systemd-networkd.service"
        "chalkos-firewall.service"
        "chalkos-nat64.service"
      ];
      wantedBy = [ "multi-user.target" ];
      path = [ config.systemd.package ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = lib.getExe reconcile;
        ExecReload = lib.getExe reconcile;
        ExecStop = "${lib.getExe reconcile} stop";
        # systemctl reaches systemd over its socket, for which root needs no capability.
        CapabilityBoundingSet = [ "" ];
        SupplementaryGroups = [ "unbound" ];
        RestrictAddressFamilies = [ "AF_UNIX" ];
        NoNewPrivileges = true;
        PrivateDevices = true;
        ProtectControlGroups = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectKernelLogs = true;
        ProtectClock = true;
        ProtectHostname = true;
        RestrictNamespaces = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        SystemCallArchitectures = "native";
        SystemCallFilter = [ "@system-service" ];
      };
    };
    # chalkd asks unbound-control whether the default VRF's resolver runs, and systemctl whether
    # the VRFs' units are active.
    systemd.services.chalkd.path = [ unbound ];
  };
}
