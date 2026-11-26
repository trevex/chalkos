# The kernel's consoles: where the kernel, the initrd and systemd write, and where systemd asks
# for passphrases. The last one is /dev/console. While that is a virtual terminal,
# chalkos-serial-log@ copies chalkd's lines from the journal to each serial port of the list.
{
  chalkosLib,
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
    # uniq: a second plain definition fails the build, so a platform's and an image's lists
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
      The kernel's consoles in order, each as the kernel's `console=` takes it, such as
      `ttyS0,115200` for the first serial port at 115200 baud or `tty0` for the screen. The
      kernel prints on every one, and systemd asks for each passphrase and recovery key on every
      one, so any of them can answer. An image whose modules set the list twice fails to build,
      unless one of them uses `lib.mkDefault` or `lib.mkForce`.

      The last entry becomes `/dev/console`, where systemd's boot status, the emergency messages and
      chalkd's lines (its fingerprint and addresses among them) appear. While it is `tty0` or
      `tty1` and the console runs on tty1, chalkd keeps off `/dev/console` and the console shows
      the fingerprint and addresses. While it is a virtual terminal, `chalkos-serial-log@` copies
      chalkd's lines from the journal to every serial port of the list (`ttyS0`, `ttyAMA0`,
      `ttyUSB0`, `hvc0`, …), so a BMC's serial-over-LAN shows them. An entry of another form,
      such as `uart8250,mmio32,0xfe215040`, gets only the kernel's messages. No entry gets a login
      prompt: no account logs in on a node.

      The `metal` platform, a platform of your own and the installer put the screen last; `kvm`
      has the first serial port alone. Where people watch a BMC's serial-over-LAN, put that port
      last, such as `[ "tty0" "ttyS0,115200" ]`, so the boot's status and the emergency messages
      appear there too.
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

    # No account logs in on a node, so no serial port gets a getty: systemd's getty generator,
    # which starts one on each serial console= and each hypervisor console, is masked.
    systemd.generators.systemd-getty-generator = "/dev/null";

    # chalkd's lines reach /dev/console, or the journal alone while the console runs on the
    # screen. While /dev/console is a virtual terminal, journalctl copies them to each serial port
    # of the list, where a BMC's serial-over-LAN shows them. It starts from the cursor it left, so
    # a restart repeats no line, and from the boot's start the first time. It runs on a port that
    # exists and has a UART: on x86 the 8250 driver registers ttyS0 whether a UART is there or
    # not, so the port exists on a machine without one too, with type 0, unknown; QEMU's 16550 is
    # of type 4. grep succeeds on a type other than 0 alone.
    systemd.targets.multi-user.wants = instances;
    systemd.services."chalkos-serial-log@" = lib.mkIf (instances != [ ]) (
      lib.mkMerge [
        {
          unitConfig.ConditionPathExists = "/sys/class/tty/%i";
          serviceConfig.ExecCondition = "${lib.getExe pkgs.gnugrep} -qsvx 0 /sys/class/tty/%i/type";
        }
        {
          description = "chalkd's lines on the serial console %i";
          # It follows chalkd for as long as the node runs, so it never stops restarting.
          startLimitIntervalSec = 0;
          # journalctl writes to a terminal without colours. A TERM of its own keeps systemd from
          # asking the terminal for its type with an escape sequence, which would show on the port.
          environment = {
            SYSTEMD_COLORS = "0";
            TERM = "dumb";
          };
          # As root it owns the journal's files; it writes only its cursor.
          serviceConfig = chalkosLib.internal.rootNoCaps // {
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
            # A hangup of the port ends journalctl; it starts again soon.
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
          };
        }
      ]
    );
  };
}
