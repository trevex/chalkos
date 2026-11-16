# BGP on Kubernetes images whose chalkos.kubernetes.bgp.enable is true: the node's BGPSpeaker and
# its BGPPeers.
{
  chalkosLib,
  config,
  lib,
  ...
}:
let
  kinds = import ./kinds.nix { inherit chalkosLib lib; };
in
{
  options.chalkos.kubernetes = chalkosLib.internal.feature "Kubernetes" "bgp" {
    carries = "FRR, which runs BGP and BFD, with the Python of frr-reload.py,";
    kinds = lib.attrNames kinds;
  };

  config = lib.mkIf (config.chalkos.kubernetes.enable && config.chalkos.kubernetes.bgp.enable) {
    chalkos.kinds = kinds;
    chalkos.tests = import ./tests.nix;
  };
}
