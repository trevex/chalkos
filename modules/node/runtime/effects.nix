# Effects: what a delivery does to the node's services once it is applied. An effect restarts or
# reloads units, runs a command or a layer's action in chalkd when one of its outputs changed,
# the documents of one of its kinds did, or its CEL rule holds.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  checkKinds = import ../../../lib/kinds/check.nix {
    inherit lib;
    inherit (config.chalkos) kinds;
  };
  outputs = config.chalkos.outputs;
  layerNames = lib.attrNames config.chalkos.layers;
  image =
    if config.chalkos.variant.name == null then
      "the image"
    else
      "the variant ${config.chalkos.variant.name}";

  # Returns kinds, or throws, prefixed with `where`, if kinds names a kind the image does not
  # declare, Secret or Addon. A Secret's values count for the kinds whose secretRef fields name
  # them, and Addon documents stay in the node's stored config.
  effectKinds =
    where: kinds:
    if lib.elem "Secret" kinds then
      throw "${where}: an effect does not fire on Secret documents; it fires on the kinds whose secretRef fields name them"
    else if lib.elem "Addon" kinds then
      throw "${where}: an effect does not fire on Addon documents; chalkd applies them, and a when rule reads them"
    else
      checkKinds where kinds kinds;

  units = lib.types.listOf lib.types.str;
  effect = lib.types.submodule {
    options = {
      outputs = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        example = [ "bird-conf" ];
        description = ''
          Names of outputs of `chalkos.outputs`. The effect fires when one of them changed: when
          the hash of its files differs from the one before the delivery, or the values of the
          Secrets its input documents reference do, so a rotated password fires the effect even
          though the rendered files stay the same.
        '';
      };
      kinds = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        example = [ "Peer" ];
        description = ''
          Config kinds, by name as for a grant. The effect fires when the documents of one of them
          changed: one was added, removed or changed, or a value of a Secret one of them
          references changed. A service that reads its kinds' documents when it starts needs no
          output in between.
        '';
      };
      when = lib.mkOption {
        type = lib.types.nullOr lib.types.nonEmptyStr;
        default = null;
        example = "facts.share != oldFacts.share";
        description = ''
          CEL expression that is true when a delivery takes the effect, for a trigger that no
          output renders; the effect fires when `when` holds, one of `outputs` changed or the
          documents of one of `kinds` did. It reads `config` and
          `oldConfig`, the node's config after and before the delivery, each by apiVersion, kind
          and name with every Secret value replaced by its SHA-256, the node's name as
          `node.name`, and `facts.<name>` after the delivery and `oldFacts.<name>` before it.
        '';
      };
      restart = lib.mkOption {
        type = units;
        default = [ ];
        example = [ "bird.service" ];
        description = ''
          Units the effect restarts, by their full names. chalkd restarts the units of every due
          effect in one `systemctl try-restart`, those of an effect with `start` in one
          `systemctl restart`, and a unit that failed with `reset-failed` and `restart`.
        '';
      };
      reload = lib.mkOption {
        type = units;
        default = [ ];
        example = [ "systemd-networkd.service" ];
        description = "Units the effect reloads with `systemctl reload`, before chalkd restarts any. chalkd reloads only a unit that is active, and not one it restarts. A unit that failed it restarts with `reset-failed` and `restart`, as a failed unit of `restart`, since systemd reloads no failed unit.";
      };
      run = lib.mkOption {
        type = lib.types.nullOr lib.types.nonEmptyStr;
        default = null;
        example = "chronyc reload sources";
        description = "Command, with its arguments, the effect runs after the reloads and before the restarts. chalkd splits it on spaces, with no shell quoting, and runs the first word: a bare program name of chalkd's PATH, or an absolute path in the image's store. chalkd runs it whether or not any unit runs, and a command that fails fails the delivery, so one that talks to a service that may be stopped checks first that the service runs.";
      };
      start = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Start the units of `restart` that do not run, with `systemctl restart` instead of `try-restart`, as for a unit whose condition only the delivery meets.";
      };
      chalkd = lib.mkOption {
        type = lib.types.nullOr (lib.types.strMatching "[a-z][a-z0-9-]*:[a-z][a-z0-9-]*");
        default = null;
        example = "kubernetes:reload";
        description = ''
          An action of a layer on what only the running chalkd holds, as `<layer>:<action>`,
          such as `kubernetes:restart`, which starts chalkd's Kubernetes loops again, or
          `kubernetes:reload`, which starts the control plane's loop again. chalkd runs the
          actions of the due effects once each, after the restarts. The layer must be one of
          the image's.
        '';
      };
      hint = lib.mkOption {
        type = lib.types.nullOr (
          lib.types.submodule {
            options = {
              text = lib.mkOption {
                type = lib.types.nonEmptyStr;
                description = "What the delivery's error says after the units' failure.";
              };
              file = lib.mkOption {
                type = lib.types.nullOr lib.types.path;
                default = null;
                description = "A file whose text follows `text`; the hint is shown only while the file holds text.";
              };
            };
          }
        );
        default = null;
        description = ''
          Why the effect's units may fail to restart, for the delivery's error: when a unit of
          `restart` fails, chalkd fails the delivery as a failed precondition that names `text`
          and the content of `file`, rather than systemctl's error alone.
        '';
      };
    };
  };

  # An effect as chalkd reads it, once it has a trigger and an action.
  effectJSON =
    name: e:
    let
      where = "chalkos.effects.${name}";
      unknown = lib.filter (o: !(outputs ? ${o})) e.outputs;
      # The layer an effect's chalkd action names, or a run of chalkd <layer> <command> does.
      chalkdLayer = if e.chalkd == null then null else lib.head (lib.splitString ":" e.chalkd);
      # Split on whitespace as chalkd splits it, with strings.Fields.
      runWords =
        if e.run == null then
          [ ]
        else
          lib.filter (w: lib.isString w && w != "") (builtins.split "[[:space:]]+" e.run);
      runLayer =
        if
          lib.length runWords >= 2
          && baseNameOf (lib.head runWords) == "chalkd"
          # chalkd's common subcommands, which name no layer; they must match those of cmd/chalkd.
          && !(lib.elem (lib.elemAt runWords 1) [
            "serve"
            "load-config"
            "health"
          ])
        then
          lib.elemAt runWords 1
        else
          null;
      missing = lib.filter (l: l != null && !(lib.elem l layerNames)) [
        chalkdLayer
        runLayer
      ];
    in
    if unknown != [ ] then
      throw "${where}.outputs: the image has no output ${lib.head unknown}; its outputs are ${
        if outputs == { } then "none" else lib.concatStringsSep ", " (lib.attrNames outputs)
      }"
    else if missing != [ ] then
      throw "${where}: it names the ${lib.head missing} layer, which ${image} does not carry; its layers are ${
        if layerNames == [ ] then "none" else lib.concatStringsSep ", " layerNames
      }"
    else if e.outputs == [ ] && e.kinds == [ ] && e.when == null then
      throw "${where}: it has none of outputs, kinds and when, so nothing triggers it"
    else if e.restart == [ ] && e.reload == [ ] && e.run == null && e.chalkd == null then
      throw "${where}: it restarts, reloads and runs nothing"
    else
      {
        inherit (e)
          outputs
          restart
          reload
          start
          ;
        kinds = effectKinds "${where}.kinds" e.kinds;
        when = if e.when == null then "" else e.when;
        run = if e.run == null then "" else e.run;
        chalkd = if e.chalkd == null then "" else e.chalkd;
        hint =
          if e.hint == null then
            null
          else
            {
              inherit (e.hint) text;
              file = if e.hint.file == null then "" else toString e.hint.file;
            };
      };
  effectsJSON = builtins.toJSON (lib.mapAttrs effectJSON config.chalkos.effects);
in
{
  options.chalkos.effects = lib.mkOption {
    type = lib.types.attrsOf effect;
    default = { };
    description = ''
      What a delivery does to the node's services, by a name of the effect's own. Once a
      delivery is applied, chalkd takes every effect one of whose `outputs` changed, the
      documents of one of whose `kinds` did, or whose `when` holds: it reloads the units
      of their `reload`, runs their `run`, restarts the units of their `restart`, and runs the
      layer actions their `chalkd` names. An effect exists only when it is declared.
    '';
  };

  config = {
    system.build.chalkosEffects = pkgs.writeText "chalkos-effects.json" effectsJSON;
    environment.etc."chalkos/effects.json".text = effectsJSON;
  };
}
