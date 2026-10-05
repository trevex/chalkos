{ pkgs, self }:
pkgs.mkShell {
  packages = with pkgs; [
    go
    gopls
    qemu_kvm
    swtpm
    mtools
    sbsigntool
    dosfstools
    openssl
    python3Packages.virt-firmware
    jq
    nixfmt
  ];

  CHALKLAB_OVMF_CODE = "${pkgs.OVMFFull.fd}/FV/OVMF_CODE.fd";
  CHALKLAB_OVMF_VARS = "${pkgs.OVMFFull.fd}/FV/OVMF_VARS.fd";
  # Any valid PE binary works for the signing tests; systemd-boot is small and always available.
  CHALKOS_TEST_EFI = "${pkgs.systemd}/lib/systemd/boot/efi/systemd-bootx64.efi";
}
