{ nixpkgs }:
{
  mkImages = import ./images.nix { defaultNixpkgs = nixpkgs; };
}
