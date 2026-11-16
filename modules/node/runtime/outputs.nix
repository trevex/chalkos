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
  # The command of an output's render: a command as it is, or that of a renderer of
  # chalkosLib.render; null for anything else.
  renderCommand =
    r:
    if lib.isString r then
      r
    else if lib.attrNames r == [ "template" ] then
      "${lib.getExe chalkos-render} template ${r.template} ${fileName ".tmpl" r.template}"
    else if lib.attrNames r == [ "jq" ] then
      toString (
        pkgs.writeShellScript "chalkos-render-jq-${fileName ".jq" r.jq}" ''
          exec ${lib.getExe pkgs.jq} -r -f ${r.jq} > "$OUT/${fileName ".jq" r.jq}"
        ''
      )
    else if lib.attrNames r == [ "documents" ] then
      "${lib.getExe chalkos-render} documents ${lib.concatStringsSep " " r.documents}"
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
          type = lib.types.nullOr lib.types.lines;
          default = null;
          example = lib.literalExpression ''"''${pkgs.bird}/bin/bird -p -c $OUT/bird.conf"'';
          description = ''
            Shell commands, run by bash with `set -e` after the renderer, that check the rendered
            files in `$OUT`; null serves the files as the renderer wrote them. A failure refuses the
            delivery before it changes anything, and at boot fails `chalkos-config.service`.
            [Check environment](extension-runtime.md#check-environment) gives what the commands
            read and may run.
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
  # Why chalkd may not replace dir, the dir of the output name, with a link, or null. As in
  # outputs.checkDir, dir must:
  # - be a clean path below /run, a tmpfs;
  # - have no component that starts with a dot, as the links and directories chalkd keeps beside
  #   the outputs' do;
  # - stay out of /run/chalkos, which holds the node's config and credentials, except for
  #   /run/chalkos/outputs/<name> and the paths below it.
  dirProblem =
    name: dir:
    let
      own = "/run/chalkos/outputs/${name}";
    in
    if builtins.match "/run(/[^/.][^/]*)+" dir == null then
      "must be a clean path below /run with no component that starts with a dot"
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
      lib.optional (renderCommand o.render == null)
        "${where}.render: a renderer is a command or one of chalkosLib.render.template, .jq and .documents, not ${attrsetText o.render}";
  # An output as chalkd reads it.
  outputJSON = name: o: {
    inherit (o)
      inputs
      dir
      user
      group
      mode
      ;
    render = renderCommand o.render;
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
            "size=${toString (room * 16)}M"
            "nr_inodes=${toString (room * 16384)}"
          ];
      };
    };
  };
}
