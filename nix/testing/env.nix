# Tools and variables the Go tests need, shared by the dev shell and the flake checks.
{ pkgs, self }:
let
  chalkPkgs = self.packages.${pkgs.stdenv.hostPlatform.system};
  secureboot = chalkPkgs.test-secureboot;
in
{
  tools = [
    # Headless, from the binary cache; the tests deny it io_uring, whose main loop loses TPM
    # emulator commands.
    pkgs.qemu_test
    pkgs.swtpm
    pkgs.mtools
    pkgs.sbsigntool
    pkgs.dosfstools
    # veritysetup, which the verity tests check their trees against.
    pkgs.cryptsetup
    # sfdisk, with which the upgrade tests partition a disk image, and blkid and the file system
    # tools systemd-repart runs when the install tests lay out theirs.
    pkgs.util-linux
    pkgs.e2fsprogs
    # VM-to-VM networking, and the forwards that reach registries the tests serve.
    pkgs.vde2
    pkgs.socat
    # Interfaces for the tests of the virtual IPs, which run in a network namespace of their own.
    pkgs.iproute2
  ];

  vars = {
    CHALKLAB_OVMF_CODE = "${pkgs.OVMFFull.fd}/FV/OVMF_CODE.fd";
    CHALKLAB_OVMF_VARS = "${pkgs.OVMFFull.fd}/FV/OVMF_VARS.fd";
    CHALKLAB_OVMF_VARS_ENROLLED = "${secureboot}/OVMF_VARS.enrolled.fd";
    CHALKLAB_SB_KEYS = "${secureboot}";
    # Any valid PE binary works for the signing tests; systemd-boot is small and always available.
    CHALKOS_TEST_EFI = "${pkgs.systemd}/lib/systemd/boot/efi/systemd-bootx64.efi";
    # The install tests lay out disk images with the systemd-repart the images run; systemd itself
    # stays off the PATH.
    CHALKOS_TEST_REPART = "${pkgs.systemd}/bin/systemd-repart";
    # The install tests also lay out a disk as the test role's image would be installed.
    CHALKOS_TEST_ROLE_DEFINITIONS = "${chalkPkgs.test-repart-definitions}";
  };
}
