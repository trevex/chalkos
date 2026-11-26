# The router's DHCP relays: dnsmasq in relay-only mode, from the configs the output dhcp-relay
# renders: dnsmasq-relay.conf for the default VRF (dnsmasq-relay.service), dnsmasq-relay-<vrf>.conf
# for a VRF (dnsmasq-relay@<vrf>.service). A relay keeps no state and finds its links' addresses as
# requests come, so it starts before its links exist.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  routerLib,
  ...
}:
let
  cfg = config.chalkos.router.dhcpRelay;
  inherit (chalkosLib.sandbox) hardened;
  kinds = routerLib.kinds ./kinds.nix;
  inherit (pkgs) dnsmasq;
  dir = config.chalkos.outputs.dhcp-relay.dir;
  # A relay's sandbox: the user dnsmasq, which dnsmasq stays as, with the capabilities it demands
  # of a relay: port 67 and 547, the ICMPv6 socket of DHCPv6 and the ARP entries of
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
  options.chalkos.router = routerLib.feature "dhcpRelay" {
    carries = "the DHCP relay, dnsmasq,";
    kinds = lib.attrNames kinds;
  };

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;

    # Pinned IDs, see router/default.nix; 332 is free in NixOS's ids.nix.
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
      check = "${lib.getExe pkgs.chalkos.chalkos-render} check each *.conf ${dnsmasq}/bin/dnsmasq --test --conf-file=$FILE";
    };
    # A relay added, changed or removed, and a VRF networkd creates anew.
    chalkos.effects.dhcp-relay = {
      outputs = [
        "dhcp-relay"
        "networkd"
      ];
      reload = [ "chalkos-dhcp-relays.service" ];
      after = [
        "networkd"
        "links"
        "router-nftables"
      ];
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
    systemd.services."dnsmasq-relay@" = routerLib.vrfService {
      name = "dnsmasq-relay";
      description = "dnsmasq's DHCP relay of the VRF %I";
      after = [
        "chalkos-config.service"
        "chalkos-firewall.service"
        "systemd-networkd.service"
      ];
      conf = "${dir}/dnsmasq-relay-%I.conf";
      serviceConfig = relay // {
        ExecStart = start "${dir}/dnsmasq-relay-%I.conf";
      };
    };
    systemd.services.chalkos-dhcp-relays = routerLib.reconciler {
      service = "relay";
      description = "Run the router's DHCP relays";
    };
  };
}
