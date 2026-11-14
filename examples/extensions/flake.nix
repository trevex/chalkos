{
  description = "chalkos extension kinds: a node's rack position and hardware";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { self, chalkos, ... }:
    let
      # One image for bare metal without Kubernetes; default.nix imports the Rack and Hardware
      # extensions.
      images = chalkos.lib.mkImages {
        # The commit the images are built from, which chalkctl shows beside an image's root hash.
        revision = self.rev or self.dirtyRev or null;
        platforms = [ "metal" ];
        variants.node = {
          kubernetes = false;
          modules = [ ./. ];
        };
      };
    in
    {
      # nix build .#node-metal -o images/node-metal: the image where the inventory, chalkos.yaml,
      # points.
      packages.x86_64-linux.node-metal = images.images.node-metal;

      # The evaluated NixOS systems, node-metal and installer, whose build outputs a guide builds
      # on their own, such as the image's schemas:
      # nix build .#nixosConfigurations.node-metal.config.system.build.chalkosSchemas
      nixosConfigurations = images.nixos;

      # nix flake check: the image's tests, chalkos.tests.
      checks.x86_64-linux = images.checks;
    };
}
