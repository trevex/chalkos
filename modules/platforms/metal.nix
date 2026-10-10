# Bare metal: the base module groups, with the console on the screen and the first serial port,
# where a BMC's serial-over-LAN shows it. The last console is the one systemd writes to.
{
  boot.kernelParams = [
    "console=tty0"
    "console=ttyS0,115200"
  ];
}
