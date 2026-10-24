# lib.chalkos, which every image's NixOS modules get as part of lib: the types of extension kinds'
# fields, the renderers of chalkos.outputs and the common effects. It holds no store paths, since
# lib does not depend on pkgs: a renderer is a value that chalkos.outputs.<name>.render turns into
# its program's command.
{ lib }:
let
  kindTypes = import ./kinds/types.nix { inherit lib; };
  # An effect that acts on units when one of outputs or the documents of one of kinds changed.
  effectOn =
    action:
    {
      outputs ? [ ],
      kinds ? [ ],
    }:
    units: {
      inherit outputs kinds;
      ${action} = units;
    };
in
{
  types = { inherit (kindTypes) secretRef; };

  # lib.mkOption, for a field of a kind, with the field's change rules: each { rule; message or
  # messageExpression; takes ? "refuse"; description ? null; }, as a kind's rules are.
  mkOption =
    {
      rules ? [ ],
      ...
    }@args:
    lib.mkOption (removeAttrs args [ "rules" ])
    // lib.optionalAttrs (rules != [ ]) { "x-chalkos-rules" = rules; };

  render = {
    # A Go text/template, which writes the file named as the template less .tmpl; a missing or
    # null key prints as nothing.
    template = path: { template = path; };
    # A jq program, whose output, raw for a string, writes the file named as the program less .jq.
    jq = path: { jq = path; };
    # The documents of kinds as they are, each as <Kind>/<name>.json.
    documents = kinds: { documents = kinds; };
  };

  restartOn = effectOn "restart";
  reloadOn = effectOn "reload";
}
