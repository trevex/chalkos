# NAT64 on router images whose chalkos.router.nat64.enable is true: Jool's kernel module and tool,
# the output jool, which renders an instance of Jool and its link per NAT64 document, and
# chalkos-nat64.service, which brings Jool's instances, their namespaces and links and the routes
# to them to the rendered files at boot and on each delivery that changes them.
{
  config,
  lib,
  pkgs,
  routerLib,
  ...
}:
let
  cfg = config.chalkos.router.nat64;
  inherit (routerLib) hardened;
  kinds = routerLib.kinds ./kinds.nix;
  jool = pkgs.jool-cli;
  # chalkd router nat64 brings Jool's instances to the output jool (<name>.json is Jool's file,
  # <name>.veth the link's), each in the namespace nat64-<name> joined to its VRF by the veth pair
  # n64-<name>, and routes their prefixes and pools to the links with protocol 64, which BGP
  # announces when a network names it. It checks every file first and changes nothing when one is
  # refused; `stop` removes all, as does a missing output directory (maintenance mode).
  # pkg/chalkd/layers/router/nat64.go gives the steps.
  nat64 = "${lib.getExe pkgs.chalkos.chalkd} router nat64";
in
{
  imports = [ ./diagnostics.nix ];

  options.chalkos.router = routerLib.feature "nat64" {
    carries = "Jool, the kernel module and tool of the NAT64 instances,";
    kinds = lib.attrNames kinds;
  };

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;
    boot.extraModulePackages = [ config.boot.kernelPackages.jool ];
    # Loaded at boot, so the image's module check proves the tree holds it and what it depends on.
    boot.kernelModules = [ "jool" ];

    chalkos.outputs.jool = {
      # The VRFs' tables and the links' membership, and the Networks' static addresses, which a
      # pool must not hold.
      inputs = lib.attrNames kinds ++ [
        "VRF"
        "Network"
      ];
      render = "${lib.getExe pkgs.chalkos.chalkos-render} jool";
      # jool file check reads a file as file handle does, without the kernel.
      check = "${lib.getExe pkgs.chalkos.chalkos-render} check each *.json ${jool}/bin/jool file check $FILE";
    };
    chalkos.effects.jool = {
      outputs = [ "jool" ];
      reload = [ "chalkos-nat64.service" ];
      after = [
        "networkd"
        "router-nftables"
      ];
    };
    # The firewall forwards, translates and tracks the NAT64s' links, and networkd leaves them
    # alone.
    chalkos.outputs.router-nftables.inputs = lib.attrNames kinds;
    chalkos.outputs.networkd.inputs = lib.attrNames kinds;

    systemd.services.chalkos-nat64 = {
      description = "Run the router's NAT64 instances";
      # At boot, networkd creates the VRF each instance's link joins, and the firewall loads
      # rules for the NAT64 links before the instances start.
      after = [
        "chalkos-config.service"
        "chalkos-firewall.service"
        "systemd-networkd.service"
      ];
      before = [ "frr.service" ];
      wantedBy = [ "multi-user.target" ];
      # chalkd runs jool, ip, nsenter and, in each instance's namespace, sysctl.
      path = [
        jool
        pkgs.chalkos.iproute2-ip
        pkgs.util-linux
        pkgs.procps
      ];
      serviceConfig =
        # ip netns add mounts a namespace on /run/netns, which the host must see, so the unit has no
        # mount namespace of its own: no ProtectSystem, PrivateTmp, PrivateDevices, ProtectHome,
        # ProtectKernelLogs, ProtectKernelModules, ProtectControlGroups or ProtectKernelTunables,
        # each of which gives it one and keeps the namespaces from the host. Mounting a namespace and
        # entering it take CAP_SYS_ADMIN and the mount system calls, and RestrictNamespaces lets
        # network namespaces alone through; jool, ip and the namespaces' sysctls take CAP_NET_ADMIN.
        removeAttrs hardened [
          "ProtectSystem"
          "PrivateTmp"
          "PrivateDevices"
          "ProtectHome"
          "ProtectKernelLogs"
          "ProtectKernelModules"
          "ProtectControlGroups"
          "ProtectKernelTunables"
        ]
        // {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = nat64;
          ExecReload = nat64;
          ExecStop = "${nat64} stop";
          CapabilityBoundingSet = [
            "CAP_NET_ADMIN"
            "CAP_SYS_ADMIN"
          ];
          RestrictAddressFamilies = [
            "AF_NETLINK"
            "AF_UNIX"
          ];
          RestrictNamespaces = "net";
          SystemCallFilter = [
            "@system-service"
            "@mount"
          ];
        };
    };
    # chalkd reads Jool's sessions, bindings and pools for the node's status, with nsenter in each
    # instance's namespace.
    systemd.services.chalkd.path = [
      jool
      pkgs.util-linux
    ];
  };
}
