# The flake's checks, by what they test. Each file holds checks of its own; a name in two of them
# fails the evaluation. ci-tiers checks the tiers of tiers.nix, which CI runs the checks in.
{
  pkgs,
  self,
  testing,
  fixtures,
  docs,
}:
let
  inherit (pkgs) lib;
  arguments = {
    inherit
      pkgs
      self
      docs
      testing
      fixtures
      ;
    checkLib = import ./lib.nix {
      inherit
        pkgs
        self
        testing
        fixtures
        ;
    };
  };
  checks =
    lib.foldl'
      (
        found: file:
        let
          more = import file arguments;
          both = lib.intersectLists (lib.attrNames found) (lib.attrNames more);
        in
        assert lib.assertMsg (both == [ ]) "${toString file} defines checks again: ${toString both}";
        found // more
      )
      { }
      [
        ./e2e.nix
        ./units
        ./image.nix
        ./eval.nix
        ./repo.nix
      ];
  tiers = import ./tiers.nix {
    inherit lib;
    names = lib.attrNames checks ++ [ "ci-tiers" ];
  };
in
checks
// {
  # Every check is in exactly one tier, and .github/labels.yml has the label e2e and a label
  # e2e:<name> for each check e2e-<name>, the labels that run them on a pull request.
  ci-tiers =
    pkgs.runCommand "chalkos-ci-tiers"
      {
        nativeBuildInputs = [ pkgs.yq-go ];
        labels = ../../.github/labels.yml;
        expected = lib.concatLines (
          lib.sort lib.lessThan ([ "e2e" ] ++ map (name: "e2e:" + lib.removePrefix "e2e-" name) tiers.e2e)
        );
      }
      ''
        actual=$(yq -r '.[].name | select(test("^e2e(:|$)"))' "$labels" | LC_ALL=C sort)
        if [ "$actual" != "$(printf %s "$expected" | LC_ALL=C sort)" ]; then
          echo "error: .github/labels.yml has these e2e labels:" >&2
          echo "$actual" >&2
          echo "and not those of the e2e tier:" >&2
          printf %s "$expected" >&2
          exit 1
        fi
        touch $out
      '';
}
