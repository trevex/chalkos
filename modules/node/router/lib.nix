# routerLib, what the router's parts share: the enable option of a feature, the kinds the image
# declares, which an output of one part reads of another's, the record of a VRF's device, the
# sandbox of their units, the option types and CEL the router kinds share (types, from types.nix)
# and the loader of a part's kinds file. The router module gives it to its parts as the module
# argument routerLib.
{
  chalkosLib,
  config,
  lib,
  pkgs,
}:
let
  routerLib = {
    # The kinds the image accepts: its own and networkd's.
    accepted = chalkosLib.internal.declaredKinds config.chalkos.kinds;

    # The option types and CEL the router kinds share.
    types = import ./types.nix { inherit chalkosLib lib; };

    # The kinds of a part of the router: its kinds.nix, a function of { chalkosLib, lib, routerLib }
    # that returns them, loaded only here, the way callPackage loads a package file. The names of
    # the kinds it returns must not depend on accepted or declared, which read them.
    kinds = path: import path { inherit chalkosLib lib routerLib; };

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
    declared = lib.filter (k: lib.elem k routerLib.accepted);

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
  };
in
routerLib
