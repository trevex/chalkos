# Converts kind declarations into JSON Schema, draft 2020-12, the schema dialect of OpenAPI 3.1.
# Each option type maps to one schema; a type without a mapping fails the evaluation and names the
# option, so an image never ships a schema that accepts more or less than its options declare.
{ lib }:
let
  types = import ./types.nix { inherit lib; };

  # A value JSON carries as it is: literal expressions, paths, packages and functions only make
  # sense to Nix.
  isPlain =
    v:
    if lib.isFunction v || lib.isPath v || lib.isDerivation v then
      false
    else if lib.isAttrs v then
      !(v ? _type) && lib.all isPlain (lib.attrValues v)
    else if lib.isList v then
      lib.all isPlain v
    else
      true;

  # The integer types of lib.types.ints, by the name the type carries.
  intRanges = {
    unsignedInt8 = {
      minimum = 0;
      maximum = 255;
    };
    unsignedInt16 = {
      minimum = 0;
      maximum = 65535;
    };
    unsignedInt32 = {
      minimum = 0;
      maximum = 4294967295;
    };
    signedInt8 = {
      minimum = -128;
      maximum = 127;
    };
    signedInt16 = {
      minimum = -32768;
      maximum = 32767;
    };
    signedInt32 = {
      minimum = -2147483648;
      maximum = 2147483647;
    };
    unsignedInt.minimum = 0;
    positiveInt.minimum = 1;
  };

  # ints.between keeps its bounds only in its description.
  betweenRange =
    at: description:
    let
      m = builtins.match "integer between (-?[0-9]+) and (-?[0-9]+) \\(both inclusive\\)" description;
    in
    if m == null then
      throw "${at}: cannot read the bounds of ints.between from \"${description}\""
    else
      {
        minimum = builtins.fromJSON (builtins.elemAt m 0);
        maximum = builtins.fromJSON (builtins.elemAt m 1);
      };

  secretRefSchema = {
    type = "object";
    properties.secretRef = {
      type = "object";
      properties = {
        name = {
          type = "string";
          minLength = 1;
        };
        key = {
          type = "string";
          minLength = 1;
        };
      };
      required = [
        "key"
        "name"
      ];
      additionalProperties = false;
    };
    required = [ "secretRef" ];
    additionalProperties = false;
    "x-chalkos-secret-ref" = true;
  };

  typeSchema =
    at: t:
    let
      n = t.name;
    in
    if n == types.secretRef.name then
      secretRefSchema
    else if n == types.kubernetesObject.name then
      {
        type = "object";
        properties = {
          apiVersion = {
            type = "string";
            minLength = 1;
          };
          kind = {
            type = "string";
            minLength = 1;
          };
          metadata.type = "object";
        };
        required = [
          "apiVersion"
          "kind"
          "metadata"
        ];
        "x-chalkos-kubernetes-object" = true;
      }
    else if n == types.portRange.name then
      {
        anyOf = [
          {
            type = "integer";
            minimum = 1;
            maximum = 65535;
          }
          {
            type = "string";
            pattern = "^(?:[1-9][0-9]{0,4}-[1-9][0-9]{0,4})$";
          }
        ];
      }
    else if n == types.peerAS.name then
      {
        anyOf = [
          {
            enum = [
              "external"
              "internal"
            ];
          }
          {
            type = "integer";
            minimum = 1;
            maximum = 4294967295;
          }
        ];
      }
    else if n == "str" then
      { type = "string"; }
    else if n == "nonEmptyStr" then
      {
        type = "string";
        minLength = 1;
      }
    else if lib.hasPrefix "strMatching " n then
      {
        type = "string";
        # builtins.match matches the whole string, a JSON Schema pattern any part of it. Nix
        # patterns are POSIX ERE and JSON Schema patterns ECMA-262: classes such as [[:alpha:]]
        # differ between the two, so patterns should stick to their common subset.
        pattern = "^(?:${t.functor.payload.pattern})$";
      }
    else if n == "bool" then
      { type = "boolean"; }
    else if n == "int" then
      { type = "integer"; }
    else if intRanges ? ${n} then
      { type = "integer"; } // intRanges.${n}
    else if n == "intBetween" then
      { type = "integer"; } // betweenRange at t.description
    else if n == "float" then
      { type = "number"; }
    else if n == "enum" then
      { enum = t.functor.payload.values; }
    else if n == "nullOr" then
      {
        anyOf = [
          (typeSchema at t.nestedTypes.elemType)
          { type = "null"; }
        ];
      }
    else if n == "listOf" then
      {
        type = "array";
        items = typeSchema "${at}.*" t.nestedTypes.elemType;
      }
      # nonEmptyListOf keeps the name "listOf": it is addCheck'd onto it rather than a type of its
      # own, and marks itself only in its description. Other addCheck'd types (nonEmptyStr among
      # them) keep their base type's name too, but add no constraint this converter does not
      # already give their base type, so the type table above covers named types and this is the
      # one check worth lifting into JSON Schema.
      // lib.optionalAttrs (lib.hasPrefix "non-empty " t.description) { minItems = 1; }
    else if n == "attrsOf" || n == "lazyAttrsOf" then
      {
        type = "object";
        additionalProperties = typeSchema "${at}.<name>" t.nestedTypes.elemType;
      }
    else if n == "submodule" then
      groupSchema at (t.getSubOptions [ ])
    else
      throw ''
        ${at}: the option type ${t.description} has no JSON Schema mapping. Kinds accept str,
        nonEmptyStr, strMatching, bool, int, ints.*, port, float, enum, nullOr, listOf, attrsOf,
        submodule and lib.chalkos.types.secretRef.
      '';

  # attrsOf and lazyAttrsOf read a submodule's options by instantiating it once, before any
  # attribute name is known; a default or example computed from the submodule's `name` argument
  # then holds the module system's placeholder for it instead of a real value, and must not reach
  # the schema.
  leaksNamePlaceholder = v: lib.hasInfix "‹name›" (builtins.toJSON v);

  # A rule as the schema carries it: as declared, without the attributes left null and with takes
  # defaulted, so Go reads one shape whether a field or a kind declares it. Other attributes, as a
  # misspelt message, reach the schema, where the rules check refuses them.
  ruleSchema = r: lib.filterAttrs (_: v: v != null) ({ takes = "refuse"; } // r);

  optionSchema =
    at: opt:
    typeSchema at (opt.type or (throw "${at} declares no type"))
    // lib.optionalAttrs (lib.isString (opt.description or null)) { inherit (opt) description; }
    // lib.optionalAttrs (opt ? default && isPlain opt.default && !leaksNamePlaceholder opt.default) {
      inherit (opt) default;
    }
    // lib.optionalAttrs (opt ? example && isPlain opt.example && !leaksNamePlaceholder opt.example) {
      examples = [ opt.example ];
    }
    # A field's change rules, which lib.chalkos.mkOption puts under x-chalkos-rules.
    // lib.optionalAttrs (opt ? "x-chalkos-rules") {
      "x-chalkos-rules" = map ruleSchema opt."x-chalkos-rules";
    };

  # An option without a default must be given; a group must be given when one of its options must.
  isRequired =
    at: v:
    if lib.isOption v then
      !(v ? default)
    else if lib.isAttrs v then
      lib.any (name: isRequired "${at}.${name}" v.${name}) (lib.attrNames v)
    else
      throw "${at} is neither an option declaration nor a group of them";

  # An attribute set of option declarations, nested as in a module's options, is an object that
  # accepts its options alone.
  groupSchema =
    at: tree:
    if !(lib.isAttrs tree) then
      throw "${at} is neither an option declaration nor a group of them"
    else
      let
        options = removeAttrs tree [ "_module" ];
        required = lib.filter (name: isRequired "${at}.${name}" options.${name}) (lib.attrNames options);
      in
      {
        type = "object";
        properties = lib.mapAttrs (
          name: v: if lib.isOption v then optionSchema "${at}.${name}" v else groupSchema "${at}.${name}" v
        ) options;
        additionalProperties = false;
      }
      // lib.optionalAttrs (required != [ ]) { inherit required; };
