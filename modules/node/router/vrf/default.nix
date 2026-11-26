# VRFs on router images: the output networkd renders the VRF documents beside networkd's own, with
# chalkos-render router-networkd, as a VRF device's units and VRF= on its members' Networks.
# networkd creates a VRF on reload but neither changes an existing VRF's table nor removes a VRF
# whose unit went, so chalkos-vrf removes those before networkd creates them anew
# with their members. networkd keeps a WireGuard device whose unit went too, with its keys and
# peers, so chalkos-vrf removes those as well.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  routerLib,
  ...
}:
let
  kinds = routerLib.kinds ./kinds.nix;
  # Removes each kernel VRF no .netdev declares with its table, after flushing the unreachable
  # defaults networkd put in the table, and each WireGuard device no .netdev declares; the
  # .netdev units are those of networkd's directories, the output networkd's among them.
  prune = "${lib.getExe pkgs.chalkos.chalkd} router prune-devices";
in
{
  config = lib.mkIf config.chalkos.router.enable {
    chalkos.kinds = kinds;

    # The output is networkd's, whose directory networkd reads; the router layer renders it with
    # the VRF documents.
    chalkos.outputs.networkd = {
      inputs = lib.attrNames kinds;
      render = "${lib.getExe pkgs.chalkos.chalkos-render} router-networkd ${config.system.build.chalkosNetworkdSchemas}";
    };
    chalkos.effects.vrf = {
      outputs = [ "networkd" ];
      reload = [ "chalkos-vrf.service" ];
    };

    # Both effects fire on the networkd output. Order the cleanup before networkd reloads,
    # including when recovery requires restarting the failed cleanup unit.
    chalkos.effects.networkd.after = [ "vrf" ];

    # The same ordering applies at boot.
    systemd.services.chalkos-vrf = {
      description = "Remove the VRFs and WireGuard devices the config no longer declares";
      after = [ "chalkos-config.service" ];
      before = [ "systemd-networkd.service" ];
      wantedBy = [ "multi-user.target" ];
      unitConfig.DefaultDependencies = false;
      path = [ pkgs.chalkos.iproute2-ip ];
      serviceConfig = chalkosLib.sandbox.hardened // {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = prune;
        ExecReload = prune;
        CapabilityBoundingSet = [ "CAP_NET_ADMIN" ];
        RestrictAddressFamilies = [
          "AF_NETLINK"
          "AF_UNIX"
        ];
      };
    };
  };
}
