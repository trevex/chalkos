# Runtime access to config and what a delivery does to services. A unit is granted the config
# kinds it reads with systemd.services.<unit>.chalkos.kinds: chalkd writes the node's documents of
# those kinds below /run/chalkos/config and the values their secretRef fields name into the unit's
# credentials, and chalkcfg is on its PATH. An output of chalkos.outputs is a set of files a
# program renders from documents, which chalkd checks before a delivery changes anything. A grant
# never restarts the unit; an effect of chalkos.effects does, once a delivery is applied, when one
# of its outputs changed, the documents of one of its kinds did, or its CEL rule holds. chalkcfg
# prints the documents, on the system path of every image.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  chalkcfg = pkgs.callPackage ../../../nix/chalkcfg.nix { };

  # Every kind of chalkos.kinds, and networkd's, whose names chalkos.kinds may not take.
  declaredKinds = lib.attrNames config.chalkos.kinds ++ [
    "Link"
    "NetDev"
    "Network"
  ];
  # value once every kind of kinds is one a unit may read, which where names: a unit must not
  # start without the documents it expects, so a misspelled kind fails the evaluation.
  checkKinds =
    where: kinds: value:
    let
      unknown = lib.filter (k: !lib.elem k declaredKinds) kinds;
    in
    if unknown != [ ] then
      throw "${where}: the image declares no kind ${lib.head unknown}; it declares ${lib.concatStringsSep ", " (lib.sort lib.lessThan declaredKinds)}"
    else if lib.elem "Secret" kinds then
      throw "${where}: a unit does not read Secret documents; it gets the values the secretRef fields of its kinds name"
    else if lib.elem "Addon" kinds then
      throw "${where}: a unit does not read Addon documents; they stay in the node's stored config, and chalkd applies their objects on control planes"
    else
      value;

  # kinds once an effect may fire on each: Secret values count for the kinds whose secretRef
  # fields name them, and Addon documents stay in the node's stored config.
  effectKinds =
    where: kinds:
    if lib.elem "Secret" kinds then
      throw "${where}: an effect does not fire on Secret documents; it fires on the kinds whose secretRef fields name them"
    else if lib.elem "Addon" kinds then
      throw "${where}: an effect does not fire on Addon documents; chalkd applies them, and a when rule reads them"
    else
      checkKinds where kinds kinds;

  grant =
    { name, config, ... }:
    {
      options.chalkos.kinds = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        example = [ "Peer" ];
        description = ''
          Config kinds the unit reads, by name: attributes of `chalkos.kinds`, or networkd's
          `Network`, `NetDev` and `Link`. The documents of a kind of `chalkos.kinds` are in its
          `dir`, networkd's below `/run/chalkos/config/networkd.chalkos.dev`, and `chalkcfg` is
          on the unit's `PATH`. The values their `secretRef` fields name are the unit's
          credentials `secret_<secret>.<key>`. A grant never restarts the unit: an effect of
          `chalkos.effects` does.
        '';
      };
      # systemd loads every file of the directory as the credential secret_<file>; chalkd keeps
      # the directory, empty or not, for every granted unit.
      config = lib.mkIf (config.chalkos.kinds != [ ]) {
        serviceConfig.LoadCredential =
          checkKinds "systemd.services.${name}.chalkos.kinds" config.chalkos.kinds
            [ "secret:/run/chalkos/credentials/${name}" ];
        path = [ chalkcfg ];
      };
    };

  granted = lib.filterAttrs (_: s: s.chalkos.kinds != [ ]) config.systemd.services;

  # A grant on a name the image defines no service of creates an empty unit, as for a typo. A
  # warning, not an assertion: units from systemd.packages have no ExecStart in the config.
  orphan =
    name: s:
    lib.optional (!(s.serviceConfig ? ExecStart || s.script != "")) ''
      systemd.services.${name}.chalkos.kinds may grant kinds to no service: systemd.services.${name}
      defines neither serviceConfig.ExecStart nor script. Ignore this if the unit comes from
      systemd.packages.
    '';

  # What chalkd reads of each granted unit.
  servicesJSON = builtins.toJSON (
    lib.mapAttrs (name: s: {
      kinds = checkKinds "systemd.services.${name}.chalkos.kinds" s.chalkos.kinds s.chalkos.kinds;
    }) granted
  );

  output =
    { name, ... }:
    {
      options = {
        inputs = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
          example = [ "Peer" ];
          description = "Config kinds, by name as for a grant, whose documents the renderer reads.";
        };
        render = lib.mkOption {
          type = lib.types.coercedTo lib.types.package toString lib.types.nonEmptyStr;
          example = lib.literalExpression "chalkosLib.render.template ./bird.conf.tmpl";
          description = ''
            Program of the image's store, with its arguments, that renders the output. It reads
            `{"node": {"name": …}, "documents": {"<Kind>": [<document>, …]}}` on standard input,
            the documents of each kind of `inputs` sorted by name, with their schema defaults and
            without any Secret's values, and writes the output's files into `$OUT`, the only path
            it may write. It runs as the unprivileged user `chalkos-render`, without capabilities,
            devices or network, on a read-only system without STATE, `/var`, or the config,
            credentials and Kubernetes keys chalkd writes below `/run/chalkos`, and must finish
            within 30 seconds; the files may take 16 MiB, and hold no links. The same documents
            must give the same files: chalkd renders every output again at boot.
            `chalkosLib.render.template` renders a Go template, in which a key a document lacks, or
            that holds null, prints as nothing.
          '';
        };
        check = lib.mkOption {
          type = lib.types.nullOr lib.types.lines;
          default = null;
          example = lib.literalExpression ''"''${pkgs.bird}/bin/bird -p -c $OUT/bird.conf"'';
          description = ''
            Shell commands, run by bash with `set -e` in the renderer's sandbox, with coreutils, grep
            and sed on the `PATH`, that check the rendered files in `$OUT`; they read the renderer's
            input on standard input and may not write `$OUT`. The files are as the renderer wrote
            them, before they get the output's owner and mode. A failure refuses the delivery
            before it changes anything, and at boot fails `chalkos-config.service`.
          '';
        };
        examples = lib.mkOption {
          type = lib.types.nullOr lib.types.path;
          default = null;
          example = lib.literalExpression "./examples";
          description = ''
            Directory of config files, YAML or JSON, each one example of a node's documents. The
            image's build renders and checks the output from each, as the node `example`, and fails
            when one fails.
          '';
        };
        dir = lib.mkOption {
          type = lib.types.str;
          default = "/run/chalkos/outputs/${name}";
          defaultText = lib.literalExpression ''"/run/chalkos/outputs/<name>"'';
          description = ''
            Where services read the output's files: a link that chalkd replaces in one rename, so
            a service reads the files of one render, never a mix of two. Refer to it as
            `config.chalkos.outputs.<name>.dir`, so a misspelled name fails the evaluation. A
            directory at the path is removed when the link first replaces it, so the path is a
            clean path below `/run` whose components start with no dot, outside `/run/chalkos`
            but for `/run/chalkos/outputs/<name>` and the paths below it, and neither another
            output's dir nor inside one.
          '';
        };
        user = lib.mkOption {
          type = lib.types.str;
          default = "root";
          description = "Owner of the output's files and directories.";
        };
        group = lib.mkOption {
          type = lib.types.str;
          default = "root";
          description = "Group of the output's files and directories.";
        };
        mode = lib.mkOption {
          type = lib.types.strMatching "0?[0-7]{3}";
          default = "0644";
          description = "Mode of the output's files; its directories are 0755.";
        };
      };
    };

  outputs = config.chalkos.outputs;
  # Where chalkd renders the outputs, outputs.DefaultRoot.
  outputsRoot = "/run/chalkos/outputs";
  # Why chalkd may not replace dir, the dir of the output name, with a link, or null: dir must be a
  # clean path below /run, a tmpfs, whose components start with no dot, as the links and
  # directories chalkd keeps beside the outputs' do, and stay out of /run/chalkos, which holds the
  # node's config and credentials, but for /run/chalkos/outputs/<name> and the paths below it.
  dirProblem =
    name: dir:
    let
      own = "/run/chalkos/outputs/${name}";
    in
    if builtins.match "/run(/[^/.][^/]*)+" dir == null then
      "is no clean path below /run whose components start with no dot"
    else if
      (dir == "/run/chalkos" || lib.hasPrefix "/run/chalkos/" dir)
      && !(dir == own || lib.hasPrefix "${own}/" dir)
    then
      "is in /run/chalkos, of which an output takes ${own} alone"
    else
      null;
  # An output's dir that is another's, or inside another's, whose link a swap of the other would
  # replace or leave dangling.
  clashes = lib.concatLists (
    lib.mapAttrsToList (
      name: o:
      map (problem: "chalkos.outputs.${name}.dir: ${o.dir} ${problem}") (
        lib.concatLists (
          lib.mapAttrsToList (
            other: p:
            lib.optional (other < name && o.dir == p.dir) "is the dir of the output ${other}"
            ++ lib.optional (lib.hasPrefix "${p.dir}/" o.dir) "is inside the dir of the output ${other}"
          ) outputs
        )
      )
    ) outputs
  );
  # An output as chalkd reads it; its name names its directories on the node, so it is a plain
  # name.
  outputJSON =
    name: o:
    if builtins.match "[a-z0-9]([-a-z0-9]*[a-z0-9])?" name == null then
      throw "chalkos.outputs.${name}: an output's name is lower-case letters, digits and dashes"
    else if dirProblem name o.dir != null then
      throw "chalkos.outputs.${name}.dir: ${o.dir} ${dirProblem name o.dir}"
    else
      {
        inputs = checkKinds "chalkos.outputs.${name}.inputs" o.inputs o.inputs;
        inherit (o)
          render
          dir
          user
          group
          mode
          ;
        check =
          if o.check == null then
            ""
          else
            toString (
              pkgs.writeShellScript "chalkos-output-${name}-check" ''
                set -e
                PATH=${
                  lib.makeBinPath [
                    pkgs.coreutils
                    pkgs.gnugrep
                    pkgs.gnused
                  ]
                }
                ${o.check}
              ''
            );
      };
  outputsJSON =
    if clashes != [ ] then
      throw (lib.head clashes)
    else
      builtins.toJSON (lib.mapAttrs outputJSON outputs);
  # The examples of each output that has them, which the image's build renders.
  examplesJSON = builtins.toJSON (
    lib.mapAttrs (_: o: o.examples) (lib.filterAttrs (_: o: o.examples != null) outputs)
  );

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
  options.systemd.services = lib.mkOption {
    type = lib.types.attrsOf (lib.types.submodule grant);
  };

  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.outputs = lib.mkOption {
        type = lib.types.attrsOf (lib.types.submodule output);
        default = { };
        description = ''
          Config files of the node's services, by a name of the output's own, each rendered from
          documents by a program of the image's store. chalkd renders and checks every output
          before a delivery changes anything, and refuses the delivery when one fails; once the
          delivery is stored it swaps the files in, and the effects of `chalkos.effects` that name
          a changed output fire. `chalkos-config.service` renders them at boot, before services
          start, and fails when one fails.
        '';
      };
      options.effects = lib.mkOption {
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
    };
  };

  config = {
    warnings = lib.concatLists (lib.mapAttrsToList orphan granted);

    # The installer is no variant and never has config to query.
    environment.systemPackages = lib.mkIf (config.chalkos.variant.name != null) [ chalkcfg ];

    system.build.chalkosServices = pkgs.writeText "chalkos-services.json" servicesJSON;
    system.build.chalkosEffects = pkgs.writeText "chalkos-effects.json" effectsJSON;
    system.build.chalkosOutputs = pkgs.writeText "chalkos-outputs.json" outputsJSON;
    system.build.chalkosOutputExamples = pkgs.writeText "chalkos-output-examples.json" examplesJSON;
    environment.etc."chalkos/services.json".text = servicesJSON;
    environment.etc."chalkos/effects.json".text = effectsJSON;
    environment.etc."chalkos/outputs.json".text = outputsJSON;

    # The unprivileged user chalkd runs renderers and checks as. Users are fixed when the image is
    # built, so it exists when chalkos-config renders at boot.
    users = lib.mkIf (outputs != { }) {
      users.chalkos-render = {
        isSystemUser = true;
        group = "chalkos-render";
        description = "chalkos output renderers";
      };
      groups.chalkos-render = { };
    };
    # The outputs' files, those the node serves and those a render stages, on a tmpfs of their
    # own, so a renderer that writes without end fills it and not /run. An output's files may
    # take 16 MiB, chalkd's MaxSize, and each output may have both, with room for one more.
    fileSystems = lib.mkIf (outputs != { }) {
      ${outputsRoot} = {
        device = "tmpfs";
        fsType = "tmpfs";
        options =
          let
            room = 2 * lib.length (lib.attrNames outputs) + 1;
          in
          [
            "mode=0755"
            "nosuid"
            "nodev"
            "size=${toString (room * 16)}M"
            "nr_inodes=${toString (room * 16384)}"
          ];
      };
    };
  };
}
