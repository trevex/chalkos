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
    argv
    ;

  # The argv of a validator's program: that of a command, or chalkos-render's run of a jq program
  # of chalkosLib.validate.jq, which prints what the program makes of the documents and takes the
  # "validate <name>" chalkd adds. null for anything else.
  programArgv =
    p:
    if lib.isString p || lib.isList p then
      argv p
    else if lib.attrNames p == [ "jq" ] then
      [
        (lib.getExe pkgs.chalkos.chalkos-render)
        "validate-jq"
        (lib.getExe pkgs.jq)
        "${p.jq}"
      ]
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
        type = chalkosLib.internal.programType "chalkosLib.validate";
        example = lib.literalExpression "chalkosLib.validate.jq ./peers.jq";
        description = ''
          Program of the image's store, with its arguments, that checks the documents of
          `inputs`; chalkd runs it as `<program> validate <name>`. It refuses a config with a
          report of refusals on standard output.
          [Program contract](extension-runtime.md#program-contract) gives its input, the report
          and its sandbox.
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
      lib.optional (programArgv v.program == null)
        "${where}.program: a validator's program is a command, a list of a program and its arguments or chalkosLib.validate.jq, not ${attrsetText v.program}";
  # A validator as chalkd reads it.
  validatorJSON = _: v: {
    inherit (v) inputs;
    program = programArgv v.program;
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
