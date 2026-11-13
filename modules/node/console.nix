# The console on the machine's screen, for a node that chalkctl cannot reach: chalkd serves it
# the node API on a local socket, and lets it act on the node, as these options say.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.chalkos.console;
in
{
  options.chalkos.console = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Whether chalkd serves the console on the machine's screen, through the socket
        `/run/chalkd/console.sock`, which root alone reaches, without TLS. The console holds no
        certificate: it calls what the API reference says the console may call.
      '';
    };
    actions.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Whether the console may reboot and shut down the node and roll back its image and its
        config. Anyone at the machine's keyboard can use them; chalkd refuses them on the
        console's socket when this is false.
      '';
    };
  };

  config = lib.mkMerge [
    {
      # The kernel's console=ttyS0 makes systemd start a getty on the first serial port, which
      # restarts without end on a machine that has none. On x86 the 8250 driver registers ttyS0
      # whether a UART is there or not, so the device's path exists on such a machine too, and
      # its type is 0, unknown; QEMU's 16550 is of type 4.
      systemd.services."serial-getty@ttyS0" = {
        overrideStrategy = "asDropin";
        unitConfig.ConditionPathExists = "/sys/class/tty/ttyS0/device";
        serviceConfig.ExecCondition = "${pkgs.runtimeShell} -c '! ${pkgs.gnugrep}/bin/grep -qx 0 /sys/class/tty/ttyS0/type'";
      };
    }
    (lib.mkIf cfg.enable {
      systemd.services.chalkd.environment = {
        CHALKD_CONSOLE = "1";
        CHALKD_CONSOLE_ACTIONS = if cfg.actions.enable then "1" else "0";
      };
    })
  ];
}
