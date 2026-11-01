# The router's DHCP relays: dnsmasq in relay-only mode, whose config the output dhcp-relay renders
# from the DHCPRelay documents, dnsmasq-relay.conf for the default VRF's and
# dnsmasq-relay-<vrf>.conf for a VRF's. The default VRF's runs as dnsmasq-relay.service, each
# VRF's as dnsmasq-relay@<vrf>.service, whose sockets systemd binds to the VRF's device.
# chalkos-dhcp-relays starts the relays of the rendered configs, restarts those that run, as
# dnsmasq reads its relays at start alone, and stops those without a config. A relay keeps no
# state, and finds its links' addresses as requests come, so it starts before its links exist.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (pkgs) dnsmasq;
  dir = config.chalkos.outputs.dhcp-relay.dir;
  # Stops the relays without a config, then starts the relay of each rendered config that does
  # not run, once its VRF's device exists, and restarts those that run; with the argument stop, it
  # stops all of them. A VRF's relay keeps the index of the device its sockets are bound to in
  # /run/dnsmasq-relay/<vrf>.ifindex. The script waits 30 seconds at most for the devices and 30
  # more for the relays it started, and fails naming those that are not running then. systemctl
  # and systemd-escape come from the unit's PATH.
  reconcile = pkgs.writeShellApplication {
    name = "chalkos-dhcp-relays";
    runtimeInputs = [ pkgs.coreutils ];
    text = ''
      dir=${dir}
      declare -A want=()
      failed=()
      started=()
      if [[ ''${1:-} != stop ]]; then
        shopt -s nullglob
        for file in "$dir"/dnsmasq-relay.conf "$dir"/dnsmasq-relay-*.conf; do
          [[ -e $file ]] || continue
          base=''${file#"$dir"/dnsmasq-relay}
          base=''${base%.conf}
          if [[ -z $base ]]; then
            want[dnsmasq-relay.service]=
          else
            want[$(systemd-escape --template=dnsmasq-relay@.service "''${base#-}")]=''${base#-}
          fi
        done
        shopt -u nullglob
      fi
      while read -r unit _; do
        [[ -n $unit ]] || continue
        if [[ -z ''${want[$unit]+x} ]]; then
          if systemctl stop "$unit"; then
            systemctl reset-failed "$unit" 2>/dev/null || true
          else
            failed+=("stop $unit")
          fi
        fi
      done < <(systemctl list-units --plain --no-legend --all dnsmasq-relay.service 'dnsmasq-relay@*.service')
      deadline=$((SECONDS + 30))
      for unit in "''${!want[@]}"; do
        vrf=''${want[$unit]}
        if [[ -n $vrf ]]; then
          until [[ -e /sys/class/net/$vrf ]] || ((SECONDS >= deadline)); do
            sleep 0.2
          done
          if [[ ! -e /sys/class/net/$vrf ]]; then
            failed+=("start $unit, whose VRF has no device")
            continue
          fi
        fi
        verb=start
        systemctl is-active --quiet "$unit" && verb=restart
        systemctl reset-failed "$unit" 2>/dev/null || true
        if systemctl "$verb" --no-block "$unit"; then
          started+=("$unit")
        else
          failed+=("$verb $unit")
        fi
      done
      deadline=$((SECONDS + 30))
      for unit in "''${started[@]}"; do
        state=$(systemctl is-active "$unit") || true
        until [[ $state == active || $state == failed ]] || ((SECONDS >= deadline)); do
          sleep 0.2
          state=$(systemctl is-active "$unit") || true
        done
        [[ $state == active ]] || failed+=("start $unit, which is $state ($(systemctl show --value --property=SubState "$unit"))")
      done
      if ((''${#failed[@]} > 0)); then
        printf 'chalkos-dhcp-relays: failed to %s\n' "''${failed[@]}" >&2
        exit 1
      fi
    '';
  };
  # Keeps the index of the device of the VRF $1 in /run/dnsmasq-relay/$1.ifindex, and fails while
  # the VRF has no device, as unbound@ does.
  recordDevice = pkgs.writeShellScript "dnsmasq-relay-vrf-device" ''
    set -e
    read -r ifindex <"/sys/class/net/$1/ifindex"
    echo "$ifindex" >"$RUNTIME_DIRECTORY/$1.ifindex"
  '';
  # A relay's hardening: the user dnsmasq, which dnsmasq stays as, with the capabilities it
  # demands of a relay: port 67 and 547, the ICMPv6 socket of DHCPv6 and the ARP entries of
  # DHCPv4 clients without an address. It writes no lease file and no pid file.
  relay = {
    Restart = "on-failure";
    RestartSec = 5;
    User = "dnsmasq";
    Group = "dnsmasq";
    AmbientCapabilities = [
      "CAP_NET_BIND_SERVICE"
      "CAP_NET_RAW"
      "CAP_NET_ADMIN"
    ];
    CapabilityBoundingSet = [
      "CAP_NET_BIND_SERVICE"
      "CAP_NET_RAW"
      "CAP_NET_ADMIN"
    ];
    RuntimeDirectory = "dnsmasq-relay";
    RuntimeDirectoryPreserve = true;
    NoNewPrivileges = true;
    ProtectSystem = "strict";
    ProtectHome = true;
    PrivateTmp = true;
    PrivateDevices = true;
    ProtectKernelTunables = true;
    ProtectKernelModules = true;
    ProtectKernelLogs = true;
    ProtectControlGroups = true;
    ProtectClock = true;
    ProtectHostname = true;
    RestrictAddressFamilies = [
      "AF_UNIX"
      "AF_NETLINK"
      "AF_INET"
      "AF_INET6"
    ];
    RestrictNamespaces = true;
    RestrictRealtime = true;
    RestrictSUIDSGID = true;
    LockPersonality = true;
    MemoryDenyWriteExecute = true;
    SystemCallArchitectures = "native";
    SystemCallFilter = [ "@system-service" ];
  };
  start =
    conf:
    "${dnsmasq}/bin/dnsmasq --keep-in-foreground --log-facility=- --pid-file= --conf-file=${conf}";
in
{
  config = lib.mkIf (config.chalkos.router.enable && config.chalkos.router.dhcpRelay.enable) {
    # Pinned, as kea's: 332 is free in NixOS's ids.nix.
    users.users.dnsmasq = {
      isSystemUser = true;
      uid = 332;
      group = "dnsmasq";
      description = "dnsmasq DHCP relays";
    };
    users.groups.dnsmasq.gid = 332;

    chalkos.outputs.dhcp-relay = {
      # The VRFs and the Networks tell each link's VRF and its addresses, the DHCPSubnets, on
      # images with DHCP, the VRFs whose families Kea serves, and the NAT64s, on images with
      # NAT64, the links no relay may name.
      inputs = [
        "DHCPRelay"
        "VRF"
        "Network"
      ]
      ++ lib.optional config.chalkos.router.dhcp.enable "DHCPSubnet"
      ++ lib.optional config.chalkos.router.nat64.enable "NAT64";
      render = "${lib.getExe chalkosPackages.chalkos-render} dhcp-relay";
      check = ''
        for f in "$OUT"/*.conf; do
          [ -e "$f" ] || continue
          ${dnsmasq}/bin/dnsmasq --test --conf-file="$f" >/dev/null
        done
      '';
      examples = config.chalkos.router.examples;
    };
    # A relay added, changed or removed, and a VRF networkd creates anew.
    chalkos.effects.dhcp-relay = {
      outputs = [
        "dhcp-relay"
        "networkd"
      ];
      reload = [ "chalkos-dhcp-relays.service" ];
    };

    systemd.services.dnsmasq-relay = {
      description = "dnsmasq's DHCP relay of the default VRF";
      after = [
        "chalkos-config.service"
        "chalkos-firewall.service"
        "systemd-networkd.service"
      ];
      unitConfig.ConditionPathExists = "${dir}/dnsmasq-relay.conf";
      serviceConfig = relay // {
        ExecStart = start "${dir}/dnsmasq-relay.conf";
      };
    };
    systemd.services."dnsmasq-relay@" = {
      description = "dnsmasq's DHCP relay of the VRF %I";
      after = [
        "sys-subsystem-net-devices-%i.device"
        "chalkos-config.service"
        "chalkos-firewall.service"
        "systemd-networkd.service"
      ];
      unitConfig = {
        Requires = "sys-subsystem-net-devices-%i.device";
        ConditionPathExists = "${dir}/dnsmasq-relay-%I.conf";
      };
      serviceConfig = relay // {
        ExecStartPre = "${recordDevice} %I";
        ExecStart = start "${dir}/dnsmasq-relay-%I.conf";
        BindNetworkInterface = "%I";
      };
    };
    systemd.services.chalkos-dhcp-relays = {
      description = "Run the router's DHCP relays";
      after = [
        "chalkos-config.service"
        "chalkos-vrf.service"
        "systemd-networkd.service"
        "chalkos-firewall.service"
      ];
      wantedBy = [ "multi-user.target" ];
      path = [ config.systemd.package ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = lib.getExe reconcile;
        ExecReload = lib.getExe reconcile;
        ExecStop = "${lib.getExe reconcile} stop";
        CapabilityBoundingSet = [ "" ];
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
  };
}
