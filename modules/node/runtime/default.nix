# Runtime access to config: units declare the config kinds they read, and when a delivery restarts
# or reloads them. chalkd writes the node's documents of those kinds below /run/chalkos/config and
# the values their secretRef fields name into the unit's credentials, and once a delivery is
# applied evaluates each unit's restart and reload rules: by default a unit restarts when one of
# its kinds changes. chalkcfg prints the documents, on the system path of every image and on the
# PATH of every consumer of kinds.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  chalkcfg = pkgs.callPackage ../../../nix/chalkcfg.nix { };
  cel = import ./cel.nix { inherit lib; };
  consumers = config.chalkos.consumers;
  consumer = lib.types.submodule (
    { name, config, ... }:
    {
      options = {
        kinds = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
          example = [ "BGPPeering" ];
          description = ''
            Config kinds the unit reads, by name: attributes of `chalkos.kinds`, or networkd's
            `Network`, `NetDev` and `Link`. The documents of a kind of `chalkos.kinds` are in its
            `dir`, networkd's below `/run/chalkos/config/networkd.chalkos.dev`. The values their
            `secretRef` fields name are the unit's credentials `secret_<secret>.<key>`. A unit of
            no kinds reads no documents and loads no credentials, and has its `restart` and
            `reload` rules alone.
          '';
        };
        restart = {
          when = lib.mkOption {
            type = lib.types.nonEmptyStr;
            # As the unit's documents and credentials change: a Secret that a document of its
            # kinds references counts as a change of that kind.
            default = if config.kinds == [ ] then "false" else cel.anyChanged config.kinds;
            defaultText = lib.literalMD ''`"<kind>" in changedKinds || …` for each of `kinds`, or `false` for a unit of no kinds'';
            example = ''"BGPPeering" in changedKinds || facts.share != oldFacts.share'';
            description = ''
              CEL expression that is true when a delivery restarts the unit. Besides `config`,
              `oldConfig` and `changedKinds`, the names of the kinds whose documents, or the
              Secrets they reference, differ between the two, it reads `facts.<name>` after the
              delivery and `oldFacts.<name>` before it. `false` never restarts the unit. chalkd
              restarts every unit whose rule is true in one `systemctl try-restart`, and a unit
              that failed with `reset-failed` and `restart` instead.
            '';
          };
          start = lib.mkOption {
            type = lib.types.bool;
            default = false;
            description = "Start the unit when it does not run, with `systemctl restart` instead of `try-restart`, as for a unit whose condition only the delivery meets.";
          };
        };
        reload = {
          when = lib.mkOption {
            type = lib.types.nonEmptyStr;
            default = "false";
            example = ''changedKinds.exists(k, k in ["Link", "NetDev", "Network"])'';
            description = "CEL expression that is true when a delivery reloads the unit, with the variables of `restart.when`. chalkd reloads each such unit that is active before it restarts any, and does not reload a unit it restarts.";
          };
          command = lib.mkOption {
            type = lib.types.nonEmptyStr;
            default = "systemctl reload ${name}";
            defaultText = lib.literalExpression ''"systemctl reload <name>"'';
            example = "networkctl reload";
            description = "Command, with its arguments, that reloads the unit; a program of chalkd's PATH or the image's store.";
          };
        };
      };
    }
  );

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

  # The units that read documents, which get credentials and chalkcfg.
  readers = lib.filterAttrs (_: c: c.kinds != [ ]) checkedConsumers;

  # The credential settings below would otherwise create an empty unit for a misspelled name.
  # A warning, not an assertion: units from systemd.packages have no ExecStart in the config.
  orphan =
    name: c:
    let
      service = config.systemd.services.${name};
    in
    lib.optional (c.kinds != [ ] && !(service.serviceConfig ? ExecStart || service.script != "")) ''
      chalkos.consumers.${name} may name no service: systemd.services.${name} defines
      neither serviceConfig.ExecStart nor script. Ignore this if the unit comes from
      systemd.packages.
    '';

  # What chalkd reads of each consumer, which the image's rules check compiles too.
  consumersJSON = builtins.toJSON (
    lib.mapAttrs (_: c: {
      inherit (c) kinds restart reload;
    }) checkedConsumers
  );
in
{
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.consumers = lib.mkOption {
        type = lib.types.attrsOf consumer;
        default = { };
        description = "Services, by name, that read config kinds, or that a delivery restarts or reloads.";
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
    }) readers;

    # The installer is no variant and never has config to query.
    environment.systemPackages = lib.mkIf (config.chalkos.variant.name != null) [ chalkcfg ];

    system.build.chalkosConsumers = pkgs.writeText "chalkos-consumers.json" consumersJSON;
    environment.etc."chalkos/consumers.json".text = consumersJSON;
  };
}
