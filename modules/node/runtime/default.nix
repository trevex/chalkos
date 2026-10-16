# Runtime access to config: units declare the config kinds they read. chalkd writes the node's
# documents of those kinds below /run/chalkos/config, the values their secretRef fields name into
# the unit's credentials, and restarts the unit when one of its kinds changes. chalkcfg prints the
# documents, on the system path of every image and on the PATH of every consumer.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  chalkcfg = pkgs.callPackage ../../../nix/chalkcfg.nix { };
  consumers = config.chalkos.consumers;
  consumer = lib.types.submodule {
    options = {
      kinds = lib.mkOption {
        type = lib.types.nonEmptyListOf lib.types.str;
        example = [ "BGPPeering" ];
        description = ''
          Config kinds the unit reads, at least one, by name: attributes of `chalkos.kinds`, or
          networkd's `Network`, `NetDev` and `Link`. The documents of a kind of `chalkos.kinds` are
          in its `dir`, networkd's below `/run/chalkos/config/networkd.chalkos.dev`. The values
          their `secretRef` fields name are the unit's credentials `secret_<secret>.<key>`.
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
    lib.mapAttrsToList (unit: c: map (kind: { inherit unit kind; }) c.kinds) consumers
  );
  unknown = lib.filter (r: !lib.elem r.kind declaredKinds) requested;
  secretKinds = lib.filter (r: r.kind == "Secret") requested;
  addonKinds = lib.filter (r: r.kind == "Addon") requested;
  # The consumers, once their kinds are known: a unit must not start without the documents it
  # expects, so a misspelled kind fails the evaluation.
  checkedConsumers =
    if unknown != [ ] then
      let
        r = lib.head unknown;
      in
      throw "chalkos.consumers.${r.unit}.kinds: the image declares no kind ${r.kind}; it declares ${lib.concatStringsSep ", " (lib.sort lib.lessThan declaredKinds)}"
    else if secretKinds != [ ] then
      throw "chalkos.consumers.${(lib.head secretKinds).unit}.kinds: a unit does not read Secret documents; it gets the values the secretRef fields of its kinds name"
    else if addonKinds != [ ] then
      throw "chalkos.consumers.${(lib.head addonKinds).unit}.kinds: a unit does not read Addon documents; they stay in the node's stored config, and chalkd applies their objects on control planes"
    else
      consumers;

  # The credential settings below would otherwise create an empty unit for a misspelled name.
  # A warning, not an assertion: units from systemd.packages have no ExecStart in the config.
  orphan =
    name: _:
    let
      service = config.systemd.services.${name};
    in
    lib.optional (!(service.serviceConfig ? ExecStart || service.script != "")) ''
      chalkos.consumers.${name} may name no service: systemd.services.${name} defines
      neither serviceConfig.ExecStart nor script. Ignore this if the unit comes from
      systemd.packages.
    '';
in
{
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.consumers = lib.mkOption {
        type = lib.types.attrsOf consumer;
        default = { };
        description = "Services, by name, that read config kinds.";
      };
    };
  };

  config = {
    warnings = lib.concatLists (lib.mapAttrsToList orphan consumers);

    # systemd loads every file of the directory as the credential secret_<file>; chalkd keeps
    # the directory, empty or not, for every consumer.
    systemd.services = lib.mapAttrs (unit: _: {
      serviceConfig.LoadCredential = [ "secret:/run/chalkos/credentials/${unit}" ];
      path = [ chalkcfg ];
    }) checkedConsumers;

    # The installer is no variant and never has config to query.
    environment.systemPackages = lib.mkIf (config.chalkos.variant.name != null) [ chalkcfg ];

    environment.etc."chalkos/consumers.json".text = builtins.toJSON (
      lib.mapAttrs (_: c: { inherit (c) kinds restartOnChange; }) checkedConsumers
    );
  };
}
