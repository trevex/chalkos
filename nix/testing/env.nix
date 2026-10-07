# Tools and variables the Go tests need, shared by the dev shell and the flake checks.
{ pkgs, self }:
let
  secureboot = self.packages.${pkgs.stdenv.hostPlatform.system}.test-secureboot;
in
{
  tools = [
    (import ../qemu.nix { inherit pkgs; })
    pkgs.swtpm
    pkgs.mtools
    pkgs.sbsigntool
    pkgs.dosfstools
    # VM-to-VM networking, and the forwards that reach registries the tests serve.
    pkgs.vde2
    pkgs.socat
  ];

  vars = {
    CHALKLAB_OVMF_CODE = "${pkgs.OVMFFull.fd}/FV/OVMF_CODE.fd";
    CHALKLAB_OVMF_VARS = "${pkgs.OVMFFull.fd}/FV/OVMF_VARS.fd";
    CHALKLAB_OVMF_VARS_ENROLLED = "${secureboot}/OVMF_VARS.enrolled.fd";
    CHALKLAB_SB_KEYS = "${secureboot}";
    # Any valid PE binary works for the signing tests; systemd-boot is small and always available.
    CHALKOS_TEST_EFI = "${pkgs.systemd}/lib/systemd/boot/efi/systemd-bootx64.efi";
  };
}
