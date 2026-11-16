# Evaluation tests of config kinds and their schemas, outputs, effects, facts and validators.
# Returns lib.runTests failures, empty on success.
{
  lib,
  flakeLib,
  layers,
}:
let
  inherit (lib) mkOption types;
  chalkosLib = import ../../lib/chalkos.nix { inherit lib; };
  inherit (chalkosLib.internal) schema kindTypes;
  inherit (import ./lib.nix { inherit lib flakeLib layers; })
    fails
    refuses
    etcJSON
    installer
    worker
    router
    ;

  demoModule =
    { chalkosLib, lib, ... }:
    {
      chalkos.kinds.Demo = {
        group = "demo.example.org";
        version = "v1alpha1";
        multiple = true;
        spec.token = lib.mkOption {
          type = lib.types.nullOr chalkosLib.types.secretRef;
          default = null;
        };
      };
    };
  withDemo = worker [ demoModule ];
  # Zone references Lane: lane alone, reserving default, which stands for no Lane; and to in the
  # items of rules, nullable.
  laneModule =
    { chalkosLib, lib, ... }:
    {
      chalkos.kinds.Lane = {
        group = "lane.example.org";
        version = "v1";
        multiple = true;
      };
      chalkos.kinds.Zone = {
        group = "zone.example.org";
        version = "v1";
        multiple = true;
        spec.lane = lib.mkOption {
          type = chalkosLib.types.ref {
            kind = "Lane";
            reserved = [ "default" ];
          };
          default = "default";
        };
        spec.rules = lib.mkOption {
          type = lib.types.listOf (
            lib.types.submodule {
              options.to = lib.mkOption {
                type = lib.types.nullOr (chalkosLib.types.ref "Lane");
                default = null;
              };
            }
          );
          default = [ ];
        };
      };
    };
  withLanes = worker [ laneModule ];
  laneSchemas = withLanes.system.build.chalkosKindSchemas;
  # A unit granted the Demo kind and networkd's Network.
  grantModule = {
    systemd.services.bird = {
      serviceConfig.ExecStart = "/bin/true";
      chalkos.kinds = [
        "Demo"
        "Network"
      ];
    };
  };
  withGrant = worker [
    demoModule
    grantModule
  ];
  servicesOf = etcJSON "services.json";
  effectsOf = etcJSON "effects.json";
  outputsOf = etcJSON "outputs.json";
  validatorsOf = etcJSON "validators.json";
  grantOf =
    kinds:
    worker [
      demoModule
      {
        systemd.services.bird = {
          serviceConfig.ExecStart = "/bin/true";
          chalkos.kinds = kinds;
        };
      }
    ];
  # An image with the module given beside the Demo kind and the unit bird.
  withBird =
    module:
    worker [
      demoModule
      { systemd.services.bird.serviceConfig.ExecStart = "/bin/true"; }
      module
    ];
  # Forces the schemas an image ships, and with them the checks of its kinds' names.
  schemasOf = config: config.system.build.chalkosSchemas.drvPath;

  # The metal image with Kubernetes and no extra modules.
  kubernetesImage = worker [ ];
  # The metal image without a layer.
  plainImage =
    (flakeLib.mkImage {
      name = "worker-metal";
      platform = "metal";
    }).nixos.config;
  # A router image, and the kinds it adds.
  routerImage = router [ ];
  routerKinds = [
    "AddressSet"
    "BGP"
    "BGPPeer"
    "CommunityList"
    "DHCPRelay"
    "DHCPSubnet"
    "FirewallPolicy"
    "FirewallZone"
    "NAT64"
    "NATRule"
    "NextHop"
    "PolicyRoute"
    "PrefixList"
    "Resolver"
    "Router"
    "RoutingPolicy"
    "VRF"
    "WireGuard"
    "WireGuardPeer"
  ];
  hasChalkcfg = packages: lib.any (p: lib.getName p == "chalkcfg") packages;
  # The options of the Peering kind: a nested group, a nullOr submodule, a list of submodules,
  # attribute sets of submodules and of strings, and a secretRef.
  peeringSpec = secretRef: {
    asn = mkOption { type = types.ints.u32; };
    peer.address = mkOption { type = types.str; };
    timers = mkOption {
      type = types.nullOr (
        types.submodule {
          options.hold = mkOption {
            type = types.int;
            default = 90;
          };
        }
      );
      default = null;
    };
    neighbours = mkOption {
      type = types.listOf (types.submodule { options.address = mkOption { type = types.str; }; });
      default = [ ];
    };
    pools = mkOption {
      type = types.attrsOf (types.submodule { options.cidr = mkOption { type = types.str; }; });
      default = { };
    };
    labels = mkOption {
      type = types.attrsOf types.str;
      default = { };
    };
    token = mkOption {
      type = types.nullOr secretRef;
      default = null;
    };
  };
  # The arguments of the command get builds, and whether it runs the image's chalkcfg.
  getPeering =
    let
      withPeering = worker [
        (
          { chalkosLib, ... }:
          {
            chalkos.kinds.Peering = {
              group = "bgp.example.org";
              version = "v1alpha1";
              multiple = true;
              spec = peeringSpec chalkosLib.types.secretRef;
            };
          }
        )
      ];
    in
    args:
    let
      words = lib.splitString " " (withPeering.chalkos.kinds.Peering.get args);
    in
    {
      chalkcfg =
        lib.hasSuffix "/bin/chalkcfg" (lib.head words) && lib.hasPrefix "/nix/store/" (lib.head words);
      args = lib.concatStringsSep " " (lib.tail words);
    };
  resolvePeering = chalkosLib.internal.get.resolve (peeringSpec kindTypes.secretRef);
  kindNames = config: map (k: k.kind) config.system.build.chalkosKinds;
  builtinFixture =
    file: builtins.fromJSON (builtins.readFile (../fixtures/kinds/builtin + "/${file}"));
  builtinSchema =
    kind:
    let
      k = kubernetesImage.chalkos.kinds.${kind};
    in
    schema.kindSchema {
      inherit kind;
      inherit (k)
        group
        version
        multiple
        spec
        rules
        specRules
        unique
        description
        withheld
        ;
    };

  # The spec schema of a kind with the given option declarations.
  specOf =
    spec:
    (schema.kindSchema {
      kind = "T";
      group = "t.example.org";
      version = "v1";
      multiple = false;
      inherit spec;
    }).properties.spec;
  # The schema of a kind, withheld or not, whose field x is a Kubernetes object.
  objectsKind =
    withheld:
    schema.kindSchema {
      kind = "T";
      group = "t.example.org";
      version = "v1";
      multiple = false;
      inherit withheld;
      spec.x = mkOption { type = kindTypes.kubernetesObject; };
    };
  # The schema of a single option of the given type.
  propOf = type: (specOf { x = mkOption { inherit type; }; }).properties.x;
  # The x-chalkos-unique of a field of the type given, declared with unique.
  uniqueOf =
    type: unique:
    (specOf { x = chalkosLib.mkOption { inherit type unique; }; }).properties.x."x-chalkos-unique";
  # The schema of a field of the type given, declared with maxItems.
  maxItemsOf =
    type: maxItems: (specOf { x = chalkosLib.mkOption { inherit type maxItems; }; }).properties.x;
  # A list item with fields of each shape a key may and may not hold.
  reservation = types.submodule {
    options = {
      mac = mkOption { type = types.strMatching "[0-9a-f:]+"; };
      address = mkOption { type = types.nullOr types.str; };
      ge = mkOption {
        type = types.nullOr types.ints.u8;
        default = null;
      };
      port = mkOption {
        type = types.nullOr kindTypes.portRange;
        default = null;
      };
      proto = mkOption {
        type = types.enum [
          "TCP"
          "udp"
        ];
        default = "udp";
      };
      tags = mkOption {
        type = types.listOf types.str;
        default = [ ];
      };
    };
  };
  # An image with the kind Pair, whose spec has from and to, and the kind-level unique given.
  pairWith =
    unique:
    worker [
      (
        { lib, ... }:
        {
          chalkos.kinds.Pair = {
            group = "pair.example.org";
            version = "v1";
            multiple = true;
            spec = {
              from = lib.mkOption { type = lib.types.str; };
              to = lib.mkOption { type = lib.types.str; };
              tags = lib.mkOption {
                type = lib.types.listOf lib.types.str;
                default = [ ];
              };
            };
            inherit unique;
          };
        }
      )
    ];

  demo = {
    kind = "Demo";
    group = "demo.example.org";
    version = "v1alpha1";
    multiple = true;
    spec = {
      name = mkOption {
        type = types.str;
        description = "Name of the demo.";
      };
      replicas = mkOption {
        type = types.ints.u8;
        default = 1;
      };
      mode = mkOption {
        type = types.enum [
          "fast"
          "safe"
        ];
        default = "safe";
      };
      token = mkOption {
        type = types.nullOr kindTypes.secretRef;
        default = null;
      };
      tags = mkOption {
        type = types.listOf types.nonEmptyStr;
        default = [ ];
        example = [ "edge" ];
      };
    };
  };
