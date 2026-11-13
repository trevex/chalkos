{ nixpkgs, goOverlay }:
{
  mkImages = import ./images.nix {
    defaultNixpkgs = nixpkgs;
    inherit goOverlay;
  };
}
