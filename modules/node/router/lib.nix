# What the router's features share: their enable option, the kinds the image declares, which an
# output of one feature reads of another's, the record of a VRF's device and the sandbox of their
# units.
{
  chalkosLib,
  config,
  lib,
  pkgs,
}:
let
  # The kinds the image accepts: its own and networkd's.
  accepted = chalkosLib.internal.declaredKinds config.chalkos.kinds;
in
{
  inherit accepted;

  # The option chalkos.router.<feature>.enable of a feature of the layer, which images carry
  # unless a variant leaves it out.
  feature =
    description:
    lib.mkOption {
      type = lib.types.bool;
      default = true;
      inherit description;
    };

  # Keeps the kinds of a list that the image accepts, in order, so an output reads the kinds of
  # another feature only on images that carry it.
  declared = lib.filter (k: lib.elem k accepted);

  # The ExecStartPre= of a VRF's instance of a per-VRF service, run with the service's name $1 and
  # the VRF $2: it keeps the index of the VRF's device in $RUNTIME_DIRECTORY/$1-$2.ifindex, where
  # chalkd router services and chalkd's status read it, and fails while the VRF has no device, so
  # that the instance does not start with its sockets bound to none. systemd looked the index up
  # when it set the unit's cgroup up, a moment before: a VRF removed and created anew within that
  # moment leaves a record of the new index for sockets bound to the old one. Only chalkos-vrf
  # removes a VRF, and the units that run chalkd router services run after it.
  recordDevice = pkgs.writeShellScript "chalkos-vrf-device" ''
    set -e
    read -r ifindex <"/sys/class/net/$2/ifindex"
    echo "$ifindex" >"$RUNTIME_DIRECTORY/$1-$2.ifindex"
  '';

  # The sandbox of a unit of the router: no new privileges, a read-only system, no devices, no
  # home and a private /tmp, kernel settings, modules, logs, cgroups, clock and host name out of
  # reach, no namespaces, realtime scheduling or set-user-ID files, and the system calls of a
  # system service of the native architecture. A unit adds its own capabilities and address
  # families.
  hardened = {
    NoNewPrivileges = true;
    ProtectSystem = "strict";
    ProtectHome = true;
    PrivateTmp = true;
    PrivateDevices = true;
    ProtectKernelTunables = true;
    ProtectKernelModules = true;
    ProtectKernelLogs = true;
    ProtectControlGroups = true;
    ProtectClock = true;
    ProtectHostname = true;
    RestrictNamespaces = true;
    RestrictRealtime = true;
    RestrictSUIDSGID = true;
    LockPersonality = true;
    MemoryDenyWriteExecute = true;
    SystemCallArchitectures = "native";
    SystemCallFilter = [ "@system-service" ];
  };
}
