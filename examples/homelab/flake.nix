{
  description = "A chalkos home lab cluster";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { chalkos, ... }:
    let
      # One image for every node, on bare metal; cp1's Node document makes it the control plane.
      # The Rack extension adds the Rack kind and the unit that reads it.
      images = chalkos.lib.mkImages {
        platforms = [ "metal" ];
        variants.node.modules = [ ./extensions/rack.nix ];
      };
    in
    {
      # nix build .#node-metal -o images/node-metal: the image where the Image document in
      # cluster/cluster.yaml points.
      packages.x86_64-linux.node-metal = images.images.node-metal;
    };
}
