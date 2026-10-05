{
  pkgs,
  nixpkgs,
  self,
}:
{
  test-secureboot = import ./testing/secureboot.nix { inherit pkgs; };
}
