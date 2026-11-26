# Outputs: sets of files a program renders from the node's documents, which chalkd checks before
# a delivery changes anything and swaps in once it is stored.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (pkgs.chalkos) chalkos-render;
  inherit (chalkosLib.internal)
    refuse
    kindProblems
    plainName
    attrsetText
    argv
    ;

  # The name of the file a renderer of path writes: the path's own, less suffix, and less the hash
  # a file the store holds by itself starts with.
  fileName =
    suffix: path:
    let
      name = baseNameOf (toString path);
      hashed = builtins.match "[0-9a-df-np-sv-z]{32}-(.+)" name;
    in
    lib.removeSuffix suffix (if hashed == null then name else lib.head hashed);
  render = lib.getExe chalkos-render;
  # The argv of an output's render: that of a command, or that of a renderer of chalkosLib.render;
  # null for anything else.
  renderArgv =
    r:
    if lib.isString r || lib.isList r then
      argv r
    else if lib.attrNames r == [ "template" ] then
      [
        render
        "template"
        "${r.template}"
        (fileName ".tmpl" r.template)
      ]
    else if lib.attrNames r == [ "jq" ] then
      [
        render
        "jq"
        (lib.getExe pkgs.jq)
        "${r.jq}"
        (fileName ".jq" r.jq)
      ]
    else if lib.attrNames r == [ "documents" ] then
      [
        render
        "documents"
      ]
      ++ lib.toList r.documents
    else
      null;
  # The argv of an output's check: [ ] for none, that of a command, or that of a check of
  # chalkosLib.check; null for anything else.
  checkArgv =
    c:
    if c == null then
      [ ]
    else if lib.isString c || lib.isList c then
      argv c
    else if !(c ? run && lib.isList c.run && c.run != [ ]) then
      null
    else if lib.attrNames c == [ "run" ] then
      [
        render
        "check"
        "run"
      ]
      ++ map toString c.run
    else if
      lib.attrNames c == [
        "each"
        "run"
      ]
      && lib.isString c.each
    then
      [
        render
        "check"
        "each"
        c.each
      ]
      ++ map toString c.run
    else
      null;

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
          type = chalkosLib.internal.programType "chalkosLib.render";
          example = lib.literalExpression "chalkosLib.render.template ./bird.conf.tmpl";
          description = ''
            Program of the image's store, with its arguments, that renders the output from the
            documents of `inputs` into `$OUT`. `chalkosLib.render.template`, `.jq` and
            `.documents` build the common renderers.
            [Program contract](extension-runtime.md#program-contract) gives its input, its
            report of refusals and its sandbox.
          '';
        };
        check = lib.mkOption {
          type = lib.types.nullOr (chalkosLib.internal.programType "chalkosLib.check");
          default = null;
          example = lib.literalExpression ''chalkosLib.check.run [ "''${pkgs.bird}/bin/bird" "-p" "-c" "$OUT/bird.conf" ]'';
          description = ''
            Program of the image's store, with its arguments, that chalkd runs after the renderer
            to check the rendered files in `$OUT`; null serves the files as the renderer wrote
            them. chalkd runs it without a shell, and an exit status other than 0 refuses the
            delivery before it changes anything, and at boot fails `chalkos-config.service`.
            `chalkosLib.check.run` and `.each` run a service's own check of its config on the
            files. [Check environment](extension-runtime.md#check-environment) gives what the
            program reads and may run.
          '';
        };
        dir = lib.mkOption {
          type = lib.types.str;
          default = "/run/chalkos/outputs/${name}";
          defaultText = lib.literalExpression ''"/run/chalkos/outputs/<name>"'';
          description = ''
            Where services read the output's files, a link to the last render. Refer to it as
            `config.chalkos.outputs.<name>.dir`, so a misspelled name fails the evaluation.
            [Output directories](extension-runtime.md#output-directories) gives the rules a dir
            follows.
          '';
        };
        replace = lib.mkOption {
          type = lib.types.bool;
          default = false;
          internal = true;
          description = ''
            Whether a swap replaces whatever `dir` holds, as the networkd output replaces what else
            writes to /run/systemd/network. Without it a swap replaces only nothing, an empty
            directory or a link to an earlier render, and fails the output otherwise.
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
  # Whether chalkd runs programs on the node's documents: renderers and checks, or validators,
  # which run in the same sandbox and keep their files below the outputs' root.
  runsPrograms = outputs != { } || config.chalkos.validators != { };
  # Where chalkd renders the outputs, outputs.DefaultRoot.
  outputsRoot = "/run/chalkos/outputs";
  # The directories of /run that systemd, NixOS and chalkd keep their state in, outputs.runOwned.
  runOwned = [
    "systemd"
    "credentials"
    "chalkd"
    "wrappers"
    "current-system"
    "booted-system"
    "user"
    "dbus"
    "udev"
    "keys"
    "lock"
    "log"
  ];
  # Why chalkd may not replace dir, the dir of the output name, with a link, or null. As in
  # outputs.checkDir, dir must:
  # - be a clean path below /run, a tmpfs;
  # - have no component that starts with a dot, as the links and directories chalkd keeps beside
  #   the outputs' do;
  # - stay out of the directories of /run that systemd, NixOS and chalkd keep their state in,
  #   runOwned, except for networkd's /run/systemd/network;
  # - stay out of /run/chalkos, which holds the node's config and credentials, except for
  #   /run/chalkos/outputs/<name> and the paths below it.
  dirProblem =
    name: dir:
    let
      own = "/run/chalkos/outputs/${name}";
      top = lib.head (lib.splitString "/" (lib.removePrefix "/run/" dir));
    in
    if builtins.match "/run(/[^/.][^/]*)+" dir == null then
      "must be a clean path below /run with no component that starts with a dot"
    else if lib.elem top runOwned && dir != "/run/systemd/network" then
      "takes /run/${top} or a path below it, which systemd, NixOS or chalkd own"
    else if
      (dir == "/run/chalkos" || lib.hasPrefix "/run/chalkos/" dir)
      && !(dir == own || lib.hasPrefix "${own}/" dir)
    then
      "is in /run/chalkos, where an output may take only ${own}"
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
  # Why the kinds a renderer of chalkosLib.render.documents copies do not suit the output: chalkd
  # hands a renderer the documents of the output's inputs alone, so each kind is one of them.
  documentsProblems =
    where: o:
    let
      kinds = o.render.documents;
      missing = lib.subtractLists o.inputs kinds;
    in
    lib.optionals (lib.isAttrs o.render && lib.attrNames o.render == [ "documents" ]) (
      lib.optional (
        !(lib.isList kinds) || kinds == [ ]
      ) "${where}.render: chalkosLib.render.documents takes a non-empty list of kinds"
      ++
        lib.optional (lib.isList kinds && missing != [ ])
          "${where}.render: chalkosLib.render.documents names ${lib.concatStringsSep ", " missing}, which ${where}.inputs does not hold; the renderer reads the documents of the output's inputs alone"
    );
  # What is wrong with an output; its name names its directories on the node, so it is a plain
  # name.
  outputProblems =
    name: o:
    let
      where = "chalkos.outputs.${name}";
    in
    lib.optional (
      builtins.match plainName name == null
    ) "${where}: an output's name is lower-case letters, digits and dashes"
    ++ lib.optional (dirProblem name o.dir != null) "${where}.dir: ${o.dir} ${dirProblem name o.dir}"
    ++ kindProblems config.chalkos.kinds "${where}.inputs" o.inputs
    ++
      lib.optional (renderArgv o.render == null)
        "${where}.render: a renderer is a command, a list of a program and its arguments or one of chalkosLib.render.template, .jq and .documents, not ${attrsetText o.render}"
    ++ documentsProblems where o
    ++
      lib.optional (checkArgv o.check == null)
        "${where}.check: a check is a command, a list of a program and its arguments or one of chalkosLib.check.run and .each, each with a non-empty list of a program and its arguments, not ${attrsetText o.check}";
  # An output as chalkd reads it.
  outputJSON = _: o: {
    inherit (o)
      inputs
      dir
      replace
      user
      group
      mode
      ;
    render = renderArgv o.render;
    check = checkArgv o.check;
  };
  outputsJSON = refuse (clashes ++ lib.concatLists (lib.mapAttrsToList outputProblems outputs)) (
    builtins.toJSON (lib.mapAttrs outputJSON outputs)
  );

in
{
  options.chalkos.outputs = lib.mkOption {
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

  config = {
    system.build.chalkosOutputs = pkgs.writeText "chalkos-outputs.json" outputsJSON;
    environment.etc."chalkos/outputs.json".text = outputsJSON;

    # The unprivileged user chalkd runs renderers, checks and validators as. Users are fixed when
    # the image is built, so it exists when chalkos-config renders at boot.
    users = lib.mkIf runsPrograms {
      users.chalkos-render = {
        isSystemUser = true;
        group = "chalkos-render";
        description = "chalkos output renderers and validators";
      };
      groups.chalkos-render = { };
    };
    # The outputs' files, those the node serves and those a render stages, and the validators'
    # empty directories, on a tmpfs of their own, so a renderer that writes without end fills it
    # and not /run. An output's files may take 16 MiB, chalkd's MaxSize, and each output may have
    # both, with room for one more: the standard output of the program that runs, a renderer's
    # or a validator's, which LimitFSIZE bounds to MaxSize as well.
    fileSystems = lib.mkIf runsPrograms {
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
            "noexec"
            "size=${toString (room * 16)}M"
            "nr_inodes=${toString (room * 16384)}"
          ];
      };
    };
  };
}
