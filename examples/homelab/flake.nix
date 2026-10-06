{
  description = "A chalkos home lab cluster";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { chalkos, ... }:
    {
      chalkos.homelab = chalkos.lib.mkCluster { modules = [ ./cluster.nix ]; };
    };
}
