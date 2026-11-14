# chalklab, with the tools a lab runs: QEMU, OVMF with Secure Boot, swtpm, the switch, socat for
# guest forwards, the tools that sign images and enroll the lab's keys, and chalkctl. A package of
# its own, so chalkctl's closure stays free of them. QEMU is the headless one of the binary cache;
# chalklab denies it io_uring, whose main loop loses TPM emulator commands.
{
  OVMFFull,
  buildGoProgram,
  chalkctl,
  coreutils,
  lib,
  makeWrapper,
  mtools,
  python3Packages,
  qemu_test,
  sbsigntool,
  socat,
  swtpm,
  treeGoPaths,
  vde2,
}:
buildGoProgram {
  pname = "chalklab";
  paths = treeGoPaths;
  nativeBuildInputs = [ makeWrapper ];
  postFixup = ''
    wrapProgram $out/bin/chalklab --prefix PATH : ${
      lib.makeBinPath [
        qemu_test
        swtpm
        vde2
        socat
        mtools
        sbsigntool
        coreutils
        python3Packages.virt-firmware
        chalkctl
      ]
    } \
      --set-default CHALKLAB_OVMF_CODE ${OVMFFull.fd}/FV/OVMF_CODE.fd \
      --set-default CHALKLAB_OVMF_VARS ${OVMFFull.fd}/FV/OVMF_VARS.fd
  '';
}
