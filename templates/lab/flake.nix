{
  description = "A chalkos lab cluster that chalklab runs on this machine";

  inputs.chalkos.url = "github:trevex/chalkos";

  outputs =
    { chalkos, ... }:
    let
      system = "x86_64-linux";
      pkgs = chalkos.inputs.nixpkgs.legacyPackages.${system};
    in
    {
      chalkos.lab = chalkos.lib.mkCluster { modules = [ ./cluster.nix ]; };

      # nix develop: chalklab and chalkctl of the chalkos this flake pins, and kubectl.
      devShells.${system}.default = pkgs.mkShellNoCC {
        packages = [
          chalkos.packages.${system}.chalklab
          chalkos.packages.${system}.chalkctl
          pkgs.kubectl
        ];
      };
    };
}
