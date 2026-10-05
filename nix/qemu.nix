{ pkgs }:
# The io_uring main loop loses TPM emulator commands
# (https://gitlab.com/qemu-project/qemu/-/issues/4581); drop the override once fixed upstream.
pkgs.qemu_kvm.override { uringSupport = false; }
