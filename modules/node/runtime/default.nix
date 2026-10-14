# Runtime access to node-specific values. The identity loader writes /run/chalkos/node.json and
# one credential file per key; units declare the keys they read so chalkd can restart them when
# a key changes. Units that read config kinds declare the kinds instead: chalkd writes their
# documents below /run/chalkos/config, the values their secretRef fields name into the unit's
# credentials, and restarts the unit when one of its kinds changes.
{ config, lib, ... }:
let
  cfg = config.chalkos.node;
  kindConsumers = config.chalkos.consumers;
  credentialPath = key: "/run/chalkos/credentials/${key}";
  consumer = lib.types.submodule {
    options = {
      keys = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        example = [ "longhorn.diskPath" ];
        description = "Identity keys (dotted paths) the unit reads, each as a systemd credential of the same name.";
      };
      restartOnChange = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Restart the unit when one of its keys changes.";
      };
    };
  };
  kindConsumer = lib.types.submodule {
    options = {
      kinds = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        example = [ "BGPPeering" ];
        description = ''
          Config kinds the unit reads, by name: attributes of `chalkos.kinds`, or networkd's
          `Network`, `NetDev` and `Link`. The documents of a kind of `chalkos.kinds` are in its
          `dir`, networkd's below `/run/chalkos/config/networkd.chalkos.dev`. The values their
          `secretRef` fields name are the unit's credentials `secret_<secret>.<key>`.
        '';
      };
      restartOnChange = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Restart the unit when a document of one of its kinds changes, or a Secret one of them references.";
      };
    };
  };

  # Every kind of chalkos.kinds, and networkd's, whose names chalkos.kinds may not take.
  declaredKinds = lib.attrNames config.chalkos.kinds ++ [
    "Link"
    "NetDev"
    "Network"
  ];
  requested = lib.concatLists (
    lib.mapAttrsToList (unit: c: map (kind: { inherit unit kind; }) c.kinds) kindConsumers
  );
  unknown = lib.filter (r: !lib.elem r.kind declaredKinds) requested;
  secretKinds = lib.filter (r: r.kind == "Secret") requested;
  inBoth = lib.filter (unit: cfg.consumers ? ${unit}) (lib.attrNames kindConsumers);
  namedLikeKeys = lib.filter (
    unit: lib.any (c: lib.elem unit c.keys) (lib.attrValues cfg.consumers)
  ) (lib.attrNames kindConsumers);
  # The consumers by kind, once their kinds are known: a unit must not start without the
  # documents it expects, so a misspelled kind fails the evaluation.
  checkedKindConsumers =
    if unknown != [ ] then
      let
        r = lib.head unknown;
      in
      throw "chalkos.consumers.${r.unit}.kinds: the image declares no kind ${r.kind}; it declares ${lib.concatStringsSep ", " (lib.sort lib.lessThan declaredKinds)}"
    else if secretKinds != [ ] then
      throw "chalkos.consumers.${(lib.head secretKinds).unit}.kinds: a unit does not read Secret documents; it gets the values the secretRef fields of its kinds name"
    else if inBoth != [ ] then
      throw "chalkos.consumers.${lib.head inBoth}: the unit is in chalkos.node.consumers too; declare it in one of them"
    else if namedLikeKeys != [ ] then
      throw "chalkos.consumers.${lib.head namedLikeKeys}: /run/chalkos/credentials/${lib.head namedLikeKeys} would be the unit's credential directory and the file of the identity key of that name; rename one"
    else
      kindConsumers;

  # The credential settings below would otherwise create an empty unit for a misspelled name.
  # A warning, not an assertion: units from systemd.packages have no ExecStart in the config.
  orphan =
    option: name: _:
    let
      service = config.systemd.services.${name};
    in
    lib.optional (!(service.serviceConfig ? ExecStart || service.script != "")) ''
      ${option}.${name} may name no service: systemd.services.${name} defines
      neither serviceConfig.ExecStart nor script. Ignore this if the unit comes from
      systemd.packages.
    '';
in
{
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.node = {
        file = lib.mkOption {
          type = lib.types.str;
          readOnly = true;
          default = "/run/chalkos/node.json";
          description = "Path of the node's identity (without secrets) on the running node.";
        };
        consumers = lib.mkOption {
          type = lib.types.attrsOf consumer;
          default = { };
          description = "Services, by name, that read node-specific values.";
        };
      };
      options.consumers = lib.mkOption {
        type = lib.types.attrsOf kindConsumer;
        default = { };
        description = "Services, by name, that read config kinds.";
      };
    };
  };

  config = {
    warnings = lib.concatLists (
      lib.mapAttrsToList (orphan "chalkos.node.consumers") cfg.consumers
      ++ lib.mapAttrsToList (orphan "chalkos.consumers") kindConsumers
    );

    systemd.services = lib.mkMerge [
      (lib.mapAttrs (_: c: {
        serviceConfig.LoadCredential = map (key: "${key}:${credentialPath key}") c.keys;
      }) cfg.consumers)
      # systemd loads every file of the directory as the credential secret_<file>; chalkd keeps
      # the directory, empty or not, for every consumer by kind.
      (lib.mapAttrs (unit: _: {
        serviceConfig.LoadCredential = [ "secret:${credentialPath unit}" ];
      }) checkedKindConsumers)
    ];

    environment.etc."chalkos/consumers.json".text = builtins.toJSON (
      lib.mapAttrs (_: c: { inherit (c) keys restartOnChange; }) cfg.consumers
      // lib.mapAttrs (_: c: {
        keys = [ ];
        inherit (c) kinds restartOnChange;
      }) checkedKindConsumers
    );
  };
}
