# The router's DHCP servers: Kea's DHCPv4 and DHCPv6 servers, whose configs the output kea renders
# from the DHCPSubnet documents, kea-dhcp4.json and kea-dhcp6.json for the default VRF's and
# kea-dhcp4-<vrf>.json and kea-dhcp6-<vrf>.json for a VRF's. The default VRF's run as
# kea-dhcp4.service and kea-dhcp6.service, each VRF's as kea-dhcp4@<vrf>.service and
# kea-dhcp6@<vrf>.service, whose sockets systemd binds to the VRF's device. Kea refuses a config
# that names a link that does not exist, and stops when a reload does, so chalkos-dhcp starts the
# servers once their links exist, reloads them when their config or the links change, and stops
# those without a config. A reload keeps the leases.
#
# Kea routes no prefix its DHCPv6 servers delegate. Their run_script hook runs chalkos-dhcp6-pd as
# kea, which records each delegated prefix in /var/lib/chalkos-dhcp6-pd; chalkos-dhcp6-routes,
# which a change there starts, routes each to the link-local address that asked for it. kea owns
# the records' directory but cannot write /var/lib, so it cannot put another directory in its
# place, and chalkd, which reads the records as root, removes none of them.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.chalkos.router.dhcp;
  routerLib = import ../lib.nix {
    inherit
      chalkosLib
      config
      lib
      pkgs
      ;
  };
  inherit (routerLib) hardened;
  kinds = import ./kinds.nix { inherit chalkosLib lib; };
  inherit (pkgs.chalkos) kea;
  dir = config.chalkos.outputs.kea.dir;
  pdRecords = "/var/lib/chalkos-dhcp6-pd";
  # The hook writes each record here first and renames it into pdRecords, on the same file system,
  # so that the path unit sees a record once it is whole.
  pdRecordsNew = "/var/lib/chalkos-dhcp6-pd.new";
  # Records the prefixes a DHCPv6 server delegates, as Kea's run_script hook runs it with the hook
  # point's name and the leases' variables alone: a file per prefix, <link>_<prefix>_<length>,
  # holding the link-local address that asked for it and when its lease ends, readable by every
  # user, which the release or the expiry of the lease removes. The values reach a file name only
  # once they match the patterns of a link, an IPv6 address and a length; chalkd router
  # dhcp6-routes checks them again.
  pdHook = pkgs.writeShellApplication {
    name = "chalkos-dhcp6-pd";
    runtimeInputs = [ pkgs.coreutils ];
    text = ''
      dir=${pdRecords}
      link=''${QUERY6_IFACE_NAME:-}
      via=''${QUERY6_REMOTE_ADDR:-}
      [[ $link =~ ^[A-Za-z0-9_.:@-]{1,15}$ ]] || link=
      # The record of the prefix $1 of the length $2 on the link the request came in on.
      record() {
        [[ -n $link && $1 =~ ^[0-9a-f:]{2,39}$ && $2 =~ ^[0-9]{1,3}$ ]] || return 1
        printf '%s/%s_%s_%s' "$dir" "$link" "$1" "$2"
      }
      case ''${1:-} in
      leases6_committed)
        for ((i = 0; i < ''${DELETED_LEASES6_SIZE:-0}; i++)); do
          t=DELETED_LEASES6_AT''${i}_TYPE a=DELETED_LEASES6_AT''${i}_ADDRESS l=DELETED_LEASES6_AT''${i}_PREFIX_LEN
          [[ ''${!t:-} == IA_PD ]] || continue
          if f=$(record "''${!a:-}" "''${!l:-}"); then rm -f "$f"; fi
        done
        for ((i = 0; i < ''${LEASES6_SIZE:-0}; i++)); do
          t=LEASES6_AT''${i}_TYPE a=LEASES6_AT''${i}_ADDRESS l=LEASES6_AT''${i}_PREFIX_LEN
          c=LEASES6_AT''${i}_CLTT v=LEASES6_AT''${i}_VALID_LIFETIME
          [[ ''${!t:-} == IA_PD && $via =~ ^fe80:[0-9a-f:]*$ && ''${!c:-} =~ ^[0-9]+$ && ''${!v:-} =~ ^[0-9]+$ ]] || continue
          f=$(record "''${!a:-}" "''${!l:-}") || continue
          # Written whole in a directory of its own, then renamed, so that chalkd never reads half a
          # record and the path unit sees the record once.
          tmp=$(mktemp "${pdRecordsNew}/XXXXXX")
          echo "$via $((''${!c} + ''${!v}))" >"$tmp"
          chmod 0644 "$tmp"
          mv -f "$tmp" "$f"
        done
        ;;
      lease6_release | lease6_decline)
        [[ ''${LEASE6_TYPE:-} == IA_PD ]] || exit 0
        if f=$(record "''${LEASE6_ADDRESS:-}" "''${LEASE6_PREFIX_LEN:-}"); then rm -f "$f"; fi
        ;;
      lease6_expire)
        # An expiry names no link: the prefix's record of any link goes.
        [[ ''${LEASE6_TYPE:-} == IA_PD && ''${LEASE6_ADDRESS:-} =~ ^[0-9a-f:]{2,39}$ && ''${LEASE6_PREFIX_LEN:-} =~ ^[0-9]{1,3}$ ]] || exit 0
        rm -f "$dir"/*_"$LEASE6_ADDRESS"_"$LEASE6_PREFIX_LEN"
        ;;
      esac
    '';
  };
  # chalkd router services kea stops the servers without a config, then starts the server of each
  # rendered config that does not run, once its links and its VRF's device exist, and reloads
  # those that run; with the argument stop, it stops all of them. systemd binds a VRF's server's
  # sockets to the index its VRF's device had when the server started, which the server keeps in
  # /run/kea/kea-<family>-<vrf>.ifindex: a VRF that chalkos-vrf removed and networkd created anew
  # has a new index, so its server restarts on the new device. It then starts
  # chalkos-dhcp6-routes, which routes the delegated prefixes of the configs. systemctl comes from
  # the unit's PATH.
  reconcile = "${lib.getExe pkgs.chalkos.chalkd} router services kea";
  # A server's hardening: the user kea, port 67 or 547, its sockets in /run/kea and its leases in
  # /var/lib/kea, which the servers share and which outlive them. The DHCPv4 servers read and
  # answer on raw sockets; the DHCPv6 servers on UDP sockets, and run chalkos-dhcp6-pd, which
  # Kea runs only from the directory KEA_HOOK_SCRIPTS_PATH names. A server that stops by itself,
  # as when a link it serves goes away, starts again every 5 seconds.
  server =
    family:
    let
      v4 = family == "dhcp4";
      capabilities = [ "CAP_NET_BIND_SERVICE" ] ++ lib.optional v4 "CAP_NET_RAW";
    in
    hardened
    // {
      ExecReload = "${pkgs.coreutils}/bin/kill -HUP $MAINPID";
      Restart = "on-failure";
      RestartSec = 5;
      User = "kea";
      Group = "kea";
      AmbientCapabilities = capabilities;
      CapabilityBoundingSet = capabilities;
      # Kea logs to the journal and needs no lock of a log file.
      Environment = [
        "KEA_LOCKFILE_DIR=none"
      ]
      ++ lib.optional (!v4) "KEA_HOOK_SCRIPTS_PATH=${pdHook}/bin";
      RuntimeDirectory = "kea";
      RuntimeDirectoryMode = "0750";
      RuntimeDirectoryPreserve = true;
      StateDirectory = "kea";
      StateDirectoryMode = "0750";
      ReadWritePaths = lib.optionals (!v4) [
        "-${pdRecords}"
        "-${pdRecordsNew}"
      ];
      UMask = "0027";
      RestrictAddressFamilies = [
        "AF_UNIX"
        "AF_NETLINK"
        "AF_INET"
        "AF_INET6"
      ]
      ++ lib.optional v4 "AF_PACKET";
    };
  # The units of the servers of a family: the default VRF's and the template of a VRF's, whose
  # sockets systemd binds to the VRF's device, as unbound@ does. Kea's DHCPv4 raw sockets read and
  # send on the links themselves; its UDP sockets take the VRF's device to find the links'
  # addresses.
  servers =
    family:
    let
      name = lib.toUpper (lib.removePrefix "dhcp" family);
    in
    {
      "kea-${family}" = {
        description = "Kea's DHCPv${name} server of the default VRF";
        after = [
          "chalkos-config.service"
          "chalkos-firewall.service"
          "systemd-networkd.service"
        ];
        # Without a rendered config, as in maintenance mode, the node runs no server.
        unitConfig.ConditionPathExists = "${dir}/kea-${family}.json";
        serviceConfig = server family // {
          ExecStart = "${kea}/bin/kea-${family} -c ${dir}/kea-${family}.json";
        };
      };
      "kea-${family}@" = {
        description = "Kea's DHCPv${name} server of the VRF %I";
        after = [
          "sys-subsystem-net-devices-%i.device"
          "chalkos-config.service"
          "chalkos-firewall.service"
          "systemd-networkd.service"
        ];
        unitConfig = {
          Requires = "sys-subsystem-net-devices-%i.device";
          ConditionPathExists = "${dir}/kea-${family}-%I.json";
        };
        serviceConfig = server family // {
          ExecStartPre = "${routerLib.recordDevice} kea-${family} %I";
          ExecStart = "${kea}/bin/kea-${family} -c ${dir}/kea-${family}-%I.json";
          BindNetworkInterface = "%I";
        };
      };
    };
in
{
  options.chalkos.router.dhcp.enable = routerLib.feature ''
    Whether router images carry the DHCP server, Kea, and accept the kind DHCPSubnet; only
    with the router layer. Set it false in a variant's modules to leave Kea out of its images,
    whose build then needs no local build of Kea.
  '';

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;
    chalkos.diagnostics = import ./diagnostics.nix { inherit lib pkgs; };

    # The images allocate the IDs of users without one statically, from 999 down, so a user
    # of an unpinned ID would move the IDs of the users after it, and the owners of their files
    # on /var, whenever an image adds or drops it. 328 is free in NixOS's ids.nix.
    users.users.kea = {
      isSystemUser = true;
      uid = 328;
      group = "kea";
      description = "Kea DHCP servers";
    };
    users.groups.kea.gid = 328;

    chalkos.outputs.kea = {
      # The VRFs and the Networks tell each link's VRF, its addresses and its router
      # advertisements, and the NAT64s, on images with NAT64, the links no subnet may name.
      inputs =
        lib.attrNames kinds
        ++ [
          "VRF"
          "Network"
        ]
        ++ routerLib.declared [ "NAT64" ];
      render = "${lib.getExe pkgs.chalkos.chalkos-render} kea ${lib.getExe pdHook}";
      # kea-dhcp4 -t and kea-dhcp6 -t refuse a config of links the check's network namespace
      # lacks, so it checks each config without them.
      check = ''
        t=$(mktemp -d)
        trap 'rm -rf "$t"' EXIT
        for f in "$OUT"/*.json; do
          [ -e "$f" ] || continue
          case $(basename "$f") in
          kea-dhcp4*) family=dhcp4 strip='.Dhcp4["interfaces-config"].interfaces = []' ;;
          *) family=dhcp6 strip='.Dhcp6["interfaces-config"].interfaces = [] | .Dhcp6.subnet6[] |= del(.interface)' ;;
          esac
          ${lib.getExe pkgs.jq} "$strip" "$f" >"$t/kea.json"
          ${kea}/bin/kea-$family -t "$t/kea.json" >"$t/kea.log" 2>&1 || {
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

    systemd.services =
      servers "dhcp4"
      // servers "dhcp6"
      // {
        # Runs the servers of the rendered configs, after networkd, which creates the links and the
        # VRFs.
        chalkos-dhcp = {
          description = "Run the router's DHCP servers";
          after = [
            "chalkos-config.service"
            "chalkos-vrf.service"
            "systemd-networkd.service"
            "chalkos-firewall.service"
          ];
          wantedBy = [ "multi-user.target" ];
          path = [ config.systemd.package ];
          serviceConfig = hardened // {
            Type = "oneshot";
            RemainAfterExit = true;
            ExecStart = reconcile;
            ExecReload = reconcile;
            ExecStop = "${reconcile} stop";
            # systemctl reaches systemd over its socket, for which root needs no capability.
            CapabilityBoundingSet = [ "" ];
            SupplementaryGroups = [ "kea" ];
            RestrictAddressFamilies = [ "AF_UNIX" ];
          };
        };
        # Routes the prefixes the DHCPv6 servers delegate, at boot, after chalkos-dhcp, which also
        # starts it, and whenever chalkos-dhcp6-pd records or removes one. It runs as root with
        # CAP_NET_ADMIN alone and only reads the records, which every user may; the kernel removes a
        # route whose lease ended without a renewal. A run takes 30 seconds at most, and a burst of
        # leases starts it as often as it comes, without systemd's limit on starts.
        chalkos-dhcp6-routes = {
          description = "Route the prefixes the router's DHCPv6 servers delegate";
          after = [ "chalkos-dhcp.service" ];
          unitConfig.StartLimitIntervalSec = 0;
          wantedBy = [ "multi-user.target" ];
          path = [ pkgs.iproute2 ];
          serviceConfig = hardened // {
            Type = "oneshot";
            ExecStart = "${lib.getExe pkgs.chalkos.chalkd} router dhcp6-routes";
            CapabilityBoundingSet = [ "CAP_NET_ADMIN" ];
            RestrictAddressFamilies = [
              "AF_UNIX"
              "AF_NETLINK"
            ];
            TimeoutStartSec = "30s";
          };
        };
      };
    systemd.paths.chalkos-dhcp6-routes = {
      description = "Watch the prefixes the router's DHCPv6 servers delegate";
      wantedBy = [ "multi-user.target" ];
      pathConfig = {
        PathChanged = pdRecords;
        # A burst of leases triggers it as often as it comes.
        TriggerLimitIntervalSec = 0;
      };
    };
    # The kernel drops the routes of a link that goes down, and nothing records a change when it
    # comes up again, so the routes are made again every minute as well.
    systemd.timers.chalkos-dhcp6-routes = {
      description = "Route the prefixes the router's DHCPv6 servers delegate again";
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnBootSec = "1min";
        OnUnitActiveSec = "1min";
      };
    };
    # The records' directory exists before a server or the path unit looks at it.
    systemd.tmpfiles.rules = [
      "d /var/lib/kea 0750 kea kea - -"
      "d ${pdRecords} 0755 kea kea - -"
      "d ${pdRecordsNew} 0700 kea kea - -"
    ];
  };
}
