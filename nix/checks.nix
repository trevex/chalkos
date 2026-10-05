{ pkgs, self }:
let
  chalkPkgs = self.packages.${pkgs.stdenv.hostPlatform.system};

  testTools = [
    (import ./qemu.nix { inherit pkgs; })
    pkgs.swtpm
    pkgs.mtools
    pkgs.sbsigntool
    pkgs.dosfstools
  ];

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
      {
        requiredSystemFeatures = [ "kvm" ];
        nativeBuildInputs = testTools ++ [ chalkPkgs.chalklab-e2e ];
        CHALKLAB_OVMF_CODE = "${pkgs.OVMFFull.fd}/FV/OVMF_CODE.fd";
        CHALKLAB_OVMF_VARS = "${pkgs.OVMFFull.fd}/FV/OVMF_VARS.fd";
        CHALKLAB_OVMF_VARS_ENROLLED = "${chalkPkgs.test-secureboot}/OVMF_VARS.enrolled.fd";
        CHALKLAB_SB_KEYS = "${chalkPkgs.test-secureboot}";
        CHALKLAB_IMAGE_DIR = "${chalkPkgs.test-image}";
      }
      ''
        export HOME=$TMPDIR
        ${runTests "chalklab-e2e -test.v -test.run '${pattern}' -test.timeout 60m"}
        touch $out
      '';
in
{
  go-unit = chalkPkgs.chalkctl.overrideAttrs (old: {
    pname = "chalkos-go-unit";
    nativeBuildInputs = old.nativeBuildInputs ++ testTools;
    CHALKOS_TEST_EFI = "${pkgs.systemd}/lib/systemd/boot/efi/systemd-bootx64.efi";
    buildPhase = ''
      runHook preBuild
      ${runTests "go test -v ./internal/... ./cmd/..."}
      runHook postBuild
    '';
    doCheck = false;
    installPhase = "touch $out";
    postFixup = "";
  });

  e2e-firmware = e2e "firmware" "^TestFirmwareBoots$";
  e2e-image = e2e "image" "^TestImageBootsWithoutSecureBoot$";
  e2e-secureboot = e2e "secureboot" "^TestSecureBoot";
  e2e-verity = e2e "verity" "^TestVerityRejectsTamperedStore$";
}
