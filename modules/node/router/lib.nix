# routerLib, what the router's parts share, which the router module gives them as the module
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

    # The features of the layer a variant may leave out, each with its option
    # chalkos.router.<feature>.enable.
    features = [
      "bgp"
      "nat64"
      "resolver"
      "dhcp"
      "dhcpRelay"
      "wireguard"
    ];

    # The outputs of the router layer, which the tests of its examples compare with golden files on
    # an image with every feature.
    outputs = [
      "router-sysctl"
      "router-nftables"
      "frr"
      "jool"
      "unbound"
      "networkd"
      "kea"
      "dhcp-relay"
    ];

    # The option chalkos.router.<name>.enable of a feature, true unless a variant leaves the
    # feature out; name must be one of features. Its description says that the images carry
    # `carries` and accept the kinds named.
    feature =
      name:
      {
        carries,
        kinds,
      }:
      let
        names = map (k: "`${k}`") kinds;
        kindsText =
          if lib.length names == 1 then
            "the kind ${lib.head names}"
          else
            "the kinds ${lib.concatStringsSep ", " (lib.init names)} and ${lib.last names}";
        # The text in lines of 92 characters at most, as the other descriptions are wrapped.
        wrap =
          text:
          lib.concatStringsSep "\n" (
            lib.foldl' (
              lines: word:
              let
                last = lib.last lines;
              in
              if lines == [ ] then
                [ word ]
              else if lib.stringLength last + 1 + lib.stringLength word > 92 then
                lines ++ [ word ]
              else
                lib.init lines ++ [ "${last} ${word}" ]
            ) [ ] (lib.splitString " " text)
          );
      in
      chalkosLib.internal.refuse
        (lib.optional (
          !lib.elem name routerLib.features
        ) "the router feature ${name} is missing from routerLib.features")
        {
          ${name}.enable = lib.mkOption {
            type = lib.types.bool;
            default = true;
            description = wrap (
              "Whether router images carry ${carries} and accept ${kindsText}. Only with the router "
              + "layer; set it false in a variant's modules to leave the feature out of its images."
            );
          };
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

    # The template unit <name>@ of a per-VRF service, whose sockets systemd binds to the device of
    # the VRF %I, as ip vrf exec would. systemd starts a unit whose interface it cannot find without
    # binding its sockets, so recordDevice fails it instead; without its rendered config conf, as
    # in maintenance mode, it does not start. A device that goes and comes back stops nothing: the
    # reconciler restarts the unit once the device is back. systemd may see the old device go only
    # after that, and BindsTo= would then stop the restarted unit, hence Requires=.
    vrfService =
      {
        name,
        description,
        after,
        conf,
        serviceConfig,
      }:
      {
        inherit description;
        after = [ "sys-subsystem-net-devices-%i.device" ] ++ after;
        unitConfig = {
          Requires = "sys-subsystem-net-devices-%i.device";
          ConditionPathExists = conf;
        };
        serviceConfig = serviceConfig // {
          ExecStartPre = "${routerLib.recordDevice} ${name} %I";
          BindNetworkInterface = "%I";
        };
      };

    # The unit that runs `chalkd router services <service>`, after chalkos-vrf and networkd, which
    # remove and create the VRFs. It stops the service's units without a rendered config, starts
    # those of the rendered configs that do not run, once their VRF's device exists, and reloads
    # the others (the DHCP relays restart, as dnsmasq reads its relays at start alone); `stop`
    # stops all. A VRF that chalkos-vrf removed and networkd created anew has a new ifindex, or no
    # device yet, so its unit restarts on the new device. systemctl reaches systemd over its socket, for which root
    # needs no capability; groups lets it read the records of recordDevice.
    reconciler =
      {
        service,
        description,
        after ? [ ],
        groups ? [ ],
      }:
      let
        reconcile = "${lib.getExe pkgs.chalkos.chalkd} router services ${service}";
      in
      {
        inherit description;
        after = [
          "chalkos-config.service"
          "chalkos-vrf.service"
          "systemd-networkd.service"
          "chalkos-firewall.service"
        ]
        ++ after;
        wantedBy = [ "multi-user.target" ];
        path = [ config.systemd.package ];
        serviceConfig =
          routerLib.hardened
          // {
            Type = "oneshot";
            RemainAfterExit = true;
            ExecStart = reconcile;
            ExecReload = reconcile;
            ExecStop = "${reconcile} stop";
            CapabilityBoundingSet = [ "" ];
            RestrictAddressFamilies = [ "AF_UNIX" ];
          }
          // lib.optionalAttrs (groups != [ ]) { SupplementaryGroups = groups; };
      };

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
