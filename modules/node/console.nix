# The console on the machine's screen, for a node that chalkctl cannot reach:
# chalkos-console.service runs it on tty1 in place of the getty, and chalkd serves it the node API
# on a local socket and lets it act on the node, as these options say.
{
  chalkosLib,
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

  config = lib.mkIf cfg.enable {
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
      # Talks to chalkd's and systemd's sockets, runs journalctl and systemctl, reads /proc
      # and answers systemd's password requests (inotify on /run/systemd/ask-password, a
      # datagram to the socket each request names). As root it owns the journal's files,
      # tty1 and those sockets, which systemd creates with mode 0600 for root; the asking
      # process takes answers of root and of its own user. It writes no file.
      serviceConfig = chalkosLib.internal.unprivileged // {
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
      };
    };
    systemd.services."getty@tty1".enable = false;
    systemd.services."autovt@tty1".enable = false;
  };
}
