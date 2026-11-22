# The evaluation tests of test/nix: each check fails its build with the tests that failed. eval
# also holds the test images and the fixtures to the kubelets chalkos pins.
{
  pkgs,
  self,
  checkLib,
  testing,
  fixtures,
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
  eval =
    assert checkLib.kubeletsPinned "the test images" (testing // fixtures);
    checkLib.evalTests "eval" "evaluation" (
      import ../../test/nix/eval.nix (arguments // { inherit pkgs; })
    );
  eval-kinds = checkLib.evalTests "eval-kinds" "kind evaluation" (
    import ../../test/nix/kinds.nix arguments
  );
}
