# The router's DHCP relays: dnsmasq in relay-only mode, whose config the output dhcp-relay renders
# from the DHCPRelay documents, dnsmasq-relay.conf for the default VRF's and
# dnsmasq-relay-<vrf>.conf for a VRF's. The default VRF's runs as dnsmasq-relay.service, each
# VRF's as dnsmasq-relay@<vrf>.service, whose sockets systemd binds to the VRF's device.
# chalkos-dhcp-relays starts the relays of the rendered configs, restarts those that run, as
# dnsmasq reads its relays at start alone, and stops those without a config. A relay keeps no
# state, and finds its links' addresses as requests come, so it starts before its links exist.
{
  config,
  lib,
  pkgs,
  routerLib,
  ...
}:
let
  cfg = config.chalkos.router.dhcpRelay;
  inherit (routerLib) hardened;
  kinds = routerLib.kinds ./kinds.nix;
  inherit (pkgs) dnsmasq;
  dir = config.chalkos.outputs.dhcp-relay.dir;
  # chalkd router services relay stops the relays without a config, then starts the relay of each
  # rendered config that does not run, once its VRF's device exists, and restarts those that run;
  # with the argument stop, it stops all of them. A VRF's relay keeps the index of the device its
  # sockets are bound to in /run/dnsmasq-relay/dnsmasq-relay-<vrf>.ifindex. systemctl comes from
  # the unit's PATH.
  reconcile = "${lib.getExe pkgs.chalkos.chalkd} router services relay";
  # A relay's hardening: the user dnsmasq, which dnsmasq stays as, with the capabilities it
  # demands of a relay: port 67 and 547, the ICMPv6 socket of DHCPv6 and the ARP entries of
  # DHCPv4 clients without an address. It writes no lease file and no pid file.
  relay = hardened // {
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
    RestrictAddressFamilies = [
      "AF_UNIX"
      "AF_NETLINK"
      "AF_INET"
      "AF_INET6"
    ];
  };
  start =
    conf:
    "${dnsmasq}/bin/dnsmasq --keep-in-foreground --log-facility=- --pid-file= --conf-file=${conf}";
in
{
  options.chalkos.router.dhcpRelay.enable = routerLib.feature "dhcpRelay" ''
    Whether router images carry the DHCP relay, dnsmasq, and accept the kind DHCPRelay; only
    with the router layer. Set it false in a variant's modules to leave dnsmasq out of its
    images.
  '';

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;

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
      inputs =
        lib.attrNames kinds
        ++ [
          "VRF"
          "Network"
        ]
        ++ routerLib.declared [
          "DHCPSubnet"
          "NAT64"
        ];
      render = "${lib.getExe pkgs.chalkos.chalkos-render} dhcp-relay";
      check = ''
        for f in "$OUT"/*.conf; do
          [ -e "$f" ] || continue
          ${dnsmasq}/bin/dnsmasq --test --conf-file="$f" >/dev/null
        done
      '';
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
        ExecStartPre = "${routerLib.recordDevice} dnsmasq-relay %I";
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
      serviceConfig = hardened // {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = reconcile;
        ExecReload = reconcile;
        ExecStop = "${reconcile} stop";
        CapabilityBoundingSet = [ "" ];
        RestrictAddressFamilies = [ "AF_UNIX" ];
      };
    };
  };
}
