# The flake's checks, by what they test. Each file holds checks of its own; a name in two of them
# fails the evaluation.
{ pkgs, self }:
let
  inherit (pkgs) lib;
  arguments = {
    inherit pkgs self;
    checkLib = import ./lib.nix { inherit pkgs self; };
  };
in
lib.foldl'
  (
    checks: file:
    let
      more = import file arguments;
      both = lib.intersectLists (lib.attrNames checks) (lib.attrNames more);
    in
    assert lib.assertMsg (both == [ ]) "${toString file} defines checks again: ${toString both}";
    checks // more
  )
  { }
  [
    ./e2e.nix
    ./units.nix
    ./image.nix
    ./eval.nix
    ./repo.nix
  ]
