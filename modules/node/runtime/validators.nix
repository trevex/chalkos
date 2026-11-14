# Validators: programs of the image's store that check a node's documents for what no schema or
# change rule sees, which chalkd runs before a delivery changes anything and refuses it with what
# they report.
{
  chalkosLib,
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (chalkosLib.internal)
    refuse
    kindProblems
    plainName
    attrsetText
    ;

  # The command of a validator's program: a command as it is, or the wrapper of a jq program of
  # chalkosLib.validate.jq, which prints what the program makes of the documents. jq takes no
  # arguments, so the wrapper drops "validate <name>". null for anything else.
  programCommand =
    name: p:
    if lib.isString p then
      p
    else if lib.attrNames p == [ "jq" ] then
      toString (
        pkgs.writeShellScript "chalkos-validate-jq-${name}" ''
          exec ${lib.getExe pkgs.jq} -c -f ${p.jq}
        ''
      )
    else
      null;

  validator = {
    options = {
      inputs = lib.mkOption {
        type = lib.types.nonEmptyListOf lib.types.str;
        example = [ "Peer" ];
        description = "Config kinds, by name as for a grant, whose documents the validator reads.";
      };
      program = lib.mkOption {
        type =
          lib.types.either (lib.types.coercedTo lib.types.package toString lib.types.nonEmptyStr) (
            lib.types.attrsOf lib.types.raw
          )
          // {
            description = "command, package or chalkosLib.validate value";
          };
        example = lib.literalExpression "chalkosLib.validate.jq ./peers.jq";
        description = ''
          Program of the image's store, with its arguments, that checks the documents. chalkd
          runs it as `<program> validate <name>`. It reads what a renderer reads on standard
          input: `{"node": {"name": …}, "documents": {"<Kind>": [<document>, …]}}`, the documents
          of each kind of `inputs`, sorted by name, with their schema defaults and without any
          Secret's values. It refuses a config by printing
          `{"refusals": [{"kind": …, "name": …, "path": …, "message": …}]}` on standard output,
          whatever its exit status, which chalkd reports as it reports a rule's refusal; it prints
          nothing, or no refusal, for a config it accepts. Otherwise exit status 0 means it ran;
          any other status, or anything else printed on standard output, fails it, which refuses
          the config too. It runs in the renderers' sandbox, may write no path but `/tmp`, and
          must finish within 30 seconds.
          `chalkosLib.validate.jq` builds a validator from a jq program, whose output is the
          report.
        '';
      };
    };
  };

  validators = config.chalkos.validators;
  # What is wrong with a validator; its name is an argument of its program and names it in
  # refusals, so it is a plain name.
  validatorProblems =
    name: v:
    let
      where = "chalkos.validators.${name}";
    in
    lib.optional (
      builtins.match plainName name == null
    ) "${where}: a validator's name is lower-case letters, digits and dashes"
    ++ kindProblems config.chalkos.kinds "${where}.inputs" v.inputs
    ++
      lib.optional (programCommand name v.program == null)
        "${where}.program: a validator's program is a command or chalkosLib.validate.jq, not ${attrsetText v.program}";
  # A validator as chalkd reads it.
  validatorJSON = name: v: {
    inherit (v) inputs;
    program = programCommand name v.program;
  };
  validatorsJSON = refuse (lib.concatLists (lib.mapAttrsToList validatorProblems validators)) (
    builtins.toJSON (lib.mapAttrs validatorJSON validators)
  );
in
{
  options.chalkos.validators = lib.mkOption {
    type = lib.types.attrsOf (lib.types.submodule validator);
    default = { };
    description = ''
      Programs that check a node's documents for what the kinds' schemas and change rules do not
      see, such as what several documents mean together, by a name of the validator's own. chalkd
      runs every validator before a delivery changes anything, after the change rules and before
      the outputs render, and refuses the delivery when one refuses it or fails. The image checks
      its own config with them too, as an upgrade does before it stages the image.
      `chalkctl validate` runs only the validators chalkctl links, the router layer's `router`;
      the others are programs of the image, which the node runs.
    '';
  };

  config = {
    system.build.chalkosValidators = pkgs.writeText "chalkos-validators.json" validatorsJSON;
    environment.etc."chalkos/validators.json".text = validatorsJSON;
  };
}
