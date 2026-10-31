# The router's DHCP servers: Kea's DHCPv4 server, whose config the output kea renders from the
# DHCPSubnet documents, kea-dhcp4.json for the default VRF's and kea-dhcp4-<vrf>.json for a VRF's.
# The default VRF's runs as kea-dhcp4.service, each VRF's as kea-dhcp4@<vrf>.service, whose
# sockets systemd binds to the VRF's device. Kea refuses a config that names a link that does not
# exist, and stops when a reload does, so chalkos-dhcp starts the servers once their links exist,
# reloads them when their config or the links change, and stops those without a config. A reload
# keeps the leases.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (chalkosPackages) kea;
  dir = config.chalkos.outputs.kea.dir;
  # Stops the servers without a config, then starts the server of each rendered config that does
  # not run, once its links and its VRF's device exist, and reloads those that run; with the
  # argument stop, it stops all of them. systemd binds a VRF's server's sockets to the index its
  # VRF's device had when the server started, which the server keeps in
  # /run/kea/dhcp4-<vrf>.ifindex: a VRF that chalkos-vrf removed and networkd created anew has a new
  # index, so its server restarts on the new device. The script waits 30 seconds at most for the
  # links and 30 more for the servers it started, and fails naming those that are not running then.
  # systemctl and systemd-escape come from the unit's PATH.
  reconcile = pkgs.writeShellApplication {
    name = "chalkos-dhcp";
    runtimeInputs = [
      pkgs.coreutils
      pkgs.jq
    ];
    text = ''
      dir=${dir}
      declare -A want=()
      failed=()
      started=()
      if [[ ''${1:-} != stop ]]; then
        shopt -s nullglob
        for file in "$dir"/kea-dhcp4.json "$dir"/kea-dhcp4-*.json; do
          # kea-dhcp4.json is no pattern, so nullglob keeps it whether it exists or not.
          [[ -e $file ]] || continue
          base=''${file#"$dir"/kea-dhcp4}
          base=''${base%.json}
          if [[ -z $base ]]; then
            want[kea-dhcp4.service]=$file
          else
            want[$(systemd-escape --template=kea-dhcp4@.service "''${base#-}")]=$file
          fi
        done
        shopt -u nullglob
      fi
      deadline=$((SECONDS + 30))
      # The VRF of the unit $1, empty for the default VRF's.
      vrfOf() {
        [[ $1 == kea-dhcp4@* ]] || return 0
        systemd-escape --unescape --instance "$1"
      }
      # Waits for the links of the config $2 of the unit $1 and its VRF's device, and fails naming
      # those that do not exist, or the config when it cannot be read.
      links() {
        local unit=$1 vrf names missing=() devices=()
        vrf=$(vrfOf "$unit")
        if ! names=$(jq -r '.Dhcp4["interfaces-config"].interfaces[]' "$2"); then
          failed+=("run $unit, whose config $2 cannot be read")
          return 1
        fi
        [[ -z $names ]] || mapfile -t devices <<<"$names"
        [[ -z $vrf ]] || devices+=("$vrf")
        for d in "''${devices[@]}"; do
          until [[ -e /sys/class/net/$d ]] || ((SECONDS >= deadline)); do
            sleep 0.2
          done
          [[ -e /sys/class/net/$d ]] || missing+=("$d")
        done
        if ((''${#missing[@]} > 0)); then
          failed+=("run $unit, whose links ''${missing[*]} have no device")
          return 1
        fi
      }
      # Stops the servers without a config first, so that a subnet that moved to another VRF
      # leaves its links before the other VRF's server starts on them.
      while read -r unit _; do
        [[ -n $unit ]] || continue
        if [[ -z ''${want[$unit]+x} ]]; then
          if systemctl stop "$unit"; then
            systemctl reset-failed "$unit" 2>/dev/null || true
          else
            failed+=("stop $unit")
          fi
        fi
      done < <(systemctl list-units --plain --no-legend --all kea-dhcp4.service 'kea-dhcp4@*.service')
      for unit in "''${!want[@]}"; do
        links "$unit" "''${want[$unit]}" || continue
        verb=reload
        if ! systemctl is-active --quiet "$unit"; then
          verb=start
        elif vrf=$(vrfOf "$unit") && [[ -n $vrf ]]; then
          ifindex=$(cat "/sys/class/net/$vrf/ifindex" 2>/dev/null) || ifindex=
          if [[ -z $ifindex || $ifindex != "$(cat "/run/kea/dhcp4-$vrf.ifindex" 2>/dev/null)" ]]; then
            verb=restart
          fi
        fi
        if [[ $verb == reload ]]; then
          systemctl reload "$unit" || failed+=("reload $unit")
          continue
        fi
        systemctl reset-failed "$unit" 2>/dev/null || true
        if systemctl "$verb" --no-block "$unit"; then
          started+=("$unit")
        else
          failed+=("$verb $unit")
        fi
      done
      deadline=$((SECONDS + 30))
      for unit in "''${started[@]}"; do
        # Each unit is asked at least once, also after the wait for another used up the deadline.
        state=$(systemctl is-active "$unit") || true
        until [[ $state == active || $state == failed ]] || ((SECONDS >= deadline)); do
          sleep 0.2
          state=$(systemctl is-active "$unit") || true
        done
        [[ $state == active ]] || failed+=("start $unit, which is $state ($(systemctl show --value --property=SubState "$unit"))")
      done
      if ((''${#failed[@]} > 0)); then
        printf 'chalkos-dhcp: failed to %s\n' "''${failed[@]}" >&2
        exit 1
      fi
    '';
  };
  # Keeps the index of the device of the VRF $1 in /run/kea/dhcp4-$1.ifindex, and fails while the
  # VRF has no device, as unbound@ does.
  recordDevice = pkgs.writeShellScript "kea-vrf-device" ''
    set -e
    read -r ifindex <"/sys/class/net/$1/ifindex"
    echo "$ifindex" >"$RUNTIME_DIRECTORY/dhcp4-$1.ifindex"
  '';
  # A server's hardening: the user kea, raw sockets, port 67, its sockets in /run/kea and its
  # leases in /var/lib/kea, which the servers share and which outlive them. A server that stops
  # by itself, as when a link it serves goes away, starts again every 5 seconds.
  server = {
    ExecReload = "${pkgs.coreutils}/bin/kill -HUP $MAINPID";
    Restart = "on-failure";
    RestartSec = 5;
    User = "kea";
    Group = "kea";
    AmbientCapabilities = [
      "CAP_NET_BIND_SERVICE"
      "CAP_NET_RAW"
    ];
    CapabilityBoundingSet = [
      "CAP_NET_BIND_SERVICE"
      "CAP_NET_RAW"
    ];
    # Kea logs to the journal and needs no lock of a log file.
    Environment = [ "KEA_LOCKFILE_DIR=none" ];
    RuntimeDirectory = "kea";
    RuntimeDirectoryMode = "0750";
    RuntimeDirectoryPreserve = true;
    StateDirectory = "kea";
    StateDirectoryMode = "0750";
    UMask = "0027";
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
      "AF_PACKET"
    ];
    RestrictNamespaces = true;
    RestrictRealtime = true;
    RestrictSUIDSGID = true;
    LockPersonality = true;
    MemoryDenyWriteExecute = true;
    SystemCallArchitectures = "native";
    SystemCallFilter = [ "@system-service" ];
  };
in
{
  config = lib.mkIf (config.chalkos.router.enable && config.chalkos.router.dhcp.enable) {
    users.users.kea = {
      isSystemUser = true;
      group = "kea";
      description = "Kea DHCP servers";
    };
    users.groups.kea = { };

    chalkos.outputs.kea = {
      # The VRFs and the Networks tell each link's VRF and its addresses.
      inputs = [
        "DHCPSubnet"
        "VRF"
        "Network"
        "NAT64"
      ];
      render = "${lib.getExe chalkosPackages.chalkos-render} kea";
      # kea-dhcp4 -t refuses a config of links the check's network namespace lacks, so it checks
      # each config without them.
      check = ''
        t=$(mktemp -d)
        trap 'rm -rf "$t"' EXIT
        for f in "$OUT"/*.json; do
          [ -e "$f" ] || continue
          ${lib.getExe pkgs.jq} '.Dhcp4["interfaces-config"].interfaces = []' "$f" >"$t/kea.json"
          ${kea}/bin/kea-dhcp4 -t "$t/kea.json" >"$t/kea.log" 2>&1 || {
            cat "$t/kea.log" >&2
            exit 1
          }
        done
      '';
      examples = config.chalkos.router.examples;
    };
    # A subnet added, changed or removed, and a link or VRF networkd creates anew.
    chalkos.effects.dhcp = {
      outputs = [
        "kea"
        "networkd"
      ];
      reload = [ "chalkos-dhcp.service" ];
    };

    systemd.services.kea-dhcp4 = {
      description = "Kea's DHCPv4 server of the default VRF";
      after = [
        "chalkos-config.service"
        "chalkos-firewall.service"
        "systemd-networkd.service"
      ];
      # Without a rendered config, as in maintenance mode, the node runs no server.
      unitConfig.ConditionPathExists = "${dir}/kea-dhcp4.json";
      serviceConfig = server // {
        ExecStart = "${kea}/bin/kea-dhcp4 -c ${dir}/kea-dhcp4.json";
      };
    };
    # A VRF's server, whose sockets systemd binds to the VRF's device, as unbound@ does. Kea's raw
    # sockets read and send on the links themselves; its UDP socket, which binds the links'
    # addresses, takes the VRF's device to find them.
    systemd.services."kea-dhcp4@" = {
      description = "Kea's DHCPv4 server of the VRF %I";
      after = [
        "sys-subsystem-net-devices-%i.device"
        "chalkos-config.service"
        "chalkos-firewall.service"
        "systemd-networkd.service"
      ];
      unitConfig = {
        Requires = "sys-subsystem-net-devices-%i.device";
        ConditionPathExists = "${dir}/kea-dhcp4-%I.json";
      };
      serviceConfig = server // {
        ExecStartPre = "${recordDevice} %I";
        ExecStart = "${kea}/bin/kea-dhcp4 -c ${dir}/kea-dhcp4-%I.json";
        BindNetworkInterface = "%I";
      };
    };
    # Runs the servers of the rendered configs, after networkd, which creates the links and the
    # VRFs.
    systemd.services.chalkos-dhcp = {
      description = "Run the router's DHCP servers";
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
        # systemctl reaches systemd over its socket, for which root needs no capability.
        CapabilityBoundingSet = [ "" ];
        SupplementaryGroups = [ "kea" ];
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
