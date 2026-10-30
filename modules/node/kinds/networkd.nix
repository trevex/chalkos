# networkd's Network, NetDev and Link: config kinds whose schemas are generated from the tables of
# the systemd the image runs, so they accept exactly the sections and keys its networkd does, and
# the output that renders their documents as networkd units. They have no get function: their
# schemas exist only once systemd's tables are built, and a section of theirs may be an object or
# a list, so a path through them cannot be checked during evaluation.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  schema = import ../../../lib/kinds/schema.nix { inherit lib; };
  api = import ../../../lib/kinds/api.nix;

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
      chalkosPackages.chalkos-networkd-schema
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
    render = lib.mkDefault "${lib.getExe chalkosPackages.chalkos-render} networkd ${schemas}";
    dir = "/run/systemd/network";
    examples = ../examples/networkd;
  };
  # networkd reloads on systemctl reload as on networkctl reload.
  chalkos.effects.networkd = {
    outputs = [ "networkd" ];
    reload = [ "systemd-networkd.service" ];
  };
}
