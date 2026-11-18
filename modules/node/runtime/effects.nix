# Effects: what a delivery does to the node's services once it is applied. An effect restarts or
# reloads units, runs a command or a layer's action in chalkd when one of its outputs changed,
# the documents of one of its kinds did, or its CEL rule holds.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (chalkosLib.internal) refuse kindProblems;
  outputs = config.chalkos.outputs;
  layerNames = lib.attrNames config.chalkos.layers;
  image = chalkosLib.internal.imageText config.chalkos.image.name;

  # What is wrong with the kinds of an effect, each prefixed with `where`: a kind the image does
  # not declare, Secret or a withheld kind. A Secret's values count for the kinds whose secretRef
  # fields name them, and a withheld kind's documents stay in the node's stored config.
  effectKindProblems =
    where: kinds:
    let
      withheld = lib.filter (k: config.chalkos.kinds.${k}.withheld or false) kinds;
    in
    lib.optional (lib.elem "Secret" kinds) "${where}: an effect does not fire on Secret documents; it fires on the kinds whose secretRef fields name them"
    ++ map (
      k:
      "${where}: an effect does not fire on ${k} documents; the kind is withheld, and a when rule reads them"
    ) withheld
    ++ kindProblems config.chalkos.kinds where (lib.subtractLists ([ "Secret" ] ++ withheld) kinds);

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
          documents of one of `kinds` did.
          [Effect conditions](extension-runtime.md#effect-condition-variables) gives the
          variables it reads.
        '';
      };
      after = lib.mkOption {
        type = lib.types.listOf lib.types.nonEmptyStr;
        default = [ ];
        example = [ "networkd" ];
        description = ''
          Effects of this image that must succeed before this effect runs when they are due
          in the same delivery. A prerequisite that has no changes is left alone. A failed
          prerequisite blocks the effects that depend on it.
        '';
      };
      waitFor = lib.mkOption {
        type = lib.types.nullOr (
          lib.types.submodule {
            options = {
              description = lib.mkOption {
                type = lib.types.nonEmptyStr;
                description = "The runtime condition this effect needs, shown in plans and errors.";
              };
              argv = lib.mkOption {
                type = lib.types.nonEmptyListOf lib.types.nonEmptyStr;
                description = ''
                  Executable from the image's store and its arguments. This read-only probe runs
                  on the node after the due prerequisites succeed. Exit 0 when ready, 75 to
                  retry, or another nonzero status to fail the effect. An attempt that runs longer
                  than 2 seconds is stopped and counts as a retry.
                '';
              };
              timeoutSeconds = lib.mkOption {
                type = lib.types.ints.between 1 60;
                default = 15;
                description = "Maximum time to wait for the condition, in seconds.";
              };
            };
          }
        );
        default = null;
        description = "An optional readiness probe run before this effect, even when its prerequisites are unchanged.";
      };
      restart = lib.mkOption {
        type = units;
        default = [ ];
        example = [ "bird.service" ];
        description = ''
          Units the effect restarts, by their full names; a unit that does not run stays stopped
          unless the effect sets `start`.
          [The config pipeline](../how-it-works/node/config-pipeline.md#chalkd-takes-the-due-effects-in-one-order)
          gives the order of the actions of every due effect.
        '';
      };
      reload = lib.mkOption {
        type = units;
        default = [ ];
        example = [ "systemd-networkd.service" ];
        description = ''
          Units to `systemctl reload`, by their full names, before the restarts in the same batch. A failed unit is
          restarted instead, as systemd reloads no failed unit.
        '';
      };
      run = lib.mkOption {
        type = lib.types.nullOr lib.types.nonEmptyStr;
        default = null;
        example = "chronyc reload sources";
        description = "Command, with its arguments, the effect runs after the reloads and before the restarts in its batch. chalkd splits it on spaces, with no shell quoting, and runs the first word: a bare program name of chalkd's PATH, or an absolute path in the image's store. chalkd runs it whether or not any unit runs, and a command that fails fails the delivery, so one that talks to a service that may be stopped checks first that the service runs.";
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
          actions of the due effects once each, after their batch's restarts. The layer must be one of
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

  effects = config.chalkos.effects;
  effectOrder = lib.toposort (a: b: lib.elem a effects.${b}.after) (lib.attrNames effects);
  # What is wrong with an effect: missing references, invalid prerequisites, no trigger or no action.
  effectProblems =
    name: e:
    let
      where = "chalkos.effects.${name}";
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
            "check"
            "help"
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
    map (
      o:
      "${where}.outputs: the image has no output ${o}; its outputs are ${
        if outputs == { } then "none" else lib.concatStringsSep ", " (lib.attrNames outputs)
      }"
    ) (lib.filter (o: !(outputs ? ${o})) e.outputs)
    ++ map (
      l:
      "${where}: it names the ${l} layer, which ${image} does not carry; its layers are ${
        if layerNames == [ ] then "none" else lib.concatStringsSep ", " layerNames
      }"
    ) missing
    ++ lib.optional (
      e.outputs == [ ] && e.kinds == [ ] && e.when == null
    ) "${where}: it has none of outputs, kinds and when, so nothing triggers it"
    ++ lib.optional (
      e.restart == [ ] && e.reload == [ ] && e.run == null && e.chalkd == null
    ) "${where}: it restarts, reloads and runs nothing"
    ++ map (dep: "${where}.after: the image has no effect ${dep}") (
      lib.filter (dep: !(effects ? ${dep})) e.after
    )
    ++ lib.optional (lib.elem name e.after) "${where}.after: an effect cannot depend on itself"
    ++ lib.optional (
      lib.length e.after != lib.length (lib.unique e.after)
    ) "${where}.after: each prerequisite must be unique"
    ++ lib.optional (
      e.waitFor != null && !(lib.hasPrefix "/nix/store/" (lib.head e.waitFor.argv))
    ) "${where}.waitFor.argv: the executable must be in the image's store"
    ++ effectKindProblems "${where}.kinds" e.kinds;
  # An effect as chalkd reads it.
  effectJSON =
    _: e:
    {
      inherit (e)
        outputs
        restart
        reload
        start
        kinds
        ;
      when = lib.defaultTo "" e.when;
      run = lib.defaultTo "" e.run;
      chalkd = lib.defaultTo "" e.chalkd;
      hint =
        if e.hint == null then
          null
        else
          {
            inherit (e.hint) text;
            file = toString e.hint.file;
          };
    }
    // lib.optionalAttrs (e.after != [ ]) { inherit (e) after; }
    // lib.optionalAttrs (e.waitFor != null) { inherit (e) waitFor; };
  effectsJSON = refuse (
    lib.concatLists (lib.mapAttrsToList effectProblems effects)
    ++ lib.optional (
      effectOrder ? cycle
    ) "chalkos.effects: prerequisite cycle among ${lib.concatStringsSep ", " effectOrder.cycle}"
  ) (builtins.toJSON (lib.mapAttrs effectJSON effects));
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
