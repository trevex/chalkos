{
  description = "A ChalkOS Kubernetes image with the BIRD BGP extension";

  inputs.chalkos.url = "github:trevex/chalkos/v0.5.0"; # x-release-please-version

  outputs =
    { self, chalkos, ... }:
    let
      system = "x86_64-linux";
      images = chalkos.lib.mkImages {
        revision = self.rev or self.dirtyRev or null;
        images = [
          # --8<-- [start:bird-image]
          {
            name = "node-bird-metal";
            platform = "metal";
            modules = [
              chalkos.nixosModules.kubernetes
              { chalkos.kubernetes.bgp.enable = false; }
              ./extensions
            ];
          }
          # --8<-- [end:bird-image]
        ];
      };
    in
    {
      packages.${system}.node-bird-metal = images.node-bird-metal;
      nixosConfigurations = chalkos.lib.nixosOf images;
      checks.${system} = chalkos.lib.testsOf images;
    };
}
