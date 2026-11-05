# lib.chalkos, which every image's NixOS modules get as part of lib: the types of extension kinds'
# fields, the renderers of chalkos.outputs, the validators of chalkos.validators and the common
# effects. It holds no store paths, since lib does not depend on pkgs: a renderer is a value that
# chalkos.outputs.<name>.render turns into its program's command, as a validator is for
# chalkos.validators.<name>.program.
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
  types = { inherit (kindTypes) ref secretRef; };

  # lib.mkOption, for a field of a kind, with the field's change rules: each { rule; message or
  # messageExpression; takes ? "refuse"; description ? null; }, as a kind's rules are; and the
  # values that must not repeat: true, or one or a list of { across ? "kind" or "list"; by ? [
  # field or { field; as ? null; } ]; as ? null; hint ? null; }; and maxItems, the most items a
  # list holds, which bounds what a rule that loops over the list costs. The schema converter
  # checks unique and maxItems against the field's type.
  mkOption =
    {
      rules ? [ ],
      unique ? false,
      maxItems ? null,
      ...
    }@args:
    lib.mkOption (
      removeAttrs args [
        "rules"
        "unique"
        "maxItems"
      ]
    )
    // lib.optionalAttrs (rules != [ ]) { "x-chalkos-rules" = rules; }
    // lib.optionalAttrs (unique != false) { "x-chalkos-unique" = unique; }
    // lib.optionalAttrs (maxItems != null) { "x-chalkos-maxItems" = maxItems; };

  render = {
    # A Go text/template, which writes the file named as the template less .tmpl; a missing or
    # null key prints as nothing.
    template = path: { template = path; };
    # A jq program, whose output, raw for a string, writes the file named as the program less .jq.
    jq = path: { jq = path; };
    # The documents of kinds as they are, each as <Kind>/<name>.json.
    documents = kinds: { documents = kinds; };
  };

  validate = {
    # A jq program that reads the array of documents and makes the report of refusals.
    jq = path: { jq = path; };
  };

  restartOn = effectOn "restart";
  reloadOn = effectOn "reload";
}
