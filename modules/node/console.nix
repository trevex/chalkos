# The console on the machine's screen, for a node that chalkctl cannot reach:
# chalkos-console.service runs it on tty1 in place of the getty, and chalkd serves it the node API
# on a local socket and lets it act on the node, as these options say.
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
        Whether the console runs on the machine's screen, tty1, in place of a login prompt.
        chalkd serves it, chalkos-console, through the socket `/run/chalkd/console.sock`, which
        root alone reaches, without TLS. The console holds no certificate: it calls what the API
        reference says the console may call.
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
      # A kernel console= on a serial port makes systemd start a getty there, which restarts
      # without end on a machine that has no such port. On x86 the 8250 driver registers ttyS0
      # whether a UART is there or not, so the port exists on such a machine too, and its type is
      # 0, unknown; QEMU's 16550 is of type 4. The conditions go to the template: a drop-in of the
      # same name for an instance would hide the template's, which holds agetty's ExecStart.
      systemd.services."serial-getty@" = {
        unitConfig.ConditionPathExists = "/sys/class/tty/%I";
        serviceConfig.ExecCondition = "${pkgs.runtimeShell} -c '! ${pkgs.gnugrep}/bin/grep -qsx 0 /sys/class/tty/%I/type'";
      };
    }
    (lib.mkIf cfg.enable {
      systemd.services.chalkd.environment = {
        CHALKD_CONSOLE = "1";
        CHALKD_CONSOLE_ACTIONS = if cfg.actions.enable then "1" else "0";
      };
      # The kernel prints only emergencies on its consoles, so its messages do not run over the
      # console on tty1; a panic or an oops raises the level and still prints over it. The serial
      # console ttyS0 then gets only emergencies too; the journal keeps every message.
      boot.kernel.sysctl."kernel.printk" = "1 4 1 7";
      # The console takes tty1 once chalkd serves and the volumes' passphrase prompts, which ask
      # on the console before cryptsetup.target, are answered. No account logs in on a chalkos
      # image, so the getty it replaces gave nothing.
      systemd.services.chalkos-console = {
        description = "Console on the machine's screen";
        wantedBy = [ "multi-user.target" ];
        after = [
          "chalkd.service"
          "cryptsetup.target"
          "systemd-user-sessions.service"
          "getty-pre.target"
        ];
        before = [ "getty.target" ];
        # The screen is the console's alone, so it never stops restarting; a console that keeps
        # failing restarts ever more slowly, up to every 30 seconds.
        startLimitIntervalSec = 0;
        environment = {
          TERM = "linux";
          CHALKOS_CONSOLE_ACTIONS = if cfg.actions.enable then "1" else "0";
        };
        serviceConfig = {
          ExecStart = lib.getExe pkgs.chalkos.chalkos-console;
          Restart = "always";
          RestartSec = 1;
          RestartSteps = 5;
          RestartMaxDelaySec = 30;
          TTYPath = "/dev/tty1";
          TTYReset = true;
          TTYVHangup = true;
          # A console that stopped leaves no stale screen on tty1.
          TTYVTDisallocate = true;
          StandardInput = "tty";
          StandardOutput = "tty";
          StandardError = "journal";
          # It reaches chalkd's socket, runs journalctl and systemctl, reads /proc, and answers
          # systemd's password requests: it watches /run/systemd/ask-password with inotify and
          # sends each answer as a datagram to the socket the request names. It needs no network,
          # writes no file and needs no capability: as root it owns the journal's files, the
          # sockets of chalkd and systemd, tty1, and the requests' sockets, which systemd creates
          # with mode 0600 for root, and the asking process takes answers of root and of its own
          # user. A path socket stays reachable on a read-only file system and from the private
          # network namespace, which hides abstract sockets alone.
          CapabilityBoundingSet = "";
          NoNewPrivileges = true;
          PrivateNetwork = true;
          RestrictAddressFamilies = [ "AF_UNIX" ];
          ProtectSystem = "strict";
          ProtectHome = true;
          ProtectKernelTunables = true;
          ProtectKernelModules = true;
          ProtectControlGroups = true;
          SystemCallFilter = [ "@system-service" ];
          MemoryDenyWriteExecute = true;
          LockPersonality = true;
          RestrictRealtime = true;
          SystemCallArchitectures = "native";
        };
      };
      systemd.services."getty@tty1".enable = false;
      systemd.services."autovt@tty1".enable = false;
    })
  ];
}
