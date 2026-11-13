# The kernel's consoles: where the kernel, the initrd and systemd write, and where systemd asks
# for passphrases. The last one is /dev/console. While that is a virtual terminal,
# chalkos-serial-log@ copies chalkd's lines from the journal to each serial port of the list.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  parameters = map (c: "console=${c}") config.chalkos.consoles;
  devices = map (c: lib.head (lib.splitString "," c)) config.chalkos.consoles;
  # The entries that name a serial port, such as ttyS0, ttyAMA0 or ttyUSB0, or a hypervisor
  # console, such as hvc0. An entry of another form, such as uart8250,mmio32,0xfe215040, names no
  # device file, and only the kernel's messages reach it.
  serialPorts = lib.filter (d: builtins.match "(tty[A-Za-z]+|hvc)[0-9]+" d != null) devices;
  terminalLast = builtins.match "tty[0-9]+" (lib.last devices) != null;
  instances = lib.optionals terminalLast (map (p: "chalkos-serial-log@${p}.service") serialPorts);
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
      the console shows the fingerprint and addresses. While the last entry is a virtual
      terminal, the unit `chalkos-serial-log@` copies chalkd's lines from the journal to every
      serial port of the list, a device such as `ttyS0`, `ttyAMA0`, `ttyUSB0` or `hvc0`, so a
      BMC's serial-over-LAN shows them. An entry of another form, such as
      `uart8250,mmio32,0xfe215040`, gets only the kernel's messages.

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

    # chalkd's lines reach /dev/console, or the journal alone while the console runs on the
    # screen. While /dev/console is a virtual terminal, journalctl copies them to each serial port
    # of the list, where a BMC's serial-over-LAN shows them. It starts from the cursor it left, so
    # a restart, such as after a serial getty's hangup, repeats no line, and from the boot's start
    # the first time.
    systemd.targets.multi-user.wants = instances;
    systemd.services."chalkos-serial-log@" = lib.mkIf (instances != [ ]) {
      description = "chalkd's lines on the serial console %i";
      # The driver of x86's first serial ports registers them whether a UART is there or not,
      # with type 0 when none answers; such a port gets no lines.
      unitConfig.ConditionPathExists = "/sys/class/tty/%i";
      # It follows chalkd for as long as the node runs, so it never stops restarting.
      startLimitIntervalSec = 0;
      # journalctl writes to a terminal without colours. A TERM of its own keeps systemd from
      # asking the terminal for its type with an escape sequence, which would show on the port.
      environment = {
        SYSTEMD_COLORS = "0";
        TERM = "dumb";
      };
      serviceConfig = {
        ExecCondition = "${pkgs.runtimeShell} -c '! ${pkgs.gnugrep}/bin/grep -qsx 0 /sys/class/tty/%i/type'";
        ExecStart = lib.concatStringsSep " " [
          "${config.systemd.package}/bin/journalctl"
          "--follow"
          "--boot"
          "--lines=all"
          "--no-pager"
          "--output=cat"
          "--cursor-file=/run/chalkos-serial-log/%i.cursor"
          "_SYSTEMD_UNIT=chalkd.service"
        ];
        Restart = "always";
        # A getty that starts on the port hangs it up, which ends journalctl; it starts again soon.
        RestartSec = 1;
        RestartSteps = 3;
        RestartMaxDelaySec = 5;
        RuntimeDirectory = "chalkos-serial-log";
        RuntimeDirectoryPreserve = true;
        StandardInput = "null";
        StandardOutput = "tty";
        StandardError = "journal";
        TTYPath = "/dev/%i";
        TTYReset = false;
        TTYVHangup = false;
        TTYVTDisallocate = false;
        # It reads the journal and writes its cursor; it needs no network and no capability: as
        # root it owns the journal's files.
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
  };
}
