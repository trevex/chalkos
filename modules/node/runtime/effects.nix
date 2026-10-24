# Effects: what a delivery does to the node's services once it is applied. An effect restarts or
# reloads units, runs a command or acts on chalkd's own loops when one of its outputs changed,
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
        description = "Units the effect reloads with `systemctl reload`, before chalkd restarts any. chalkd reloads only a unit that is active, and not one it restarts.";
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
        type = lib.types.nullOr (
          lib.types.enum [
            "restart"
            "reload"
          ]
        );
        default = null;
        description = ''
          What the effect does to chalkd's control-plane loops, which run inside chalkd and have
          no unit of their own: `reload` starts the control plane's loop again, which reads the
          share and applies the control plane's objects, and `restart` every Kubernetes loop,
          which read the cluster when they start.
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
    in
    if unknown != [ ] then
      throw "${where}.outputs: the image has no output ${lib.head unknown}; its outputs are ${
        if outputs == { } then "none" else lib.concatStringsSep ", " (lib.attrNames outputs)
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
      of their `reload`, runs their `run`, restarts the units of their `restart`, and acts on
      its own loops as their `chalkd` says. An effect exists only when it is declared.
    '';
  };

  config = {
    system.build.chalkosEffects = pkgs.writeText "chalkos-effects.json" effectsJSON;
    environment.etc."chalkos/effects.json".text = effectsJSON;
  };
}
