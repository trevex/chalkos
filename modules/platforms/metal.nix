# Bare metal: the base module groups, with the kernel's consoles on the first serial port and the
# screen. The screen is last, so it is /dev/console: a machine without a serial port shows the
# boot's status and the emergency shell, and the passphrase prompts appear on both. A machine whose
# BMC's serial-over-LAN is the console people watch can put its port last with chalkos.consoles.
{ lib, ... }:
{
  chalkos.consoles = lib.mkDefault [
    "ttyS0,115200"
    "tty0"
  ];
}