in
rec {
  # The schema of a whole document of a kind, around the schema of its spec. The kind's rules
  # over its documents go into x-chalkos-kind, where a kind without rules has none, and its
  # description, the sentence that says what its documents are for, is the schema's.
  envelope =
    {
      kind,
      group,
      version,
      multiple,
      spec,
      rules ? [ ],
      description ? null,
    }:
    {
      "$schema" = "https://json-schema.org/draft/2020-12/schema";
      title = kind;
      type = "object";
      properties = {
        apiVersion.const = "${group}/${version}";
        kind.const = kind;
        metadata = {
          type = "object";
          properties.name = {
            type = "string";
            pattern = "^${types.documentName}$";
          };
          required = [ "name" ];
          additionalProperties = false;
        };
        inherit spec;
      };
      required = [
        "apiVersion"
        "kind"
        "metadata"
        "spec"
      ];
      additionalProperties = false;
      "x-chalkos-kind" = {
        inherit
          group
          version
          kind
          multiple
          ;
      }
      // lib.optionalAttrs (rules != [ ]) { rules = map ruleSchema rules; };
    }
    // lib.optionalAttrs (description != null) { inherit description; };

  # The schema of a kind whose spec is declared with options.
  kindSchema =
    {
      kind,
      group,
      version,
      multiple,
      spec,
      rules ? [ ],
      description ? null,
    }:
    envelope {
      inherit
        kind
        group
        version
        multiple
        rules
        description
        ;
      spec = groupSchema "chalkos.kinds.${kind}.spec" spec;
    };

  fileName =
    {
      kind,
      group,
      version,
      ...
    }:
    "${group}_${version}_${kind}.json";
}
