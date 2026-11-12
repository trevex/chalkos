{
  description = "A chalkos home lab: a router and three Kubernetes nodes on an IPv6 fabric";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { self, chalkos, ... }:
    let
      system = "x86_64-linux";
      pkgs = chalkos.inputs.nixpkgs.legacyPackages.${system};
      # Two images for bare metal: the router's, without Kubernetes, and the nodes'.
      # extensions/default.nix adds bird and its Speaker and Peer kinds to the nodes'.
      images = chalkos.lib.mkImages {
        # The commit the images are built from, which chalkctl shows beside an image's root hash.
        revision = self.rev or self.dirtyRev or null;
        platforms = [ "metal" ];
        variants = {
          router = {
            kubernetes = false;
            router = true;
          };
          node.modules = [ ./extensions ];
        };
      };
    in
    {
      # nix build .#<image> -o images/<image>: the images where the inventory, chalkos.yaml,
      # points. nix build .#installer: the installer's raw image and ISO, which install both.
      packages.${system} = {
        router-metal = images.images.router-metal;
        node-metal = images.images.node-metal;
        installer = images.installer;
      };

      # nix develop: chalkctl and chalkbuild of the chalkos this flake pins, and the other tools
      # the guide runs.
      devShells.${system}.default = pkgs.mkShellNoCC {
        packages = [
          chalkos.packages.${system}.chalkctl
          chalkos.packages.${system}.chalkbuild
          pkgs.kubectl
          pkgs.jq
          pkgs.openssl
          pkgs.wireguard-tools
          pkgs.arp-scan
          pkgs.netcat
        ];
      };
    };
}
