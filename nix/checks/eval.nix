# The evaluation tests of test/nix: each check fails to evaluate with the tests that failed.
{
  pkgs,
  self,
  checkLib,
  ...
}:
let
  inherit (self.lib) mkImages;
in
{
  eval = checkLib.evalTests "eval" "evaluation" (
    import ../../test/nix/eval.nix {
      inherit pkgs mkImages;
      inherit (pkgs) lib;
    }
  );
  eval-kinds = checkLib.evalTests "eval-kinds" "kind evaluation" (
    import ../../test/nix/kinds.nix {
      inherit mkImages;
      inherit (pkgs) lib;
    }
  );
}
