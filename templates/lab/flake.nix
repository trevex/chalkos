{
  description = "A chalkos lab cluster that chalklab runs on this machine";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { chalkos, ... }:
    {
      chalkos.lab = chalkos.lib.mkCluster { modules = [ ./cluster.nix ]; };
    };
}
