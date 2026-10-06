{ pkgs, self }:
let
  chalkPkgs = self.packages.${pkgs.stdenv.hostPlatform.system};
  testEnv = import ./testing/env.nix { inherit pkgs self; };

  # Tests skip when a tool or variable is missing, so a check must not pass on skipped or zero tests.
  runTests = command: ''
    set -o pipefail
    ${command} 2>&1 | tee "$TMPDIR/test.log"
    if grep -q -- '--- SKIP' "$TMPDIR/test.log"; then
      echo "error: a test was skipped" >&2
      exit 1
    fi
    if ! grep -q -- '--- PASS' "$TMPDIR/test.log"; then
      echo "error: no test ran" >&2
      exit 1
    fi
  '';

  e2e =
    name: pattern:
    pkgs.runCommand "chalkos-e2e-${name}"
      (
        {
          requiredSystemFeatures = [ "kvm" ];
          nativeBuildInputs = testEnv.tools ++ [ chalkPkgs.chalklab-e2e ];
          CHALKLAB_IMAGE_DIR = "${chalkPkgs.test-image}";
        }
        // testEnv.vars
      )
      ''
        export HOME=$TMPDIR
        ${runTests "chalklab-e2e -test.v -test.run '${pattern}' -test.timeout 60m"}
        touch $out
      '';
in
{
  go-unit = chalkPkgs.chalkctl.overrideAttrs (
    old:
    {
      pname = "chalkos-go-unit";
      nativeBuildInputs = old.nativeBuildInputs ++ testEnv.tools;
      buildPhase = ''
        runHook preBuild
        ${runTests "go test -v ./pkg/... ./cmd/..."}
        runHook postBuild
      '';
      doCheck = false;
      installPhase = "touch $out";
      postFixup = "";
    }
    // testEnv.vars
  );

  e2e-firmware = e2e "firmware" "^TestFirmwareBoots$";
  e2e-image = e2e "image" "^TestImageBootsWithoutSecureBoot$";
  e2e-secureboot = e2e "secureboot" "^TestSecureBoot";
  e2e-verity = e2e "verity" "^TestVerityRejectsTamperedStore$";
}
