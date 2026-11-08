# What the router's features share: their enable option, the kinds the image declares, which an
# output of one feature reads of another's, and the sandbox of their units.
{ config, lib }:
let
  # The kinds the image accepts: its own and networkd's.
  accepted = lib.attrNames config.chalkos.kinds ++ (import ../../../lib/kinds/api.nix).networkdKinds;
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

  # The kinds of the list the image accepts, in the list's order: an output reads the kinds of
  # another feature only on images that carry it.
  declared = lib.filter (k: lib.elem k accepted);

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
