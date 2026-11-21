# The router's DHCP servers: Kea's DHCPv4 and DHCPv6 servers, from the configs the output kea
# renders: kea-dhcp4.json and kea-dhcp6.json for the default VRF (kea-dhcp4.service,
# kea-dhcp6.service), kea-dhcp4-<vrf>.json and kea-dhcp6-<vrf>.json for a VRF
# (kea-dhcp4@<vrf>.service, kea-dhcp6@<vrf>.service). Kea refuses a config naming a link that does
# not exist and stops when a reload does, so chalkos-dhcp starts the servers once their links
# exist. A reload keeps the leases.
#
# Kea routes no prefix its DHCPv6 servers delegate: its run_script hook, chalkos-dhcp6-pd, records
# each in /var/lib/chalkos-dhcp6-pd as kea, and chalkos-dhcp6-routes, which a change there starts,
# routes each to the link-local address that asked for it. kea owns the records' directory but
# cannot write /var/lib, so it cannot put another directory in its place; chalkd reads the records
# as root and removes none.
{
  config,
  lib,
  pkgs,
  routerLib,
  ...
}:
let
  cfg = config.chalkos.router.dhcp;
  inherit (routerLib) hardened;
  kinds = routerLib.kinds ./kinds.nix;
  inherit (pkgs.chalkos) kea;
  dir = config.chalkos.outputs.kea.dir;
  pdRecords = "/var/lib/chalkos-dhcp6-pd";
  # The hook writes each record here first and renames it into pdRecords, on the same file system,
  # so that the path unit sees a record once it is whole.
  pdRecordsNew = "/var/lib/chalkos-dhcp6-pd.new";
  # Kea's run_script hook, run with the hook point's name and the leases' variables alone. A file
  # per delegated prefix, <link>_<prefix>_<length>, readable by every user, holds the link-local
  # address that asked for it and when its lease ends; release or expiry removes it. Values reach
  # a file name only once they match the patterns of a link, an IPv6 address and a length; chalkd
  # router dhcp6-routes checks them again.
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
  # A server's sandbox: user kea, port 67 or 547, sockets in /run/kea and leases in /var/lib/kea,
  # which the servers share and which outlive them. DHCPv4 uses raw sockets; DHCPv6 uses UDP and
  # runs chalkos-dhcp6-pd, which Kea runs only from KEA_HOOK_SCRIPTS_PATH. A server that stops by
  # itself, as when a link it serves goes away, starts again every 5 seconds.
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
  # The default VRF's server of a family and the template of a VRF's. DHCPv4's raw sockets work on
  # the links themselves; its UDP sockets take the VRF's device to find the links' addresses.
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
      "kea-${family}@" = routerLib.vrfService {
        name = "kea-${family}";
        description = "Kea's DHCPv${name} server of the VRF %I";
        after = [
          "chalkos-config.service"
          "chalkos-firewall.service"
          "systemd-networkd.service"
        ];
        conf = "${dir}/kea-${family}-%I.json";
        serviceConfig = server family // {
          ExecStart = "${kea}/bin/kea-${family} -c ${dir}/kea-${family}-%I.json";
        };
      };
    };
in
{
  imports = [ ./diagnostics.nix ];

  options.chalkos.router = routerLib.feature "dhcp" {
    carries = "the DHCP server, Kea, which chalkos builds from source,";
    kinds = lib.attrNames kinds;
  };

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;

    # Pinned IDs, see router/default.nix; 328 is free in NixOS's ids.nix.
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
      check = "${lib.getExe pkgs.chalkos.chalkos-render} check kea ${kea}/bin/kea-dhcp4 ${kea}/bin/kea-dhcp6";
    };
    # A subnet added, changed or removed, and a link or VRF networkd creates anew.
    chalkos.effects.dhcp = {
      outputs = [
        "kea"
        "networkd"
      ];
      reload = [ "chalkos-dhcp.service" ];
      after = [
        "networkd"
        "links"
        "router-nftables"
      ];
    };

    systemd.services =
      servers "dhcp4"
      // servers "dhcp6"
      // {
        # Also starts chalkos-dhcp6-routes, which routes the configs' delegated prefixes.
        chalkos-dhcp = routerLib.reconciler {
          service = "kea";
          description = "Run the router's DHCP servers";
          groups = [ "kea" ];
        };
        # Runs at boot after chalkos-dhcp, which also starts it, and whenever chalkos-dhcp6-pd
        # records or removes a prefix. Root with CAP_NET_ADMIN alone; it only reads the records,
        # which every user may. The kernel removes a route whose lease ended without a renewal. A
        # run takes 30 seconds at most; a burst of leases starts it as often as it comes, without
        # systemd's start limit.
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
