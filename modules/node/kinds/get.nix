# The chalkcfg commands of chalkos.kinds.<Kind>.get. A path is checked against the kind's option
# declarations when the image is evaluated, so a field the kind does not have fails the build
# instead of printing nothing on the node.
{ lib }:
let
  isOption = v: lib.isAttrs v && (v._type or null) == "option";

  # What a path may go on with below a value of an option type: the fields of a submodule, the
  # elements of a list, the values of an attribute set, or nothing below a plain value. nullOr
  # adds nothing a path could name.
  typeNode =
    t:
    let
      n = t.name;
    in
    if n == "nullOr" then
      typeNode t.nestedTypes.elemType
    else if n == "listOf" then
      { list = typeNode t.nestedTypes.elemType; }
    else if n == "attrsOf" || n == "lazyAttrsOf" then
      { attrs = typeNode t.nestedTypes.elemType; }
    else if n == "submodule" || n == "chalkosSecretRef" then
      groupNode (t.getSubOptions [ ])
    else
      { value = t.description; };

  # A group of option declarations, nested as in a module's options.
  groupNode = tree: {
    fields = lib.mapAttrs (_: v: if isOption v then typeNode v.type else groupNode v) (
      removeAttrs tree [ "_module" ]
    );
  };

  # A whole document of the kind, as chalkd writes it.
  documentNode = spec: {
    fields = {
      apiVersion.value = "string";
      kind.value = "string";
      metadata.fields.name.value = "string";
      spec = groupNode spec;
    };
  };

  # A field in jsonpath: .name, or ['name'] for a name that is no identifier, such as a key with a
  # dash. null for a name jsonpath cannot quote.
  field =
    s:
    if builtins.match "[A-Za-z_][A-Za-z0-9_]*" s != null then
      ".${s}"
    else if lib.hasInfix "'" s || lib.hasInfix "]" s || lib.hasInfix "\\" s then
      null
    else
      "['${s}']";

  # The jsonpath of the segments below node, at the dotted path at: { jsonpath } or { error }.
  walk =
    node: at: segments:
    if segments == [ ] then
      { jsonpath = ""; }
    else
      let
        s = lib.head segments;
        here = if at == "" then s else "${at}.${s}";
        where = if at == "" then "the document" else at;
        below =
          next: fragment:
          if fragment == null then
            { error = "jsonpath cannot name the field ${s}"; }
          else
            let
              rest = walk next here (lib.tail segments);
            in
            if rest ? error then rest else { jsonpath = fragment + rest.jsonpath; };
        fields = lib.attrNames node.fields;
      in
      if s == "" then
        { error = "the path has an empty field name"; }
      else if node ? fields then
        if node.fields ? ${s} then
          below node.fields.${s} (field s)
        else if fields == [ ] then
          { error = "${where} has no fields"; }
        else
          { error = "${where} has no field ${s}; its fields are ${lib.concatStringsSep ", " fields}"; }
      else if node ? list then
        if s == "*" || builtins.match "[0-9]+" s != null then
          below node.list "[${s}]"
        else
          { error = "${where} is a list: follow it with * or an index such as 0, not ${s}"; }
      else if node ? attrs then
        below node.attrs (if s == "*" then "[*]" else field s)
      else
        { error = "${where} holds a ${node.value} and has no fields"; };
in
rec {
  # The jsonpath expression of a dotted path through a document of a kind whose spec has the given
  # option declarations: { jsonpath } or { error }.
  resolve =
    spec: path:
    let
      r = walk (documentNode spec) "" (lib.splitString "." path);
    in
    if r ? error then r else { jsonpath = "{${r.jsonpath}}"; };

  # The function of chalkos.kinds.<kind>.get.
  command =
    {
      chalkcfg,
      kind,
      spec,
    }:
    {
      name ? null,
      path ? null,
      output ? null,
    }:
    let
      fail = msg: throw "chalkos.kinds.${kind}.get: ${msg}";
      resolved = resolve spec path;
      format =
        if path != null && output != null then
          fail "give a path or an output, not both"
        else if path != null then
          if resolved ? error then
            fail "the path \"${path}\" does not follow the kind's options: ${resolved.error}"
          else
            "jsonpath=${resolved.jsonpath}"
        else if output == null then
          null
        else if
          lib.elem output [
            "json"
            "yaml"
          ]
          || lib.hasPrefix "jsonpath=" output
          || lib.hasPrefix "go-template=" output
        then
          output
        else
          fail "the output is json, yaml, jsonpath=<expression> or go-template=<template>, not ${output}";
    in
    lib.concatStringsSep " " (
      [
        (lib.getExe chalkcfg)
        "get"
        kind
      ]
      ++ lib.optional (name != null) (lib.escapeShellArg name)
      ++ lib.optionals (format != null) [
        "-o"
        (lib.escapeShellArg format)
      ]
    );
}
