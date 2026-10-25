# Outputs: sets of files a program renders from the node's documents, which chalkd checks before
# a delivery changes anything and swaps in once it is stored.
{
  chalkosPackages,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (chalkosPackages) chalkos-render;
  checkKinds = import ../../../lib/kinds/check.nix {
    inherit lib;
    inherit (config.chalkos) kinds;
  };

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
  # lib.chalkos.render.
  renderCommand =
    where: r:
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
      throw "${where}: a renderer is a command or one of lib.chalkos.render.template, .jq and .documents, not ${
        if lib.attrNames r == [ ] then
          "an empty attribute set"
        else
          "an attribute set of ${lib.concatStringsSep ", " (lib.attrNames r)}"
      }";

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
          type =
            lib.types.either (lib.types.coercedTo lib.types.package toString lib.types.nonEmptyStr) (
              lib.types.attrsOf lib.types.raw
            )
            // {
              description = "command, package or lib.chalkos.render value";
            };
          example = lib.literalExpression "lib.chalkos.render.template ./bird.conf.tmpl";
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
            `lib.chalkos.render.template`, `.jq` and `.documents` build the common renderers;
            `lib.chalkos.render.template` renders a Go template, in which a key a document lacks,
            or that holds null, prints as nothing.
          '';
        };
        check = lib.mkOption {
          type = lib.types.nullOr lib.types.lines;
          default = null;
          example = lib.literalExpression ''"''${pkgs.bird}/bin/bird -p -c $OUT/bird.conf"'';
          description = ''
            Shell commands, run by bash with `set -e` in the renderer's sandbox, with coreutils, grep
            and sed on the `PATH`, that check the rendered files in `$OUT`; they read the renderer's
            input on standard input and may not write `$OUT`. Unlike the renderer they may open
            netlink sockets, so `unshare --map-root-user --net nft --check --file "$OUT/…"` checks
            an nftables ruleset in a user and network namespace of its own. The files are as the
            renderer wrote them, before they get the output's owner and mode. A failure refuses
            the delivery before it changes anything, and at boot fails `chalkos-config.service`.
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
        render = renderCommand "chalkos.outputs.${name}.render" o.render;
        inherit (o)
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
    system.build.chalkosOutputExamples = pkgs.writeText "chalkos-output-examples.json" examplesJSON;
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
