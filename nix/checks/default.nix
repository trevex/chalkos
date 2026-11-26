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
  names = lib.attrNames checks ++ [ "ci-tiers" ];
  tiers = import ./tiers.nix { inherit lib names; };
  # The packages of pkg/ whose names end in test, the shared test helpers, as pkg/<path>.
  helpers =
    let
      walk =
        dir: path:
        lib.concatLists (
          lib.mapAttrsToList (
            name: type:
            lib.optionals (type == "directory") (
              lib.optional (lib.hasSuffix "test" name) "${path}/${name}"
              ++ walk (dir + "/${name}") "${path}/${name}"
            )
          ) (builtins.readDir dir)
        );
    in
    walk ../../pkg "pkg";
  e2eFiles = lib.filter (lib.hasSuffix "_test.go") (lib.attrNames (builtins.readDir ../../test/e2e));
in
checks
// {
  # Every check is in exactly one tier, and .github/labels.yml has the label e2e and the labels of
  # the tiers' e2eLabels, which run end-to-end checks on a pull request, and the label ci-failure
  # of the issues CI opens. testing.md or ci.md names each check in code font, but the
  # release-image-<name> checks, which ci.md names by that pattern, and testing.md each end-to-end
  # test file and each test helper package.
  ci-tiers =
    pkgs.runCommand "chalkos-ci-tiers"
      {
        nativeBuildInputs = [ pkgs.yq-go ];
        labels = ../../.github/labels.yml;
        expected = lib.concatLines (lib.sort lib.lessThan ([ "e2e" ] ++ lib.attrNames tiers.e2eLabels));
        testing = ../../docs/contribute/testing.md;
        ci = ../../docs/contribute/ci.md;
        checks = lib.filter (name: !lib.hasPrefix "release-image-" name) names;
        pages = e2eFiles ++ helpers;
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
        status=0
        for name in $checks; do
          grep -qF -- "\`$name\`" "$testing" "$ci" || {
            echo "error: docs/contribute/testing.md and ci.md name no check $name" >&2
            status=1
          }
        done
        for name in $pages; do
          grep -qF -- "\`$name\`" "$testing" || {
            echo "error: docs/contribute/testing.md names no $name" >&2
            status=1
          }
        done
        [ "$status" = 0 ]
        touch $out
      '';
}
