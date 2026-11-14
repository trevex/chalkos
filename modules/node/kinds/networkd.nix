# networkd's Network, NetDev and Link: config kinds whose schemas are generated from the tables of
# the systemd the image runs, so they accept exactly the sections and keys its networkd does, and
# the output that renders their documents as networkd units. They have no get function: their
# schemas exist only once systemd's tables are built, and a section of theirs may be an object or
# a list, so a path through them cannot be checked during evaluation.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (chalkosLib.internal) schema api;

  # What a document of each kind is, which its schema carries.
  descriptions = {
    Link = "A systemd .link file, which udev applies to the network devices it matches, such as to name them or set their offloads.";
    NetDev = "A systemd-networkd .netdev file, which creates a virtual network device, such as a VLAN, a bond or a bridge.";
    Network = "A systemd-networkd .network file, which configures the network devices it matches: their addresses, routes, DHCP and the virtual devices on them.";
  };
  networkdKind = kind: {
    inherit kind;
    group = api.groups.networkd;
    inherit (api) version;
    multiple = true;
    description = descriptions.${kind};
  };
  schemas = pkgs.stdenvNoCC.mkDerivation {
    name = "chalkos-networkd-schemas";
    inherit (config.systemd.package) src;
    nativeBuildInputs = [
      pkgs.chalkos.chalkos-networkd-schema
      pkgs.jq
    ];
    dontConfigure = true;
    buildPhase = ''
      runHook preBuild
      chalkos-networkd-schema \
        src/network/networkd-network-gperf.gperf \
        src/network/netdev/netdev-gperf.gperf \
        src/udev/net/link-config-gperf.gperf \
        specs
      mkdir $out
      ${lib.concatMapStrings (
        kind:
        let
          k = networkdKind kind;
          envelope = pkgs.writeText "${kind}-envelope.json" (
            builtins.toJSON (schema.envelope (k // { spec = { }; }))
          );
        in
        ''
          jq --slurpfile s specs/${lib.toLower kind}.json \
            '.properties.spec = $s[0].spec | ."$defs" = $s[0]."$defs"' \
            ${envelope} > $out/${schema.fileName k}
        ''
      ) api.networkdKinds}
      runHook postBuild
    '';
    dontInstall = true;
    dontFixup = true;
  };
in
{
  # The schemas and the kinds, which kinds/default.nix adds to those of chalkos.kinds.
  system.build.chalkosNetworkdSchemas = schemas;
  system.build.chalkosNetworkdKinds = map networkdKind api.networkdKinds;

  # The node's networkd documents as networkd units, in /run/systemd/network, where they take
  # precedence over the image's own. The renderer reads each key's networkd name from the
  # schemas, which are the image's, so the units follow from the documents alone. chalkos-config
  # renders them before networkd starts, and nothing else writes the directory. networkd has no
  # offline validator, so the output has no check: the schemas accept exactly the sections and
  # keys the image's networkd does, and the examples render when the image is built. A layer may
  # render the output with a renderer of its own, which writes the units of more kinds beside
  # these.
  chalkos.outputs.networkd = {
    inputs = api.networkdKinds;
    render = lib.mkDefault "${lib.getExe pkgs.chalkos.chalkos-render} networkd ${schemas}";
    dir = "/run/systemd/network";
    examples = ../examples/networkd;
  };
  # networkd reloads on systemctl reload as on networkctl reload. Its reload reads the .netdev and
  # .network units alone.
  chalkos.effects.networkd = {
    outputs = [ "networkd" ];
    reload = [ "systemd-networkd.service" ];
  };

  # udev applies a .link unit to a device only while it handles the device's add event, and
  # systemd-udev-trigger replays those events before chalkos-config renders the Link documents.
  # This unit replays them once the units are there and before networkd starts. It runs on a node
  # with Link documents alone, and replays the events of the devices with a bus parent alone,
  # the physical ports: each add event also has systemd-sysctl apply the sysctl.d settings of the
  # device again, which would undo what networkd and a CNI set on virtual devices. networkd
  # creates the virtual devices after this unit, and udev applies their Link documents then.
  systemd.services.chalkos-links = {
    description = "Apply the node's Link documents to its network devices";
    wantedBy = [ "sysinit.target" ];
    after = [
      "chalkos-config.service"
      "systemd-udevd.service"
      "systemd-udev-trigger.service"
    ];
    before = [
      "sysinit.target"
      "network-pre.target"
      "systemd-networkd.service"
    ];
    unitConfig = {
      DefaultDependencies = false;
      ConditionPathExistsGlob = "/run/systemd/network/*.link";
    };
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      # udev settles within seconds; a unit that hangs fails instead of holding up the boot.
      TimeoutStartSec = "30s";
      ExecStart = pkgs.writeShellScript "chalkos-links" ''
        set -eu
        udevadm=${config.systemd.package}/bin/udevadm
        "$udevadm" control --reload
        set --
        for d in /sys/class/net/*; do
          if [ -e "$d/device" ]; then
            set -- "$@" "$d"
          fi
        done
        if [ $# -gt 0 ]; then
          exec "$udevadm" trigger --action=add --settle "$@"
        fi
      '';
    };
  };
  # A delivery that changes the Link documents runs the unit again, after networkd's reload.
  # The kernel renames a device that is up, and udev renames a device again on an add event, so
  # a new name takes effect at once; networkd matches the device to the units of its new name
  # when udev has handled the event. start runs the unit also on a node whose first Link
  # document the delivery brings, where it did not run at boot.
  chalkos.effects.links = {
    kinds = [ "Link" ];
    restart = [ "chalkos-links.service" ];
    start = true;
  };
}
