# The evaluation tests of test/nix: each check fails to evaluate with the tests that failed.
{
  pkgs,
  self,
  checkLib,
  ...
}:
let
  arguments = {
    inherit (pkgs) lib;
    flakeLib = self.lib;
    layers = self.nixosModules;
  };
in
{
  eval = checkLib.evalTests "eval" "evaluation" (
    import ../../test/nix/eval.nix (arguments // { inherit pkgs; })
  );
  eval-kinds = checkLib.evalTests "eval-kinds" "kind evaluation" (
    import ../../test/nix/kinds.nix arguments
  );
}
