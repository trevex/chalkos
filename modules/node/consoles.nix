# The kernel's consoles: where the kernel, the initrd and systemd write, and where systemd asks
# for passphrases. The last one is /dev/console.
{ config, lib, ... }:
let
  parameters = map (c: "console=${c}") config.chalkos.consoles;
in
{
  options.chalkos.consoles = lib.mkOption {
    # uniq: a second plain definition fails the build, so a platform's and a variant's lists
    # never join into one; a module overrides the lists of mkDefault.
    type = lib.types.uniq (
      lib.types.nonEmptyListOf (lib.types.strMatching "[A-Za-z0-9]+(,[A-Za-z0-9]+)*")
    );
    default = [
      "ttyS0,115200"
      "tty0"
    ];
    example = [
      "tty0"
      "ttyS1,115200"
    ];
    description = ''
      The kernel's consoles in order, each as the kernel's `console=` takes it: a device and its
      options, such as `ttyS0,115200` for the first serial port at 115200 baud, or `tty0` for the
      screen. The kernel prints its messages on every one, and systemd asks for each passphrase
      and recovery key on every one, so any of them can answer. The list is set once: an image
      whose modules set it twice fails to build, unless one of them uses `lib.mkDefault` or
      `lib.mkForce`.

      The last entry becomes `/dev/console`. systemd's boot status, the emergency shell and
      chalkd's lines, its fingerprint and addresses among them, appear there. While the last
      entry is `tty0` or `tty1` and the console runs on tty1, chalkd keeps off `/dev/console`, and
      the console shows the fingerprint and addresses.

      The `metal` platform, a platform of your own and the installer put the screen last; the
      `kvm` platform has the first serial port alone. A machine whose BMC shows serial-over-LAN
      as the console people watch can put that port last, such as `[ "tty0" "ttyS0,115200" ]`,
      so the boot's status and the emergency shell appear there too.
    '';
  };

  config = {
    boot.kernelParams = parameters;
    assertions = [
      {
        assertion = lib.filter (lib.hasPrefix "console=") config.boot.kernelParams == parameters;
        message = "boot.kernelParams holds console= entries of its own; set the kernel's consoles in chalkos.consoles, whose last entry is /dev/console";
      }
    ];
  };
}
