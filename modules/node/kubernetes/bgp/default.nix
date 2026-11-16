# BGP on Kubernetes images whose chalkos.kubernetes.bgp.enable is true: FRR (modules/node/frr)
# runs the sessions of the node's BGPSpeaker with its BGPPeers, whose config the output bgp
# renders. FRR announces the addresses of the speaker's export interfaces, the node's own and the
# VIP while the node holds it on one of them, and installs the default routes its peers send.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  kinds = import ./kinds.nix { inherit chalkosLib lib; };
  render = lib.getExe pkgs.chalkos.chalkos-render;
in
{
  options.chalkos.kubernetes = chalkosLib.internal.feature "Kubernetes" "bgp" {
    carries = "FRR, which runs BGP and BFD,";
    kinds = lib.attrNames kinds;
  };

  config = lib.mkIf (config.chalkos.kubernetes.enable && config.chalkos.kubernetes.bgp.enable) {
    chalkos.kinds = kinds;
    chalkos.tests = import ./tests.nix;

    chalkos.frr = {
      enable = true;
      rendered = "${config.chalkos.outputs.bgp.dir}/frr.conf";
      # The config of a node without a rendered one, as in maintenance mode: FRR runs no BGP.
      fallback = pkgs.runCommand "frr-fallback.conf" { } ''
        mkdir out
        echo '{"node": {"name": ""}, "documents": {}}' | OUT=$PWD/out ${render} kubernetes-frr
        cp out/frr.conf $out
      '';
    };

    # The speaker and its peers, and the Node, whose IPv4 address is the router ID of a speaker
    # that gives none.
    chalkos.outputs.bgp = {
      inputs = lib.attrNames kinds ++ [ "Node" ];
      render = "${render} kubernetes-frr";
      inherit (config.chalkos.frr) check;
    };
    # A reload of a failed frr.service restarts it, which fills the config in as a reload does.
    chalkos.effects.bgp = {
      outputs = [ "bgp" ];
      reload = [ "frr.service" ];
    };
    # chalkd loads the values the peers' password fields name as frr.service's credentials.
    systemd.services.frr.chalkos.kinds = [ "BGPPeer" ];
    # BFD's control packets arrive on UDP port 3784, from a source port of the neighbour's
    # choosing, so no connection the firewall tracks covers them.
    networking.firewall.allowedUDPPorts = [ 3784 ];
  };
}
