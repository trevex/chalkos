# Converts kind declarations into JSON Schema, draft 2020-12, the schema dialect of OpenAPI 3.1.
# Each option type maps to one schema; a type without a mapping fails the evaluation and names the
# option, so an image never ships a schema that accepts more or less than its options declare.
{ lib }:
let
  types = import ./types.nix { inherit lib; };
  api = import ./api.nix;

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
    # A key reference. Secret is a core kind of every image, so its apiVersion is known here.
    "x-chalkos-ref" = {
      apiVersion = api.apiVersions.core;
      kind = "Secret";
      key = true;
    };
  };

  typeSchema =
    at: t:
    let
      n = t.name;
    in
    if n == types.secretRef.name then
      secretRefSchema
    else if n == types.refName then
      {
        type = "string";
        pattern = "^(?:${types.documentName})$";
        # resolveRefs adds the target's apiVersion, which only the image's kinds tell.
        "x-chalkos-ref" = {
          inherit (t.chalkosRef) kind;
        }
        // lib.optionalAttrs (t.chalkosRef.reserved != [ ]) { inherit (t.chalkosRef) reserved; };
      }
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
        submodule, lib.chalkos.types.ref and lib.chalkos.types.secretRef.
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

  # The normalisers a key's values compare through, which chalkd implements: ip and cidr as the
  # rules' ip() and cidr() compare, host as registry hosts compare, lower in lower case.
  normalisers = [
    "cidr"
    "host"
    "ip"
    "lower"
  ];

  # The schema s without its null branch, as nullOr writes it.
  nonNull =
    s:
    let
      others = lib.filter (b: b != { type = "null"; }) (s.anyOf or [ ]);
    in
    if s ? anyOf && lib.length others == 1 then lib.head others else s;
  isSecretRef = s: (s."x-chalkos-ref".key or false) == true;
  isObject = s: (s.type or null) == "object" && s ? properties;
  # A value a key compares: neither a list nor an object, and no secretRef.
  isScalar =
    s:
    let
      t = nonNull s;
    in
    !isSecretRef t
    && !(lib.elem (t.type or null) [
      "array"
      "object"
    ]);
  # The branches a value of the schema s may take, its anyOf flattened, other than null.
  valueBranches =
    s:
    lib.filter (b: b != { type = "null"; }) (
      if s ? anyOf then lib.concatMap valueBranches s.anyOf else [ s ]
    );
  isStringBranch = b: (b.type or null) == "string" || (b ? enum && lib.all lib.isString b.enum);
  # Whether some or all of the field's values are strings, the values normalisers read.
  takesStrings = s: lib.any isStringBranch (valueBranches s);
  takesOnlyStrings = s: lib.all isStringBranch (valueBranches s);

  # The declarations of the value `of`, a field or a kind's spec, whose schema is s, from the
  # unique declared at `at`: each with its scope, across, filled in, its by fields as { field;
  # as; }, and the attributes left null dropped, so Go reads one shape. A declaration that chalkd
  # could not check, or would check against values the field never holds, is instead the refusal
  # that names where it is declared.
  uniqueDeclarations =
    at: of: s: unique:
    let
      t = nonNull s;
      isList = (t.type or null) == "array";
      item = if isList then nonNull t.items else t;
      what = if isList then "the items of ${of}" else of;
      are = if isList then "are" else "is";
      fail = msg: "${at}: ${msg}";
      member =
        f:
        let
          m = if lib.isString f then { field = f; } else f;
          extra = lib.attrNames (
            removeAttrs m [
              "field"
              "as"
            ]
          );
          field = item.properties.${m.field} or null;
          as = m.as or null;
        in
        if !lib.isAttrs m || !lib.isString (m.field or null) then
          fail "by takes field names or { field; as; }"
        else if extra != [ ] then
          fail "${lib.head extra} of by's ${m.field} is none of field and as"
        else if field == null then
          fail "by names ${m.field}, which is no field of ${what}, whose fields are ${lib.concatStringsSep ", " (lib.attrNames item.properties)}"
        else if !isScalar field then
          fail "by names ${m.field}, a list, an object or a secretRef, which a key cannot hold"
        else if as != null && !takesStrings field then
          fail "${m.field} holds no strings, which a normaliser reads"
        else if as != null && !takesOnlyStrings field then
          fail "${m.field} holds values other than strings too, which a normaliser cannot read"
        else if as != null && !lib.elem as normalisers then
          fail (unknownNormaliser as)
        else
          { inherit (m) field; } // lib.optionalAttrs (as != null) { inherit as; };
      unknownNormaliser =
        a: "the normaliser ${builtins.toJSON a} is none of ${lib.concatStringsSep ", " normalisers}";
      declaration =
        d:
        let
          extra = lib.attrNames (
            removeAttrs d [
              "across"
              "by"
              "as"
              "hint"
            ]
          );
          by = d.by or null;
          as = d.as or null;
          hint = d.hint or null;
          across = d.across or (if isList && by != null then "list" else "kind");
          members = if lib.isList by then map member by else [ ];
          refused = lib.findFirst lib.isString null members;
        in
        if !lib.isAttrs d then
          fail "a declaration is an attribute set { across; by; as; hint; }"
        else if extra != [ ] then
          fail "${lib.head extra} is none of across, by, as and hint"
        else if isSecretRef t || isSecretRef item then
          fail "a secretRef names a key of a Secret, which is no value to compare"
        else if (t.type or null) == "object" && !isObject t then
          fail "${of} is a map, whose keys are unique already"
        else if isList && (item.type or null) == "array" then
          fail "${of} is a list of lists"
        else if
          !lib.elem across [
            "kind"
            "list"
          ]
        then
          fail "across is kind or list, not ${builtins.toJSON across}"
        else if across == "list" && !isList then
          fail "across = \"list\" compares the items of a list, and ${of} is none"
        else if by == null && !isScalar item then
          fail "${what} ${are} ${
            if isList then "objects" else "an object"
          }; give by, the fields that make up the key"
        else if by != null && !isObject item then
          fail "by names fields of objects, and ${what} ${are} none; leave by out to compare the values themselves"
        else if by != null && (!lib.isList by || by == [ ]) then
          fail "by is a list of one field or more"
        else if by != null && as != null then
          fail "as goes on each field of by"
        else if as != null && !takesStrings item then
          fail "a normaliser reads strings, and ${what} ${are} none"
        else if as != null && !takesOnlyStrings item then
          fail "a normaliser reads strings alone, and ${what} may hold other values too"
        else if hint != null && !lib.isString hint then
          fail "hint is a string"
        else if as != null && !lib.elem as normalisers then
          fail (unknownNormaliser as)
        else if refused != null then
          refused
        else
          {
            inherit across;
          }
          // lib.optionalAttrs (by != null) { by = members; }
          // lib.optionalAttrs (as != null) { inherit as; }
          // lib.optionalAttrs (hint != null) { inherit hint; };
    in
    if unique == true then
      [ (declaration { }) ]
    else if lib.isAttrs unique then
      [ (declaration unique) ]
    else if lib.isList unique then
      map declaration unique
    else
      [ "${at} is true, a declaration or a list of declarations" ];

  # The first refusal among a unique's declarations, or null when chalkd can check them all.
  uniqueRefusal =
    at: of: s: unique:
    lib.findFirst lib.isString null (uniqueDeclarations at of s unique);

  # The x-chalkos-unique of a unique's declarations, failing the evaluation with their first
  # refusal.
  uniqueSchema =
    at: of: s: unique:
    let
      refusal = uniqueRefusal at of s unique;
    in
    if refusal != null then throw refusal else uniqueDeclarations at of s unique;

  optionSchema =
    at: opt:
    let
      s = typeSchema at (opt.type or (throw "${at} declares no type"));
    in
    s
    // lib.optionalAttrs (opt ? "x-chalkos-unique") {
      "x-chalkos-unique" = uniqueSchema "${at}.unique" at s opt."x-chalkos-unique";
    }
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
  inherit uniqueRefusal;

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

  # The schema of a kind whose spec is declared with options. The kind's unique, tuples of its
  # spec's fields that no two documents share, marks the spec, as a field's marks the field.
  kindSchema =
    {
      kind,
      group,
      version,
      multiple,
      spec,
      rules ? [ ],
      unique ? [ ],
      description ? null,
    }:
    let
      at = "chalkos.kinds.${kind}";
      specSchema = groupSchema "${at}.spec" spec;
    in
    envelope {
      inherit
        kind
        group
        version
        multiple
        rules
        description
        ;
      spec =
        specSchema
        // lib.optionalAttrs (unique != [ ]) {
          "x-chalkos-unique" = uniqueSchema "${at}.unique" "${at}.spec" specSchema unique;
        };
    };

  fileName =
    {
      kind,
      group,
      version,
      ...
    }:
    "${group}_${version}_${kind}.json";

  # The references of a schema, a JSON value: every x-chalkos-ref below it, in no particular order.
  refsOf =
    v:
    if lib.isAttrs v then
      lib.optional (v ? "x-chalkos-ref") v."x-chalkos-ref"
      ++ lib.concatMap refsOf (lib.attrValues (removeAttrs v [ "x-chalkos-ref" ]))
    else if lib.isList v then
      lib.concatMap refsOf v
    else
      [ ];

  # The schemas of chalkos.kinds, by file name, as the image ships them: each reference with the
  # apiVersion of the kind it names, and each kind's metadata.name refusing the names that
  # references to it reserve. A reference to a kind chalkos.kinds lacks fails the evaluation and
  # names the kind that holds it; networkd's kinds are no targets, as their schemas come from
  # systemd's tables.
  resolveRefs =
    schemas:
    let
      byKind = lib.mapAttrs' (
        _: s: lib.nameValuePair s."x-chalkos-kind".kind s.properties.apiVersion.const
      ) schemas;
      fill =
        from: v:
        if lib.isAttrs v then
          lib.mapAttrs (n: x: if n == "x-chalkos-ref" then target from x else fill from x) v
        else if lib.isList v then
          map (fill from) v
        else
          v;
      target =
        from: r:
        if byKind ? ${r.kind} then
          r // { apiVersion = byKind.${r.kind}; }
        else
          throw "chalkos.kinds.${from}: a field references the kind ${r.kind}, which chalkos.kinds does not declare; it declares ${lib.concatStringsSep ", " (lib.sort lib.lessThan (lib.attrNames byKind))}";
      refs = lib.concatMap refsOf (lib.attrValues schemas);
      reservedOf =
        kind:
        lib.sort lib.lessThan (
          lib.unique (lib.concatMap (r: r.reserved or [ ]) (lib.filter (r: r.kind == kind) refs))
        );
      refuse =
        s:
        let
          names = reservedOf s."x-chalkos-kind".kind;
        in
        if names == [ ] then
          s
        else
          lib.recursiveUpdate s { properties.metadata.properties.name.not.enum = names; };
    in
    lib.mapAttrs (_: s: refuse (fill s."x-chalkos-kind".kind s)) schemas;
}
