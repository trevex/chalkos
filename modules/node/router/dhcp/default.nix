# The router's DHCP servers: Kea's DHCPv4 and DHCPv6 servers, from the configs the output kea
# renders: kea-dhcp4.json and kea-dhcp6.json for the default VRF (kea-dhcp4.service,
# kea-dhcp6.service), kea-dhcp4-<vrf>.json and kea-dhcp6-<vrf>.json for a VRF
# (kea-dhcp4@<vrf>.service, kea-dhcp6@<vrf>.service). Kea refuses a config naming a link that does
# not exist and stops when a reload does, so chalkos-dhcp starts the servers once their links
# exist. A reload keeps the leases.
#
# Kea routes no prefix its DHCPv6 servers delegate: its run_script hook, chalkd router dhcp6-pd,
# records each in /var/lib/chalkos-dhcp6-pd/records as kea, and chalkos-dhcp6-routes, which a change there
# starts, routes each to the link-local address that asked for it. kea owns the records' directory but
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
  # The records and their staging directory share a parent, which root owns so that kea cannot
  # put another directory in place of either, and which a DHCPv6 server's sandbox binds whole:
  # the hook writes each record in pdRecordsNew first and renames it into pdRecords, which must
  # stay on one mount, so that the path unit sees a record once it is whole.
  pdState = "/var/lib/chalkos-dhcp6-pd";
  pdRecords = "${pdState}/records";
  pdRecordsNew = "${pdRecords}.new";
  # Kea's run_script hook, which Kea runs with the hook point's name and the leases' variables
  # alone: chalkd router dhcp6-pd, through a link of chalkd whose name gives the command. A file per
  # delegated prefix, <link>_<prefix>_<length>, readable by every user, holds the link-local address
  # that asked for it and when its lease ends; release, decline or expiry removes it. Values reach
  # a file name only once they match the patterns of a link, an IPv6 address and a length; chalkd
  # router dhcp6-routes checks them again.
  pdHook = pkgs.runCommand "chalkos-dhcp6-pd" { meta.mainProgram = "chalkd-router-dhcp6-pd"; } ''
    mkdir -p $out/bin
    ln -s ${lib.getExe pkgs.chalkos.chalkd} $out/bin/chalkd-router-dhcp6-pd
  '';
  # A server's sandbox: user kea, port 67 or 547, sockets in /run/kea and leases in /var/lib/kea,
  # which the servers share and which outlive them. DHCPv4 uses raw sockets; DHCPv6 uses UDP and
  # runs pdHook, which Kea runs only from KEA_HOOK_SCRIPTS_PATH. A server that stops by
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
      ReadWritePaths = lib.optional (!v4) "-${pdState}";
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
          path = [ pkgs.chalkos.iproute2-ip ];
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
      "d ${pdState} 0755 root root - -"
      "d ${pdRecords} 0755 kea kea - -"
      "d ${pdRecordsNew} 0700 kea kea - -"
    ];
  };
}
