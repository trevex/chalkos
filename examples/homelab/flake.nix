{
  description = "A chalkos home lab cluster";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { self, chalkos, ... }:
    let
      # One image for every node, on bare metal; cp1's Node document makes it the control plane.
      # The Rack extension adds the Rack kind and the unit that reads it; the Hardware extension a
      # kind rendered into a file a unit reads.
      images = chalkos.lib.mkImages {
        # The commit the images are built from, which chalkctl shows beside an image's root hash.
        revision = self.rev or self.dirtyRev or null;
        platforms = [ "metal" ];
        variants.node.modules = [
          ./extensions/rack.nix
          ./extensions/hardware.nix
        ];
      };
    in
    {
      # nix build .#node-metal -o images/node-metal: the image where the inventory, chalkos.yaml,
      # points.
      packages.x86_64-linux.node-metal = images.images.node-metal;
    };
}