in
lib.runTests {
  # ref refuses a reserved name no document can have, an unknown attribute and no kind.
  testRefRefuses = {
    expr = map (a: fails (kindTypes.ref a).name) [
      {
        kind = "VRF";
        reserved = [ "Default" ];
      }
      {
        kind = "VRF";
        reserve = [ "default" ];
      }
      { reserved = [ "default" ]; }
    ];
    expected = lib.replicate 3 true;
  };
  # The image's schemas carry the apiVersion of the kind a reference names, wherever it sits.
  testRefGetsItsTargetsAPIVersion = {
    expr =
      let
        spec = laneSchemas."zone.example.org_v1_Zone.json".properties.spec.properties;
      in
      {
        lane = spec.lane."x-chalkos-ref";
        to = (lib.head spec.rules.items.properties.to.anyOf)."x-chalkos-ref";
      };
    expected = {
      lane = {
        apiVersion = "lane.example.org/v1";
        kind = "Lane";
        reserved = [ "default" ];
      };
      to = {
        apiVersion = "lane.example.org/v1";
        kind = "Lane";
      };
    };
  };
  # A Lane cannot be named default, which Zone's lane takes for no Lane.
  testReservedNameRefusedAsTargetName = {
    expr = laneSchemas."lane.example.org_v1_Lane.json".properties.metadata.properties.name.not;
    expected.enum = [ "default" ];
  };
  # Two kinds that reserve different names of Lane each refuse their own, and a reserved name
  # shared with Zone appears once.
  testReservedNamesOfTwoKindsUnite = {
    expr =
      (worker [
        laneModule
        (
          { chalkosLib, lib, ... }:
          {
            chalkos.kinds.Area = {
              group = "area.example.org";
              version = "v1";
              spec.lane = lib.mkOption {
                type = chalkosLib.types.ref {
                  kind = "Lane";
                  reserved = [
                    "none"
                    "default"
                  ];
                };
              };
            };
          }
        )
      ])
      .system.build.chalkosKindSchemas."lane.example.org_v1_Lane.json".properties.metadata.properties.name.not;
    expected.enum = [
      "default"
      "none"
    ];
  };
  testRefToAKindTheImageLacksFails = {
    expr = fails (
      schemasOf (worker [
        (
          { chalkosLib, lib, ... }:
          {
            chalkos.kinds.Zone = {
              group = "zone.example.org";
              version = "v1";
              spec.lane = lib.mkOption { type = chalkosLib.types.ref "Lane"; };
            };
          }
        )
      ])
    );
    expected = true;
  };
  # get reads a reference as the string it is.
  testRefGet = {
    expr = lib.hasSuffix "get Zone a -o 'jsonpath={.spec.lane}'" (
      withLanes.chalkos.kinds.Zone.get {
        name = "a";
        path = "spec.lane";
      }
    );
    expected = true;
  };
  # unique = true compares a field's values across the kind's documents, also where it may be
  # null, and marks the field beside its anyOf.
  testUniqueOnAScalar = {
    expr = [
      (uniqueOf types.ints.u32 true)
      (specOf {
        x = chalkosLib.mkOption {
          type = types.nullOr types.ints.u32;
          default = null;
          unique = true;
        };
      }).properties.x
    ];
    expected = [
      [ { across = "kind"; } ]
      {
        anyOf = [
          (propOf types.ints.u32)
          { type = "null"; }
        ];
        default = null;
        "x-chalkos-unique" = [ { across = "kind"; } ];
      }
    ];
  };
  # Without by, a list's items compare across the kind unless across says list; as and hint carry
  # over.
  testUniqueOnAListOfValues = {
    expr = [
      (uniqueOf (types.listOf types.str) { })
      (uniqueOf (types.nonEmptyListOf types.str) {
        across = "list";
        as = "ip";
        hint = "name each server once";
      })
    ];
    expected = [
      [ { across = "kind"; } ]
      [
        {
          across = "list";
          as = "ip";
          hint = "name each server once";
        }
      ]
    ];
  };
  # An enum of strings takes a normaliser, on the field and on a field of by.
  testUniqueNormalisesAnEnumOfStrings = {
    expr = [
      (uniqueOf (types.nullOr (
        types.enum [
          "A"
          "b"
        ]
      )) { as = "lower"; })
      (uniqueOf (types.listOf reservation) {
        by = [
          {
            field = "proto";
            as = "lower";
          }
        ];
      })
    ];
    expected = [
      [
        {
          across = "kind";
          as = "lower";
        }
      ]
      [
        {
          across = "list";
          by = [
            {
              field = "proto";
              as = "lower";
            }
          ];
        }
      ]
    ];
  };
  # With by, a list's items compare within the list by default, each declaration on its own; a
  # field name is a field without a normaliser, and a null as and hint are left out.
  testUniqueByOnAList = {
    expr = uniqueOf (types.listOf reservation) [
      {
        by = [
          {
            field = "mac";
            as = "lower";
          }
        ];
      }
      {
        by = [
          "address"
          {
            field = "ge";
            as = null;
          }
        ];
        hint = null;
      }
      {
        across = "kind";
        by = [ "mac" ];
        hint = "reserve each MAC once";
      }
    ];
    expected = [
      {
        across = "list";
        by = [
          {
            field = "mac";
            as = "lower";
          }
        ];
      }
      {
        across = "list";
        by = [
          { field = "address"; }
          { field = "ge"; }
        ];
      }
      {
        across = "kind";
        by = [ { field = "mac"; } ];
        hint = "reserve each MAC once";
      }
    ];
  };
  # An object, here one that may be null, compares by its fields across the kind.
  testUniqueByOnAnObject = {
    expr = uniqueOf (types.nullOr reservation) {
      by = [
        "mac"
        "ge"
      ];
    };
    expected = [
      {
        across = "kind";
        by = [
          { field = "mac"; }
          { field = "ge"; }
        ];
      }
    ];
  };
  # A list within list items carries its own declaration, which chalkd applies to each such list.
  testUniqueWithinListItems = {
    expr =
      (propOf (
        types.listOf (
          types.submodule {
            options.protocols = chalkosLib.mkOption {
              type = types.listOf types.str;
              unique.across = "list";
            };
          }
        )
      )).items.properties.protocols."x-chalkos-unique";
    expected = [ { across = "list"; } ];
  };
  # A kind's unique marks its spec, with its fields as by's.
  testKindUnique = {
    expr =
      (pairWith [
        {
          by = [
            "from"
            {
              field = "to";
              as = "lower";
            }
          ];
          hint = "give each pair one document";
        }
      ])
      .system.build.chalkosKindSchemas."pair.example.org_v1_Pair.json".properties.spec."x-chalkos-unique";
    expected = [
      {
        across = "kind";
        by = [
          { field = "from"; }
          {
            field = "to";
            as = "lower";
          }
        ];
        hint = "give each pair one document";
      }
    ];
  };
  testKindWithoutUniqueHasNoMarker = {
    expr =
      (pairWith [ ]).system.build.chalkosKindSchemas."pair.example.org_v1_Pair.json".properties.spec
        ? "x-chalkos-unique";
    expected = false;
  };
  # Each declaration chalkd could not check, or would check against values the field never holds,
  # fails the evaluation, by the refusal meant for it rather than one an earlier check throws.
  testUniqueRefusesBadDeclarations =
    let
      cases = [
        {
          type = types.listOf types.str;
          unique.as = "mac";
          refusal = ''the normaliser "mac" is none of cidr, host, ip, lower'';
        }
        {
          type = types.listOf types.str;
          unique.by = [ "x" ];
          refusal = "by names fields of objects, and the items of x are none; leave by out to compare the values themselves";
        }
        {
          type = types.str;
          unique.across = "list";
          refusal = ''across = "list" compares the items of a list, and x is none'';
        }
        {
          type = types.str;
          unique.across = "node";
          refusal = ''across is kind or list, not "node"'';
        }
        {
          type = types.listOf reservation;
          unique.by = [ "serial" ];
          refusal = "by names serial, which is no field of the items of x, whose fields are address, ge, mac, port, proto, tags";
        }
        {
          type = types.listOf reservation;
          unique.by = [ "tags" ];
          refusal = "by names tags, a list, an object or a secretRef, which a key cannot hold";
        }
        {
          type = types.listOf reservation;
          unique.by = [ ];
          refusal = "by is a list of one field or more";
        }
        {
          type = types.listOf reservation;
          unique = {
            by = [ "mac" ];
            as = "lower";
          };
          refusal = "as goes on each field of by";
        }
        {
          type = types.listOf reservation;
          unique.by = [
            {
              field = "ge";
              as = "ip";
            }
          ];
          refusal = "ge holds no strings, which a normaliser reads";
        }
        {
          type = types.listOf reservation;
          unique.by = [
            {
              field = "port";
              as = "lower";
            }
          ];
          refusal = "port holds values other than strings too, which a normaliser cannot read";
        }
        {
          type = types.listOf reservation;
          unique = true;
          refusal = "the items of x are objects; give by, the fields that make up the key";
        }
        {
          type = types.nullOr kindTypes.secretRef;
          unique = true;
          refusal = "a secretRef names a key of a Secret, which is no value to compare";
        }
        {
          type = types.listOf kindTypes.secretRef;
          unique = true;
          refusal = "a secretRef names a key of a Secret, which is no value to compare";
        }
        {
          type = types.ints.u32;
          unique.as = "lower";
          refusal = "a normaliser reads strings, and x is none";
        }
        {
          type = types.nullOr kindTypes.portRange;
          unique.as = "lower";
          refusal = "a normaliser reads strings alone, and x may hold other values too";
        }
        {
          type = types.listOf kindTypes.peerAS;
          unique.as = "lower";
          refusal = "a normaliser reads strings alone, and the items of x may hold other values too";
        }
        {
          type = types.str;
          unique.scope = "kind";
          refusal = "scope is none of across, by, as and hint";
        }
        {
          type = types.str;
          unique.hint = 1;
          refusal = "hint is a string";
        }
        {
          type = types.attrsOf types.str;
          unique = true;
          refusal = "x is a map, whose keys are unique already";
        }
        {
          type = types.listOf (types.listOf types.str);
          unique.across = "list";
          refusal = "x is a list of lists";
        }
        {
          type = types.str;
          unique = "kind";
          at = "x.unique is true, a declaration or a list of declarations";
        }
      ];
    in
    {
      expr = map (x: {
        fails = fails (uniqueOf x.type x.unique);
        refusal = schema.uniqueRefusal "x.unique" "x" (propOf x.type) x.unique;
      }) cases;
      expected = map (x: {
        fails = true;
        refusal = x.at or "x.unique: ${x.refusal}";
      }) cases;
    };
  testKindUniqueRefusesBadDeclarations =
    let
      cases = [
        {
          unique = [ { by = [ "nope" ]; } ];
          refusal = "by names nope, which is no field of chalkos.kinds.Pair.spec, whose fields are from, tags, to";
        }
      ];
      # Pair's spec, as pairWith declares it.
      pairSpec = specOf {
        from = mkOption { type = types.str; };
        to = mkOption { type = types.str; };
        tags = mkOption {
          type = types.listOf types.str;
          default = [ ];
        };
      };
    in
    {
      expr = map (x: {
        fails = fails (schemasOf (pairWith x.unique));
        refusal =
          schema.uniqueRefusal "chalkos.kinds.Pair.unique" "chalkos.kinds.Pair.spec" pairSpec
            x.unique;
      }) cases;
      expected = map (x: {
        fails = true;
        refusal = "chalkos.kinds.Pair.unique: ${x.refusal}";
      }) cases;
    };
  testDemoSchemaMatchesFixture = {
    expr = schema.kindSchema demo;
    expected = builtins.fromJSON (
      builtins.readFile ../fixtures/kinds/image/schemas/demo.example.org_v1alpha1_Demo.json
    );
  };
  # The schemas of option types the kinds' fixtures do not show.
  testTypes = {
    expr = lib.mapAttrs (_: propOf) {
      inherit (types) int float;
      s8 = types.ints.s8;
      between = types.ints.between (-5) 5;
      lazyAttrsOf = types.lazyAttrsOf types.int;
    };
    expected = {
      int.type = "integer";
      float.type = "number";
      s8 = {
        type = "integer";
        minimum = -128;
        maximum = 127;
      };
      between = {
        type = "integer";
        minimum = -5;
        maximum = 5;
      };
      lazyAttrsOf = {
        type = "object";
        additionalProperties.type = "integer";
      };
    };
  };
  # chalkosLib.mkOption's maxItems bounds a list, and fails the evaluation on any other type or
  # on a bound that is no positive integer.
  testMaxItems = {
    expr = maxItemsOf (types.nonEmptyListOf types.str) 8;
    expected = {
      type = "array";
      items.type = "string";
      minItems = 1;
      maxItems = 8;
    };
  };
  testMaxItemsRefusesNonLists = {
    expr = map (x: fails (maxItemsOf x.type x.maxItems)) [
      {
        type = types.str;
        maxItems = 8;
      }
      {
        type = types.nullOr (types.listOf types.str);
        maxItems = 8;
      }
      {
        type = types.listOf types.str;
        maxItems = 0;
      }
    ];
    expected = [
      true
      true
      true
    ];
  };
  testSubmodule = {
    expr = propOf (
      types.submodule {
        options.a = mkOption { type = types.str; };
        options.b = mkOption {
          type = types.bool;
          default = false;
        };
      }
    );
    expected = {
      type = "object";
      properties = {
        a.type = "string";
        b = {
          type = "boolean";
          default = false;
        };
      };
      required = [ "a" ];
      additionalProperties = false;
    };
  };
  # attrsOf and lazyAttrsOf build their submodule before any name exists, so a default from the
  # submodule's name is a placeholder and stays out of the schema.
  testSubmoduleNamePlaceholderDropsDefault = {
    expr =
      (propOf (
        types.attrsOf (
          types.submodule (
            { name, ... }:
            {
              options.a = mkOption {
                type = types.str;
                default = name;
              };
            }
          )
        )
      )).additionalProperties.properties.a
        ? default;
    expected = false;
  };
  # A group of options without mkOption of its own is an object, required when one of its
  # options is.
  testNestedGroupIsRequiredWhenAChildIs = {
    expr = (specOf { peer.address = mkOption { type = types.str; }; }).required;
    expected = [ "peer" ];
  };
  testNestedGroupIsOptionalWhenNoChildIs = {
    expr =
      (specOf {
        peer.port = mkOption {
          type = types.port;
          default = 179;
        };
      })
        ? required;
    expected = false;
  };
  testLiteralExampleIsDropped = {
    expr =
      (specOf {
        x = mkOption {
          type = types.str;
          default = "a";
          example = lib.literalExpression "pkgs.hello";
        };
      }).properties.x
        ? examples;
    expected = false;
  };
  testUnsupportedTypeFails = {
    expr = fails (propOf (types.either types.str types.int));
    expected = true;
  };
  testOptionWithoutTypeFails = {
    expr = fails (specOf {
      x = mkOption { default = 1; };
    });
    expected = true;
  };
  testGroupLeafWithoutOptionFails = {
    expr = fails (specOf {
      x = "foo";
    });
    expected = true;
  };
  testKindDir = {
    expr = withDemo.chalkos.kinds.Demo.dir;
    expected = "/run/chalkos/config/demo.example.org/Demo";
  };
  testImageShipsSchemas = {
    expr = "${withDemo.environment.etc."chalkos/schemas".source}";
    expected = "${withDemo.system.build.chalkosSchemas}";
  };
  testKindsListed = {
    expr = lib.elem {
      apiVersion = "demo.example.org/v1alpha1";
      kind = "Demo";
      multiple = true;
    } withDemo.system.build.chalkosKinds;
    expected = true;
  };
  testNetworkdKindsListed = {
    expr = lib.filter (
      k: k.apiVersion == "networkd.chalkos.dev/v1alpha1"
    ) withDemo.system.build.chalkosKinds;
    expected =
      map
        (kind: {
          apiVersion = "networkd.chalkos.dev/v1alpha1";
          inherit kind;
          multiple = true;
        })
        [
          "Link"
          "NetDev"
          "Network"
        ];
  };
  # A kind is refused when a built-in kind has its name, when its group is reserved, and when its
  # name, group or version is malformed.
  testKindDeclarationsRefused = {
    expr =
      map
        (
          {
            name,
            group,
            version ? "v1",
          }:
          fails (schemasOf (worker [ { chalkos.kinds.${name} = { inherit group version; }; } ]))
        )
        [
          {
            name = "Demo";
            group = "demo.example.org";
          }
          {
            name = "Network";
            group = "demo.example.org";
          }
          {
            name = "Host";
            group = "demo.example.org";
          }
          {
            name = "Bridge";
            group = "networkd.chalkos.dev";
            version = "v1alpha1";
          }
          {
            name = "Mine";
            group = "extra.chalkos.dev";
          }
          {
            name = "demo";
            group = "demo.example.org";
          }
          {
            name = "Demo";
            group = "Demo";
          }
          {
            name = "Demo";
            group = "demo.example.org";
            version = "1.0";
          }
        ];
    expected = [ false ] ++ lib.replicate 7 true;
  };
  testBuiltinKindsOfKubernetesImage = {
    expr = lib.sort lib.lessThan (kindNames kubernetesImage);
    expected = [
      "Addon"
      "BGPPeer"
      "BGPSpeaker"
      "Cluster"
      "Flannel"
      "Host"
      "Link"
      "Mirror"
      "NetDev"
      "Network"
      "Node"
      "Registry"
      "Secret"
      "SystemDisk"
      "TimeSync"
      "Volume"
    ];
  };
  # BGP's kinds belong to Kubernetes images that carry the feature: an image that leaves it out
  # and an image without a layer accept neither.
  testKubernetesBGPKinds = {
    expr = {
      groups = map (k: kubernetesImage.chalkos.kinds.${k}.group) [
        "BGPSpeaker"
        "BGPPeer"
      ];
      without = lib.intersectLists [
        "BGPSpeaker"
        "BGPPeer"
      ] (kindNames (worker [ { chalkos.kubernetes.bgp.enable = false; } ]));
      plain = lib.intersectLists [
        "BGPSpeaker"
        "BGPPeer"
      ] (kindNames plainImage);
    };
    expected = {
      groups = [
        "kubernetes.chalkos.dev"
        "kubernetes.chalkos.dev"
      ];
      without = [ ];
      plain = [ ];
    };
  };
  testImageWithoutKubernetesHasNoNode = {
    expr = lib.elem "Node" (kindNames plainImage);
    expected = false;
  };
  testImageWithoutKubernetesHasNoKubernetesKinds = {
    expr = lib.intersectLists [
      "Addon"
      "Cluster"
      "Flannel"
    ] (kindNames plainImage);
    expected = [ ];
  };
  # The kinds of router.chalkos.dev exist on router images alone, and their schemas lie in the
  # fixtures, which pkg/router/derive's test holds to the kinds the router's model reads, as the
  # validator router reads those the image declares.
  testRouterKindsOnRouterImagesAlone = {
    expr = {
      router = lib.attrNames (
        lib.filterAttrs (_: k: k.group == "router.chalkos.dev") routerImage.chalkos.kinds
      );
      fixtures = map (
        file: lib.removeSuffix ".json" (lib.removePrefix "router.chalkos.dev_v1alpha1_" file)
      ) (lib.attrNames (builtins.readDir ../fixtures/kinds/router));
      kubernetes = lib.attrNames (
        lib.filterAttrs (_: k: k.group == "router.chalkos.dev") kubernetesImage.chalkos.kinds
      );
      plain = lib.intersectLists routerKinds (kindNames plainImage);
    };
    expected = {
      router = routerKinds;
      fixtures = routerKinds;
      kubernetes = [ ];
      plain = [ ];
    };
  };
  # chalkos-render's golden test and the router rules' tests read these fixtures, so they are the
  # router image's schemas.
  testRouterSchemasMatchFixtures = {
    expr = map (
      kind: routerImage.system.build.chalkosKindSchemas."router.chalkos.dev_v1alpha1_${kind}.json"
    ) routerKinds;
    expected = map (
      kind:
      builtins.fromJSON (
        builtins.readFile (../fixtures/kinds/router + "/router.chalkos.dev_v1alpha1_${kind}.json")
      )
    ) routerKinds;
  };
  testImageWithoutKubernetesHasRegistryAndMirror = {
    expr = lib.subtractLists (kindNames plainImage) [
      "Registry"
      "Mirror"
    ];
    expected = [ ];
  };
  # Registries and endpoints as containerd takes them: IPv6 hosts in brackets, endpoints with a
  # scheme and an optional path.
  testMirrorEndpoints =
    let
      spec = kubernetesImage.chalkos.kinds.Mirror.spec;
      registry = spec.registry.type.check;
      endpoint = spec.endpoints.type.nestedTypes.elemType.check;
    in
    {
      expr = {
        registries = map registry [
          "docker.io"
          "localhost:5000"
          "[fd00::1]:5000"
          "[fd00::1]"
        ];
        badRegistries = map registry [
          "https://docker.io"
          "[fd00::1"
          "docker.io/library"
        ];
        endpoints = map endpoint [
          "https://mirror.example.com"
          "http://[fd00::1]:5000"
          "https://mirror.lan:5000/cache"
        ];
        badEndpoints = map endpoint [
          "ftp://mirror.example.com"
          "mirror.lan:5000"
          "[fd00::1]:5000"
          "https://m.example/?x=1"
          "mirror lan"
        ];
      };
      expected = {
        registries = [
          true
          true
          true
          true
        ];
        badRegistries = [
          false
          false
          false
        ];
        endpoints = [
          true
          true
          true
        ];
        badEndpoints = [
          false
          false
          false
          false
          false
        ];
      };
    };
  # Flannel is a kind only where flannel is the pod network.
  testFlannelKindFollowsTheProvider = {
    expr = lib.elem "Flannel" (kindNames (worker [ { chalkos.cni.provider = "none"; } ]));
    expected = false;
  };
  # A Kubernetes object: an object with apiVersion, kind and metadata, and any other field.
  testKubernetesObjectSchema = {
    expr = (objectsKind true).properties.spec.properties.x;
    expected = {
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
    };
  };
  # A kind whose spec embeds Kubernetes objects, at any depth, is withheld: the services would see
  # the Secrets among them hidden by content alone.
  testKubernetesObjectsWithheld = {
    expr = {
      withheld = fails (objectsKind true);
      notWithheld = fails (objectsKind false);
      nested = fails (
        schema.kindSchema {
          kind = "T";
          group = "t.example.org";
          version = "v1";
          multiple = false;
          spec.a.b = mkOption { type = types.listOf kindTypes.kubernetesObject; };
        }
      );
    };
    expected = {
      withheld = false;
      notWithheld = true;
      nested = true;
    };
  };
  # Extensions declare no raw objects.
  testKubernetesObjectIsNotPublic = {
    expr =
      (worker [
        (
          { chalkosLib, ... }:
          {
            chalkos.kinds.Demo = {
              group = "demo.example.org";
              version = "v1alpha1";
              spec.x = mkOption { type = chalkosLib.types.kubernetesObject or null; };
            };
          }
        )
      ]).chalkos.kinds.Demo.spec.x.type;
    expected = null;
  };
  testBuiltinSchemasMatchFixtures = {
    expr = map builtinSchema [
      "Host"
      "Flannel"
      "Cluster"
      "Addon"
      "Node"
      "Registry"
      "Mirror"
      "Secret"
      "SystemDisk"
      "TimeSync"
      "Volume"
    ];
    expected = map builtinFixture [
      "chalkos.dev_v1alpha1_Host.json"
      "kubernetes.chalkos.dev_v1alpha1_Flannel.json"
      "kubernetes.chalkos.dev_v1alpha1_Cluster.json"
      "kubernetes.chalkos.dev_v1alpha1_Addon.json"
      "kubernetes.chalkos.dev_v1alpha1_Node.json"
      "oci.chalkos.dev_v1alpha1_Registry.json"
      "oci.chalkos.dev_v1alpha1_Mirror.json"
      "chalkos.dev_v1alpha1_Secret.json"
      "storage.chalkos.dev_v1alpha1_SystemDisk.json"
      "time.chalkos.dev_v1alpha1_TimeSync.json"
      "storage.chalkos.dev_v1alpha1_Volume.json"
    ];
  };
  # chalkd's tests carry out the built-in effects and grants from these fixtures, so they are the
  # effects.json and services.json of an image with the Kubernetes layer.
  testBuiltinEffectsMatchFixture = {
    expr = effectsOf kubernetesImage;
    expected = builtins.fromJSON (builtins.readFile ../fixtures/kinds/effects.json);
  };
  testBuiltinServicesMatchFixture = {
    expr = servicesOf kubernetesImage;
    expected = builtins.fromJSON (builtins.readFile ../fixtures/kinds/services.json);
  };
  testGrantLoadsItsSecrets = {
    expr = withGrant.systemd.services.bird.serviceConfig.LoadCredential;
    expected = [ "secret:/run/chalkos/credentials/bird" ];
  };
  # A shell on a node queries the config with chalkcfg; the installer has no config to query.
  testImageHasChalkcfg = {
    expr = hasChalkcfg kubernetesImage.environment.systemPackages;
    expected = true;
  };
  testInstallerHasNoChalkcfg = {
    expr = hasChalkcfg (installer [ ]).environment.systemPackages;
    expected = false;
  };
  testGrantHasChalkcfgOnPath = {
    expr = hasChalkcfg withGrant.systemd.services.bird.path;
    expected = true;
  };
  # A grant reaches chalkd, and restarts nothing: there is no effect of its own.
  testGrantRecordedForChalkd = {
    expr = {
      service = (servicesOf withGrant).bird;
      effect = (effectsOf withGrant) ? bird;
    };
    expected = {
      service.kinds = [
        "Demo"
        "Network"
      ];
      effect = false;
    };
  };
  testEffectsReachChalkd = {
    expr =
      (effectsOf (withBird {
        chalkos.effects.bird = {
          when = "facts.share != oldFacts.share";
          restart = [ "bird.service" ];
          start = true;
          run = "birdc configure";
          chalkd = "kubernetes:reload";
        };
      })).bird;
    expected = {
      outputs = [ ];
      kinds = [ ];
      when = "facts.share != oldFacts.share";
      restart = [ "bird.service" ];
      reload = [ ];
      run = "birdc configure";
      start = true;
      chalkd = "kubernetes:reload";
      hint = null;
    };
  };
  # An output reaches chalkd with its check as a script of the store.
  testOutputsReachChalkd =
    let
      config = withBird {
        chalkos.outputs.bird-conf = {
          inputs = [ "Demo" ];
          render = "/nix/store/x-render/bin/render";
          check = ''grep -q protocol "$OUT/bird.conf"'';
          user = "bird";
        };
        chalkos.effects.bird = {
          outputs = [ "bird-conf" ];
          reload = [ "bird.service" ];
        };
      };
      output = (outputsOf config).bird-conf;
    in
    {
      expr = {
        inherit (output)
          inputs
          render
          dir
          user
          group
          mode
          ;
        check = lib.hasSuffix "-chalkos-output-bird-conf-check" output.check;
        outputs = (effectsOf config).bird.outputs;
        dirOption = config.chalkos.outputs.bird-conf.dir;
      };
      expected = {
        inputs = [ "Demo" ];
        render = "/nix/store/x-render/bin/render";
        dir = "/run/chalkos/outputs/bird-conf";
        user = "bird";
        group = "root";
        mode = "0644";
        check = true;
        outputs = [ "bird-conf" ];
        dirOption = "/run/chalkos/outputs/bird-conf";
      };
    };
  # chalkosLib's renderers are data, which an output's render turns into the program's command,
  # naming the file it writes after the template or program.
  testRenderers =
    let
      config = withBird (
        { chalkosLib, pkgs, ... }:
        {
          chalkos.outputs = {
            t.render = chalkosLib.render.template ../../modules/testing/greeting.tmpl;
            d.render = chalkosLib.render.documents [
              "Peer"
              "Link"
            ];
            j.render = chalkosLib.render.jq (builtins.toFile "bird.conf.jq" ".");
            p.render = pkgs.writeShellScript "r" "true";
          };
        }
      );
      render = name: (outputsOf config).${name}.render;
    in
    {
      expr = {
        value = chalkosLib.render.documents [ "Peer" ];
        template = builtins.match ".*/bin/chalkos-render template /nix/store/[^ ]*-greeting.tmpl (.*)" (
          render "t"
        );
        documents = builtins.match ".*/bin/chalkos-render (documents Peer Link)" (render "d");
        jq = lib.hasSuffix "-chalkos-render-jq-bird.conf" (render "j");
        package = lib.hasPrefix "/nix/store/" (render "p");
      };
      expected = {
        value.documents = [ "Peer" ];
        template = [ "greeting" ];
        documents = [ "documents Peer Link" ];
        jq = true;
        package = true;
      };
    };
  # A validator reaches chalkd with its input kinds and its program's command: a command as it is,
  # a package as its path, and a jq program of chalkosLib.validate as a wrapper of the store.
  testValidatorsReachChalkd =
    let
      config = withBird (
        { chalkosLib, pkgs, ... }:
        {
          chalkos.validators = {
            c = {
              inputs = [ "Demo" ];
              program = "/nix/store/x-validate/bin/validate --strict";
            };
            p = {
              inputs = [
                "Demo"
                "Network"
              ];
              program = pkgs.writeShellScript "v" "true";
            };
            j = {
              inputs = [ "Demo" ];
              program = chalkosLib.validate.jq (builtins.toFile "demo.jq" "{refusals: []}");
            };
          };
        }
      );
      validators = validatorsOf config;
    in
    {
      expr = {
        inherit (validators) c;
        inputs = validators.p.inputs;
        package = lib.hasPrefix "/nix/store/" validators.p.program;
        jq = lib.hasSuffix "-chalkos-validate-jq-j" validators.j.program;
        value = chalkosLib.validate.jq "x.jq";
      };
      expected = {
        c = {
          inputs = [ "Demo" ];
          program = "/nix/store/x-validate/bin/validate --strict";
        };
        inputs = [
          "Demo"
          "Network"
        ];
        package = true;
        jq = true;
        value.jq = "x.jq";
      };
    };
  # A validator of a kind the image does not declare, of Secret documents, of no kind, of a name
  # that is not plain, or whose program is no command or chalkosLib value fails the evaluation.
  testValidatorsRefused =
    let
      refused =
        v:
        fails (
          validatorsOf (withBird {
            chalkos.validators = v;
          })
        );
      program = "/nix/store/x-validate/bin/validate";
    in
    {
      expr = {
        unknown = refused {
          v.inputs = [ "Nope" ];
          v.program = program;
        };
        secret = refused {
          v.inputs = [ "Secret" ];
          v.program = program;
        };
        none = refused {
          v.inputs = [ ];
          v.program = program;
        };
        name = refused {
          "Demo.check".inputs = [ "Demo" ];
          "Demo.check".program = program;
        };
        value = refused {
          v.inputs = [ "Demo" ];
          v.program.python = "x.py";
        };
        accepted = refused {
          v.inputs = [ "Demo" ];
          v.program = program;
        };
      };
      expected = {
        unknown = true;
        secret = true;
        none = true;
        name = true;
        value = true;
        accepted = false;
      };
    };
  # chalkosLib's effects act on units when outputs or the documents of kinds change.
  testEffectHelpers = {
    expr = {
      restart = chalkosLib.restartOn { outputs = [ "bird-conf" ]; } [ "bird.service" ];
      reload = chalkosLib.reloadOn { kinds = [ "Demo" ]; } [ "bird.service" ];
    };
    expected = {
      restart = {
        outputs = [ "bird-conf" ];
        kinds = [ ];
        restart = [ "bird.service" ];
      };
      reload = {
        outputs = [ ];
        kinds = [ "Demo" ];
        reload = [ "bird.service" ];
      };
    };
  };
  # An effect on a kind reaches chalkd.
  testEffectOnAKindReachesChalkd = {
    expr =
      (effectsOf (withBird {
        chalkos.effects.bird = {
          kinds = [ "Demo" ];
          restart = [ "bird.service" ];
        };
      })).bird.kinds;
    expected = [ "Demo" ];
  };
  # An effect fails the evaluation when it names an output the image lacks, which a misspelling
  # gives, a kind the image lacks, Secret or Addon, or when it has no trigger or no action.
  testEffectsRefused =
    let
      onKind = kind: {
        kinds = [ kind ];
        restart = [ "bird.service" ];
      };
    in
    {
      expr =
        lib.mapAttrs
          (
            _: e:
            fails (
              effectsOf (withBird {
                chalkos.effects.bird = e;
              })
            )
          )
          {
            output = {
              outputs = [ "bird-cnf" ];
              reload = [ "bird.service" ];
            };
            unknownKind = onKind "Nope";
            secret = onKind "Secret";
            addon = onKind "Addon";
            noTrigger.restart = [ "bird.service" ];
            noAction.when = "true";
          };
      expected = {
        output = true;
        unknownKind = true;
        secret = true;
        addon = true;
        noTrigger = true;
        noAction = true;
      };
    };
  # An output fails the evaluation when its renderer is none of chalkosLib's or a mix of two, when
  # it reads a kind the image lacks or Secret, and when its name is not plain.
  testOutputsRefused =
    let
      render = "/nix/store/x-render/bin/render";
    in
    {
      expr =
        lib.mapAttrs
          (
            _: o:
            fails (
              outputsOf (withBird {
                chalkos.outputs = o;
              })
            )
          )
          {
            accepted.bird-conf = {
              inputs = [ "Demo" ];
              inherit render;
            };
            renderer.t.render.python = "x.py";
            twoRenderers.t.render = {
              jq = "a.jq";
              template = "a.tmpl";
            };
            unknownKind.bird-conf = {
              inputs = [ "Nope" ];
              inherit render;
            };
            secret.bird-conf = {
              inputs = [ "Secret" ];
              inherit render;
            };
            name."Bird.conf" = { inherit render; };
          };
      expected = {
        accepted = false;
        renderer = true;
        twoRenderers = true;
        unknownKind = true;
        secret = true;
        name = true;
      };
    };
  # An effect's chalkd action or run names a layer of the image; another layer fails.
  testEffectOfAnotherLayerFails = {
    expr =
      map
        (
          e:
          fails (
            effectsOf (withBird {
              chalkos.effects.bird = {
                when = "true";
              }
              // e;
            })
          )
        )
        [
          { chalkd = "router:reload"; }
          { run = "/nix/store/x-chalkd/bin/chalkd router x"; }
          { chalkd = "kubernetes:restart"; }
          { run = "/nix/store/x-chalkd/bin/chalkd load-config"; }
          { run = "/nix/store/x-chalkd/bin/chalkd \tload-config"; }
          { run = "/nix/store/x-chalkd/bin/chalkd kubernetes vxlan-rule"; }
          # A layer's command without the layer's name names no layer of the image.
          { run = "/nix/store/x-chalkd/bin/chalkd vxlan-rule"; }
        ];
    expected = [
      true
      true
      false
      false
      false
      false
      true
    ];
  };
  testEffectHintReachesChalkd = {
    expr =
      (effectsOf (withBird {
        chalkos.effects.bird = {
          when = "true";
          restart = [ "bird.service" ];
          hint = {
            text = "bird needs its peers";
            file = "/run/bird/why";
          };
        };
      })).bird.hint;
    expected = {
      text = "bird needs its peers";
      file = "/run/bird/why";
    };
  };
  # An output's dir is a clean path below /run, out of chalkos's own directories but its own and
  # out of every other output's dir, since chalkd replaces whatever it holds with a link.
  testOutputDirs =
    let
      dirs =
        outputs:
        outputsOf (withBird {
          chalkos.outputs = lib.mapAttrs (_: dir: {
            render = "/nix/store/x-render/bin/render";
            inherit dir;
          }) outputs;
        });
      accepted = outputs: !fails (dirs outputs);
    in
    {
      expr = {
        systemd = accepted { a = "/run/systemd/resolved.conf.d"; };
        # The built-in output networkd's.
        networkd = accepted { a = "/run/systemd/network"; };
        own = accepted { a = "/run/chalkos/outputs/a/sub"; };
        etc = accepted { a = "/etc/bird"; };
        run = accepted { a = "/run"; };
        unclean = accepted { a = "/run/bird/"; };
        dot = accepted { a = "/run/bird/.x"; };
        chalkos = accepted { a = "/run/chalkos/config"; };
        other = accepted { a = "/run/chalkos/outputs/b"; };
        same = accepted {
          a = "/run/bird";
          b = "/run/bird";
        };
        nested = accepted {
          a = "/run/bird/sub";
          b = "/run/bird";
        };
        siblings = accepted {
          a = "/run/bird";
          b = "/run/bird2";
        };
      };
      expected = {
        systemd = true;
        networkd = false;
        own = true;
        etc = false;
        run = false;
        unclean = false;
        dot = false;
        chalkos = false;
        other = false;
        same = false;
        nested = false;
        siblings = true;
      };
    };
  # networkd's units are a built-in output in networkd's runtime directory, rendered by
  # chalkos-render with the image's networkd schemas, which networkd reloads, and the image tests
  # its examples.
  testBuiltinNetworkd = {
    expr =
      let
        output = (outputsOf withDemo).networkd;
      in
      {
        inherit (output) inputs dir check;
        render =
          builtins.match "/nix/store/[^ ]+-chalkos-render-[^ ]+/bin/chalkos-render networkd /nix/store/[^ ]+-chalkos-networkd-schemas" output.render
          != null;
        tests = lib.filter (lib.hasPrefix "networkd-") (lib.attrNames withDemo.chalkos.tests);
        # The router's examples are tests of router images alone.
        routerTests = lib.any (lib.hasPrefix "router-") (lib.attrNames withDemo.chalkos.tests);
      };
    expected = {
      inputs = [
        "Link"
        "NetDev"
        "Network"
      ];
      dir = "/run/systemd/network";
      check = "";
      render = true;
      tests = [
        "networkd-none"
        "networkd-static"
        "networkd-vlan"
      ];
      routerTests = false;
    };
  };
  # chalkos-links replays the physical ports' add events once chalkos-config has rendered the
  # Link documents and udev has handled the first events, and before networkd starts, on a node
  # with Link documents alone, and fails rather than hangs. A change of the Link documents runs
  # it again, or for the first time.
  testLinksUnit = {
    expr =
      let
        unit = withDemo.systemd.services.chalkos-links;
      in
      {
        inherit (unit) wantedBy;
        after = lib.sort lib.lessThan unit.after;
        before = lib.sort lib.lessThan unit.before;
        inherit (unit.unitConfig) DefaultDependencies ConditionPathExistsGlob;
        inherit (unit.serviceConfig) Type RemainAfterExit TimeoutStartSec;
        script = lib.hasSuffix "-chalkos-links" (toString unit.serviceConfig.ExecStart);
      };
    expected = {
      wantedBy = [ "sysinit.target" ];
      after = [
        "chalkos-config.service"
        "systemd-udev-trigger.service"
        "systemd-udevd.service"
      ];
      before = [
        "network-pre.target"
        "sysinit.target"
        "systemd-networkd.service"
      ];
      DefaultDependencies = false;
      ConditionPathExistsGlob = "/run/systemd/network/*.link";
      Type = "oneshot";
      RemainAfterExit = true;
      TimeoutStartSec = "30s";
      script = true;
    };
  };
  # Every image has the unprivileged user its renderers run as, since it renders the built-in
  # outputs.
  testRenderUser = {
    expr = withDemo.users.users.chalkos-render.isSystemUser;
    expected = true;
  };
  # An image with validators and no outputs has the user and the tmpfs too, since its validators
  # run in the renderers' sandbox below the outputs' root; one with neither has none.
  testValidatorsRunWithoutOutputs =
    let
      image =
        validators:
        withBird (
          { lib, ... }:
          {
            chalkos.outputs = lib.mkForce { };
            chalkos.validators = validators;
          }
        );
      only = image {
        v.inputs = [ "Demo" ];
        v.program = "/nix/store/x-validate/bin/validate";
      };
      neither = image { };
    in
    {
      expr = {
        user = only.users.users ? chalkos-render;
        tmpfs = only.fileSystems ? "/run/chalkos/outputs";
        neitherUser = neither.users.users ? chalkos-render;
        neitherTmpfs = neither.fileSystems ? "/run/chalkos/outputs";
      };
      expected = {
        user = true;
        tmpfs = true;
        neitherUser = false;
        neitherTmpfs = false;
      };
    };
  # The outputs' files are on a tmpfs of their own, with room for each output's files twice and
  # one more: the built-in outputs', and those of an extension.
  testOutputsTmpfs = {
    expr = {
      builtin = withDemo.fileSystems."/run/chalkos/outputs".options;
      extension =
        (withBird {
          chalkos.outputs.bird-conf.render = "/nix/store/x-render/bin/render";
          chalkos.outputs.bird-peers.render = "/nix/store/x-render/bin/render";
        }).fileSystems."/run/chalkos/outputs".options;
    };
    expected = {
      builtin = [
        "mode=0755"
        "nosuid"
        "nodev"
        "size=80M"
        "nr_inodes=81920"
      ];
      extension = [
        "mode=0755"
        "nosuid"
        "nodev"
        "size=144M"
        "nr_inodes=147456"
      ];
    };
  };
  # The rules check reads each file of the contract a node loads from /etc/chalkos, as the node
  # reads it.
  testRulesImageHoldsTheContract = {
    expr =
      let
        build = withGrant.system.build;
        inherit (build.chalkosRulesImage) entries;
      in
      {
        files = lib.attrNames entries;
        services = entries."services.json".text == withGrant.environment.etc."chalkos/services.json".text;
      };
    expected = {
      files = [
        "diagnostics.json"
        "effects.json"
        "facts.json"
        "outputs.json"
        "schemas"
        "services.json"
        "units"
        "validators.json"
      ];
      services = true;
    };
  };
  # The image's build checks that each granted unit runs something; evaluation warns of none.
  testGrantCheckedAtBuild = {
    expr =
      let
        c = worker [ { systemd.services.orphan.chalkos.kinds = [ "Network" ]; } ];
        check = lib.findFirst (
          d: lib.hasInfix "chalkos-grants-check" d.name
        ) null c.chalkos.imageDirectory.checks;
      in
      {
        inherit (c) warnings;
        checked = check != null && lib.hasInfix "orphan" check.buildCommand;
      };
    expected = {
      warnings = [ ];
      checked = true;
    };
  };
  # A granted unit falls back to a stand-in credential while chalkd wrote no directory of them.
  testGrantFallsBackWithoutCredentials = {
    expr = withGrant.systemd.services.bird.serviceConfig.SetCredential;
    expected = [ "secret:none" ];
  };
  testGrantOfUnknownKindFails = {
    expr = fails (servicesOf (grantOf [ "Nope" ]));
    expected = true;
  };
  # A unit granted no kinds reads no documents, so it gets no credentials and no chalkcfg.
  testUngrantedUnitLoadsNoCredentials = {
    expr =
      let
        config = withBird { };
      in
      {
        credentials = config.systemd.services.bird.serviceConfig ? LoadCredential;
        chalkcfg = hasChalkcfg config.systemd.services.bird.path;
        service = (servicesOf config) ? bird;
      };
    expected = {
      credentials = false;
      chalkcfg = false;
      service = false;
    };
  };
  testGrantOfSecretFails = {
    expr = fails (servicesOf (grantOf [ "Secret" ]));
    expected = true;
  };
  # Addon documents never reach /run, so no unit reads them.
  testGrantOfAddonFails = {
    expr = fails (servicesOf (grantOf [ "Addon" ]));
    expected = true;
  };
  # A withheld kind says so in its schema and its metadata.json entry, and no unit, output or
  # effect reads its documents; a kind is not withheld by default.
  testWithheldKind =
    let
      withVault =
        module:
        withBird {
          imports = [ module ];
          chalkos.kinds.Vault = {
            group = "vault.example.org";
            version = "v1";
            withheld = true;
          };
        };
      config = withVault { };
      xKind = file: config.system.build.chalkosKindSchemas.${file}."x-chalkos-kind";
    in
    {
      expr = {
        schema = xKind "vault.example.org_v1_Vault.json";
        demo = xKind "demo.example.org_v1alpha1_Demo.json" ? withheld;
        metadata = lib.filter (k: k.kind == "Vault") config.system.build.chalkosKinds;
        grant = fails (
          servicesOf (withVault {
            systemd.services.bird.chalkos.kinds = [ "Vault" ];
          })
        );
        effect = fails (
          effectsOf (withVault {
            chalkos.effects.bird = {
              kinds = [ "Vault" ];
              restart = [ "bird.service" ];
            };
          })
        );
      };
      expected = {
        schema = {
          group = "vault.example.org";
          version = "v1";
          kind = "Vault";
          multiple = false;
          withheld = true;
        };
        demo = false;
        metadata = [
          {
            apiVersion = "vault.example.org/v1";
            kind = "Vault";
            multiple = false;
            withheld = true;
          }
        ];
        grant = true;
        effect = true;
      };
    };
  testKubernetesImageDirectoryHasKubernetesFacts = {
    expr = builtins.attrNames kubernetesImage.chalkos.imageDirectory.facts;
    expected = [ "kubernetes" ];
  };
  testImageDirectoryWithoutKubernetesHasNoFacts = {
    expr = plainImage.chalkos.imageDirectory.facts;
    expected = { };
  };
  # A layer's name is a file name in facts/: lower-case letters, digits and dashes.
  testImageDirectoryRefusesFactsOfABadLayerName = {
    expr =
      map
        (
          layer:
          refuses "names the layer \"${layer}\"; a layer's name is" (worker [
            { chalkos.imageDirectory.facts.${layer} = ./kinds.nix; }
          ])
        )
        [
          "edge-2"
          ""
          "Edge"
          "a/b"
        ];
    expected = [
      false
      true
      true
      true
    ];
  };
  # The command get builds: the image's chalkcfg get with the kind, a name of metadata.name's
  # pattern, and the jsonpath of a path or an output.
  testGet = {
    expr = map getPeering [
      {
        name = "uplink";
        path = "spec.asn";
      }
      { }
      {
        name = "uplink";
        output = "json";
      }
      { name = "uplink.a-1"; }
    ];
    expected =
      map
        (args: {
          chalkcfg = true;
          inherit args;
        })
        [
          "get Peering uplink -o 'jsonpath={.spec.asn}'"
          "get Peering"
          "get Peering uplink -o json"
          "get Peering uplink.a-1"
        ];
  };
  # get refuses a name chalkcfg would not read, a path with an output, and an unknown output.
  testGetRefuses = {
    expr = map (a: fails (getPeering a)) [
      { name = "a b"; }
      { name = "Uplink"; }
      { name = "-a"; }
      { name = "a."; }
      { name = "../x"; }
      { name = ""; }
      {
        path = "spec.asn";
        output = "json";
      }
      { output = "xml"; }
    ];
    expected = lib.replicate 8 true;
  };
  # The jsonpath of each path of a Peering.
  testGetJSONPath =
    let
      paths = {
        "spec.peer.address" = "{.spec.peer.address}";
        "spec.timers.hold" = "{.spec.timers.hold}";
        "spec.neighbours.*.address" = "{.spec.neighbours[*].address}";
        "spec.neighbours.0.address" = "{.spec.neighbours[0].address}";
        "spec.labels.zone-a" = "{.spec.labels['zone-a']}";
        "spec.labels.zone" = "{.spec.labels.zone}";
        # client-go's jsonpath takes [*] for lists only; .* takes every value of an object.
        "spec.labels.*" = "{.spec.labels.*}";
        "spec.pools.*.cidr" = "{.spec.pools.*.cidr}";
        "spec.labels.zone/a" = "{.spec.labels['zone/a']}";
        "spec.labels.a:b" = "{.spec.labels['a:b']}";
        "spec.labels.0" = "{.spec.labels['0']}";
        "spec.token.secretRef.name" = "{.spec.token.secretRef.name}";
        "metadata.name" = "{.metadata.name}";
      };
    in
    {
      expr = lib.mapAttrs (path: _: resolvePeering path) paths;
      expected = lib.mapAttrs (_: jsonpath: { inherit jsonpath; }) paths;
    };
  # The error of each path of a Peering that resolves to no jsonpath.
  testGetPathErrors =
    let
      errors = {
        "spec.asnn" =
          "spec has no field asnn; its fields are asn, labels, neighbours, peer, pools, timers, token";
        status = "the document has no field status; its fields are apiVersion, kind, metadata, spec";
        "spec.peer.address.port" = "spec.peer.address holds a string and has no fields";
        "spec.neighbours.address" =
          "spec.neighbours is a list: follow it with * or an index such as 0, not address";
        "spec..asn" = "the path has an empty field name";
        # client-go's jsonpath does not quote within ['…'], so it misreads keys with a space, an @
        # or a quote.
        "spec.labels.a b" = "the key a b cannot be addressed by a jsonpath; use output with a go-template";
      };
    in
    {
      expr = lib.mapAttrs (path: _: resolvePeering path) errors;
      expected = lib.mapAttrs (_: error: { inherit error; }) errors;
    };
  # The extensions example's unit reads the node's Rack with chalkcfg, by a path the evaluation
  # checked.
  testExtensionsRackReadsWithChalkcfg = {
    expr =
      let
        c =
          (flakeLib.mkImage {
            name = "node-metal";
            platform = "metal";
            modules = [ ../../examples/extensions/rack.nix ];
          }).nixos.config;
      in
      lib.hasInfix "/bin/chalkcfg get Rack rack -o 'jsonpath={.spec.location}'" c.systemd.services.rack-location.serviceConfig.ExecStart.text;
    expected = true;
  };
  # A field's rules reach its property, also in a submodule, with takes defaulted and the
  # attributes left null left out.
  testFieldRulesReachTheSchema = {
    expr =
      let
        props =
          (specOf {
            x = chalkosLib.mkOption {
              type = types.str;
              rules = [
                {
                  rule = "self == oldSelf";
                  message = "x is fixed";
                  messageExpression = null;
                  description = "x stays what it was.";
                }
              ];
            };
            group = mkOption {
              type = types.submodule {
                options.y = chalkosLib.mkOption {
                  type = types.int;
                  rules = [
                    {
                      rule = "self >= oldSelf";
                      takes = "reset";
                    }
                  ];
                };
              };
            };
          }).properties;
      in
      [
        props.x."x-chalkos-rules"
        props.group.properties.y."x-chalkos-rules"
      ];
    expected = [
      [
        {
          rule = "self == oldSelf";
          message = "x is fixed";
          takes = "refuse";
          description = "x stays what it was.";
        }
      ]
      [
        {
          rule = "self >= oldSelf";
          takes = "reset";
        }
      ]
    ];
  };
  # The Extension API page, whose table of chalkosLib is in nix/docs/templates, lists each
  # helper of chalkosLib: a function, or a type, as the leaves of its attribute sets.
  testExtensionAPIListsEveryHelper = {
    expr =
      let
        rows = lib.filter (l: lib.hasPrefix "| `chalkosLib." (lib.trim l)) (
          lib.splitString "\n" (builtins.readFile ../../nix/docs/templates/extension-api.tail.md)
        );
      in
      lib.sort lib.lessThan (
        map (l: lib.head (builtins.match "[|] `chalkosLib[.]([A-Za-z.]+)`.*" (lib.trim l))) rows
      );
    expected =
      let
        leaves =
          prefix: set:
          lib.concatLists (
            lib.mapAttrsToList (
              name: v:
              if lib.isAttrs v && !lib.isType "option-type" v then
                leaves "${prefix}${name}." v
              else
                [ "${prefix}${name}" ]
            ) set
          );
      in
      # chalkosLib.internal is chalkos's own, which the page does not list.
      lib.sort lib.lessThan (leaves "" (removeAttrs chalkosLib [ "internal" ]));
  };
  # An option without rules is lib.mkOption's.
  testMkOptionWithoutRules = {
    expr = chalkosLib.mkOption { type = types.str; } == mkOption { type = types.str; };
    expected = true;
  };
  # A kind's rules reach x-chalkos-kind and its specRules its spec's x-chalkos-rules, as a field's
  # rules reach the field, each without the other's; a kind without them has neither.
  testKindAndSpecRulesReachTheSchema = {
    expr =
      let
        schemas =
          (worker [
            demoModule
            {
              chalkos.kinds.Demo = {
                rules = [
                  {
                    rule = "size(documents) <= 1";
                    message = "one Demo";
                    description = "A node holds one Demo at most.";
                  }
                  {
                    rule = "documents == oldDocuments";
                    messageExpression = "\"keep \" + oldDocuments.map(n, n).join(\", \")";
                    takes = "reinstall";
                  }
                ];
                specRules = [
                  {
                    rule = "self.token == null || self.token == oldSelf.token";
                    message = "the token stays";
                    description = "A Demo keeps its token.";
                  }
                ];
              };
            }
          ]).system.build.chalkosKindSchemas;
        demo = schemas."demo.example.org_v1alpha1_Demo.json";
        host = schemas."chalkos.dev_v1alpha1_Host.json";
      in
      {
        kind = demo."x-chalkos-kind".rules;
        spec = demo.properties.spec."x-chalkos-rules";
        hostKind = host."x-chalkos-kind" ? rules;
        hostSpec = host.properties.spec ? "x-chalkos-rules";
      };
    expected = {
      kind = [
        {
          rule = "size(documents) <= 1";
          message = "one Demo";
          takes = "refuse";
          description = "A node holds one Demo at most.";
        }
        {
          rule = "documents == oldDocuments";
          messageExpression = "\"keep \" + oldDocuments.map(n, n).join(\", \")";
          takes = "reinstall";
        }
      ];
      spec = [
        {
          rule = "self.token == null || self.token == oldSelf.token";
          message = "the token stays";
          takes = "refuse";
          description = "A Demo keeps its token.";
        }
      ];
      hostKind = false;
      hostSpec = false;
    };
  };
  # The image's facts reach /etc/chalkos/facts.json: a file with the fields it keeps, or a command
  # with its inputs.
  testFactsReachTheImage = {
    expr =
      let
        config = worker [
          {
            chalkos.facts = {
              seen = {
                file = "/var/lib/seen.json";
                fields = [ "at" ];
              };
              probe = {
                command = "/nix/store/x-probe/bin/probe facts";
                inputs = [ "fallbackSecret" ];
              };
            };
          }
        ];
        defined = config.system.build.chalkosFactDefinitions;
      in
      {
        inherit (defined) seen probe;
        etc = config.environment.etc."chalkos/facts.json".source == config.system.build.chalkosFacts;
      };
    expected = {
      seen = {
        file = "/var/lib/seen.json";
        fields = [ "at" ];
      };
      probe = {
        command = "/nix/store/x-probe/bin/probe facts";
        inputs = [ "fallbackSecret" ];
      };
      etc = true;
    };
  };
  # Every image declares the facts of the storage kinds' rules, and an image with Kubernetes
  # the share, of which it keeps the kind and the hashes of the other keys, and the cluster file
  # the kubelet's restart rules read; the fallback secret reaches the command that checks it alone.
  testBuiltinFacts = {
    expr =
      let
        defined = kubernetesImage.system.build.chalkosFactDefinitions;
      in
      {
        names = lib.attrNames defined;
        plain = lib.attrNames plainImage.system.build.chalkosFactDefinitions;
        inherit (defined) recorded share cluster;
        disks = lib.hasSuffix " disks" defined.disks.command && !(defined.disks ? inputs);
        fallback =
          lib.hasSuffix " fallback" defined.fallback.command
          && defined.fallback.inputs == [ "fallbackSecret" ];
      };
    expected = {
      names = [
        "cluster"
        "disks"
        "fallback"
        "recorded"
        "share"
      ];
      plain = [
        "disks"
        "fallback"
        "recorded"
      ];
      recorded.file = "/state/storage/storage.json";
      share = {
        file = "/state/kubernetes/share.json";
        fields = [ "kind" ];
        hashOthers = true;
      };
      cluster.file = "/run/chalkos/kubernetes/cluster.json";
      disks = true;
      fallback = true;
    };
  };
  # A fact fails the build when its name is no identifier, when it has both a file and a command or
  # neither, when a file fact has inputs, and when a command fact has fields or hashes others.
  testFactsRefused = {
    expr =
      lib.mapAttrs
        (_: facts: fails (worker [ { chalkos = { inherit facts; }; } ]).system.build.chalkosFacts.drvPath)
        {
          accepted.x.file = "/x";
          name.disk-ids.file = "/x";
          fileAndCommand.x = {
            file = "/x";
            command = "/bin/x";
          };
          noSource.x = { };
          fileWithInputs.x = {
            file = "/x";
            inputs = [ "fallbackSecret" ];
          };
          commandWithFields.x = {
            command = "/bin/x";
            fields = [ "kind" ];
          };
          commandHashes.x = {
            command = "/bin/x";
            hashOthers = true;
          };
        };
    expected = {
      accepted = false;
      name = true;
      fileAndCommand = true;
      noSource = true;
      fileWithInputs = true;
      commandWithFields = true;
      commandHashes = true;
    };
  };
  # A test's file, which chalkos-rules test reads: each test with its expectations, the files it
  # names as their store paths and what it leaves unset at the defaults.
  testTestsJSON = {
    expr =
      let
        c = worker [
          {
            chalkos.tests.demo = {
              config = ../../modules/node/examples/networkd/none.yaml;
              refusals = [ { message = "no"; } ];
            };
          }
        ];
      in
      (builtins.fromJSON (builtins.unsafeDiscardStringContext c.system.build.chalkosTestsJSON.text))
      .tests.demo;
    expected = {
      config = "${../../modules/node/examples/networkd/none.yaml}";
      before = null;
      node = "example";
      facts = { };
      oldFacts = { };
      outputs = { };
      refusals = [
        {
          kind = "";
          name = "";
          path = "";
          message = "no";
        }
      ];
      effects = null;
    };
  };
  # A test refuses, at evaluation, effects without before, an output, an effect or a fact the
  # image lacks, and a name that is no file name of its own in the runner's results.
  testTestProblemsFail =
    let
      config = ../../modules/node/examples/networkd/none.yaml;
      refused =
        test:
        fails
          (worker [ { chalkos.tests.${test.name or "demo"} = removeAttrs test [ "name" ]; } ])
          .system.build.chalkosTestsJSON.text;
    in
    {
      expr = {
        accepted = refused { inherit config; };
        effectsWithoutBefore = refused {
          inherit config;
          effects = [ ];
        };
        unknownOutput = refused {
          inherit config;
          outputs.missing = ../../modules/node/examples/networkd;
        };
        unknownEffect = refused {
          inherit config;
          before = config;
          effects = [ "missing" ];
        };
        unknownFact = refused {
          inherit config;
          facts.missing = true;
        };
        unknownOldFact = refused {
          inherit config;
          oldFacts.missing = true;
        };
        reservedName = refused {
          inherit config;
          name = "summary.txt";
        };
        pathName = refused {
          inherit config;
          name = "a/b";
        };
        dotName = refused {
          inherit config;
          name = "..";
        };
        controlName = refused {
          inherit config;
          name = "a\tb";
        };
      };
      expected = {
        accepted = false;
        effectsWithoutBefore = true;
        unknownOutput = true;
        unknownEffect = true;
        unknownFact = true;
        unknownOldFact = true;
        reservedName = true;
        pathName = true;
        dotName = true;
        controlName = true;
      };
    };
  # The image directory's build runs the tests only with chalkos.testsOnBuild, which leaves the
  # system, and so the image's store and root hash, as it is.
  testTestsOnBuild = {
    expr =
      let
        off = kubernetesImage;
        on = worker [ { chalkos.testsOnBuild = true; } ];
        tested = c: lib.elem "chalkos-tests" (map (d: d.name) c.chalkos.imageDirectory.checks);
      in
      {
        off = tested off;
        on = tested on;
        sameSystem = off.system.build.toplevel.drvPath == on.system.build.toplevel.drvPath;
      };
    expected = {
      off = false;
      on = true;
      sameSystem = true;
    };
  };
  # The image's rules, grants and kernel modules checks fail the builds of the image directory and
  # of the installer, and stay out of the system's derivation, so a check changes no root hash.
  testBuildChecksOutsideTheSystem = {
    expr =
      let
        images = flakeLib.mkImages {
          images = [
            {
              name = "worker-metal";
              platform = "metal";
              modules = [ layers.kubernetes ];
            }
          ];
        };
        c = images.worker-metal.nixos.config;
        names = ds: map (d: d.name) ds;
      in
      {
        image = lib.sort lib.lessThan (names c.chalkos.imageDirectory.checks);
        # NixOS's own checks stay; none of the image's are among them.
        system = lib.filter (n: lib.elem n (names c.chalkos.imageDirectory.checks)) (names c.system.checks);
        imageDepends = images.worker-metal.checks == c.chalkos.imageDirectory.checks;
        installerDepends =
          images.installer.checks == images.installer.nixos.config.chalkos.imageDirectory.checks;
      };
    expected = {
      image = [
        "chalkos-grants-check"
        "chalkos-rules-check"
        "kernel-modules-check"
      ];
      system = [ ];
      imageDepends = true;
      installerDepends = true;
    };
  };
  # exampleTests names a test by its file without the extension, skips files of other types, and
  # refuses two files that give one name.
  testExampleTestsNames = {
    expr = {
      tests = lib.mapAttrs (_: t: baseNameOf t.config) (
        chalkosLib.internal.exampleTests "demo" ./fixtures/example-tests
      );
      clash = fails (chalkosLib.internal.exampleTests "demo" ./fixtures/example-tests-clash);
    };
    expected = {
      tests = {
        demo-one = "one.yaml";
        "demo-two.v1" = "two.v1.json";
      };
      clash = true;
    };
  };
}
