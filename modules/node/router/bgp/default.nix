# BGP on router images whose chalkos.router.bgp.enable is true: FRR (modules/node/frr) runs a BGP
# instance per VRF, whose whole config the output frr renders from the node's BGP documents.
{
  config,
  lib,
  pkgs,
  routerLib,
  ...
}:
let
  cfg = config.chalkos.router.bgp;
  kinds = routerLib.kinds ./kinds.nix;
  render = lib.getExe pkgs.chalkos.chalkos-render;
in
{
  options.chalkos.router = routerLib.feature "bgp" {
    carries = "FRR, which runs BGP and BFD,";
    kinds = lib.attrNames kinds;
  };

  config = lib.mkIf (config.chalkos.router.enable && cfg.enable) {
    chalkos.kinds = kinds;

    chalkos.frr = {
      enable = true;
      rendered = "${config.chalkos.outputs.frr.dir}/frr.conf";
      # The config of a node without a rendered one, as in maintenance mode: FRR runs no BGP.
      fallback = pkgs.runCommand "frr-fallback.conf" { } ''
        mkdir out
        echo '{"node": {"name": ""}, "documents": {}}' | OUT=$PWD/out ${render} frr
        cp out/frr.conf $out
      '';
    };

    chalkos.outputs.frr = {
      # BGP's kinds, and the links of each VRF, the WireGuards' among them.
      inputs =
        lib.attrNames kinds
        ++ [
          "Network"
          "VRF"
        ]
        ++ routerLib.declared [ "WireGuard" ];
      render = "${render} frr";
      inherit (config.chalkos.frr) check;
    };
    # A reload of a failed frr.service restarts it, which fills the config in as a reload does.
    chalkos.effects.frr = {
      outputs = [ "frr" ];
      reload = [ "frr.service" ];
    };

    systemd.services.frr = {
      # chalkd loads the values the peers' password fields name as frr.service's credentials.
      chalkos.kinds = [ "BGPPeer" ];
      # A new peer's ports are open before FRR reloads.
      after = [ "chalkos-firewall.service" ];
    };
  };
}
