# Validators: programs of the image's store that check a node's documents for what no schema or
# change rule sees, which chalkd runs before a delivery changes anything and refuses it with what
# they report.
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

  # The command of a validator's program: a command as it is, or the wrapper of a jq program of
  # lib.chalkos.validate.jq, which prints what the program makes of the documents. jq takes no
  # arguments, so the wrapper drops "validate <name>".
  programCommand =
    where: name: p:
    if lib.isString p then
      p
    else if lib.attrNames p == [ "jq" ] then
      toString (
        pkgs.writeShellScript "chalkos-validate-jq-${name}" ''
          exec ${lib.getExe pkgs.jq} -c -f ${p.jq}
        ''
      )
    else
      throw "${where}: a validator's program is a command or lib.chalkos.validate.jq, not ${
        if lib.attrNames p == [ ] then
          "an empty attribute set"
        else
          "an attribute set of ${lib.concatStringsSep ", " (lib.attrNames p)}"
      }";

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
            description = "command, package or lib.chalkos.validate value";
          };
        example = lib.literalExpression "lib.chalkos.validate.jq ./peers.jq";
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
          `lib.chalkos.validate.jq` builds a validator from a jq program, whose output is the
          report.
        '';
      };
      examples = lib.mkOption {
        type = lib.types.nullOr lib.types.path;
        default = null;
        example = lib.literalExpression "./examples";
        description = ''
          Directory of config files, YAML or JSON, each one example of a node's documents. The
          image's build runs the validator on each, and fails when it fails or refuses one.
        '';
      };
    };
  };

  validators = config.chalkos.validators;
  # A validator as chalkd reads it; its name is an argument of its program and names it in
  # refusals, so it is a plain name.
  validatorJSON =
    name: v:
    if builtins.match "[a-z0-9]([-a-z0-9]*[a-z0-9])?" name == null then
      throw "chalkos.validators.${name}: a validator's name is lower-case letters, digits and dashes"
    else
      {
        inputs = checkKinds "chalkos.validators.${name}.inputs" v.inputs v.inputs;
        program = programCommand "chalkos.validators.${name}.program" name v.program;
      };
  validatorsJSON = builtins.toJSON (lib.mapAttrs validatorJSON validators);
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
