# The flake's checks, by what they test. Each file holds checks of its own; a name in two of them
# fails the evaluation. ci-tiers checks the tiers of tiers.nix, which CI runs the checks in.
{
  pkgs,
  self,
  testing,
  fixtures,
  docs,
  sbom,
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
      sbom
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
        ./release.nix
      ];
  tiers = import ./tiers.nix {
    inherit lib;
    names = lib.attrNames checks ++ [ "ci-tiers" ];
  };
in
checks
// {
  # Every check is in exactly one tier, and .github/labels.yml has the label e2e and the labels of
  # the tiers' e2eLabels, which run end-to-end checks on a pull request, and the label ci-failure
  # of the issues CI opens.
  ci-tiers =
    pkgs.runCommand "chalkos-ci-tiers"
      {
        nativeBuildInputs = [ pkgs.yq-go ];
        labels = ../../.github/labels.yml;
        expected = lib.concatLines (lib.sort lib.lessThan ([ "e2e" ] ++ lib.attrNames tiers.e2eLabels));
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
        if ! yq -e '.[] | select(.name == "ci-failure")' "$labels" >/dev/null; then
          echo "error: .github/labels.yml has no label ci-failure, which CI's failure issues carry" >&2
          exit 1
        fi
        touch $out
      '';
}
