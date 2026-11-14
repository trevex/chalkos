# chalkos's library, the flake's lib output: the helpers the images' modules get as chalkosLib,
# here built from chalkos's nixpkgs for code outside an image, and mkImages, which builds a
# cluster's images.
{ nixpkgs, overlay }:
import ./chalkos.nix { inherit (nixpkgs) lib; }
// {
  mkImages = import ./images.nix {
    defaultNixpkgs = nixpkgs;
    inherit overlay;
  };
}
