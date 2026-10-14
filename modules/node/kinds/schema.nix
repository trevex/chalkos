# Converts kind declarations into JSON Schema, draft 2020-12, the schema dialect of OpenAPI 3.1.
# Each option type maps to one schema; a type without a mapping fails the evaluation and names the
# option, so an image never ships a schema that accepts more or less than its options declare.
{ lib }:
let
  isOption = v: lib.isAttrs v && (v._type or null) == "option";

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
    if n == "chalkosSecretRef" then
      secretRefSchema
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
        submodule and chalkosLib.types.secretRef.
      '';

  # attrsOf and lazyAttrsOf read a submodule's options by instantiating it once, before any
  # attribute name is known; a default or example computed from the submodule's `name` argument
  # then holds the module system's placeholder for it instead of a real value, and must not reach
  # the schema.
  leaksNamePlaceholder = v: lib.hasInfix "‹name›" (builtins.toJSON v);

  optionSchema =
    at: opt:
    typeSchema at (opt.type or (throw "${at} declares no type"))
    // lib.optionalAttrs (lib.isString (opt.description or null)) { inherit (opt) description; }
    // lib.optionalAttrs (opt ? default && isPlain opt.default && !leaksNamePlaceholder opt.default) {
      inherit (opt) default;
    }
    // lib.optionalAttrs (opt ? example && isPlain opt.example && !leaksNamePlaceholder opt.example) {
      examples = [ opt.example ];
    };

  # An option without a default must be given; a group must be given when one of its options must.
  isRequired =
    at: v:
    if isOption v then
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
          name: v: if isOption v then optionSchema "${at}.${name}" v else groupSchema "${at}.${name}" v
        ) options;
        additionalProperties = false;
      }
      // lib.optionalAttrs (required != [ ]) { inherit required; };
in
rec {
  types.secretRef =
    lib.types.submodule {
      options.secretRef = {
        name = lib.mkOption {
          type = lib.types.nonEmptyStr;
          description = "Name of the Secret document.";
        };
        key = lib.mkOption {
          type = lib.types.nonEmptyStr;
          description = "Key in the Secret's data.";
        };
      };
    }
    // {
      # The converter recognises the type by this name.
      name = "chalkosSecretRef";
      description = "reference to a key of a Secret";
    };

  # The schema of a whole document of a kind, around the schema of its spec.
  envelope =
    {
      kind,
      group,
      version,
      multiple,
      spec,
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
            pattern = "^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$";
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
      };
    };

  # The schema of a kind whose spec is declared with options.
  kindSchema =
    {
      kind,
      group,
      version,
      multiple,
      spec,
    }:
    envelope {
      inherit
        kind
        group
        version
        multiple
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
