# Evaluation tests of config kinds and their schemas, outputs, effects, facts and validators.
# Returns the tests in the form of lib.runTests, which the eval-kinds check runs.
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
    "TimeServer"
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
  # The kinds an image accepts, by the x-chalkos-kind of each schema, networkd's among them.
  kindsOf =
    config:
    map (s: s."x-chalkos-kind") (lib.attrValues config.system.build.chalkosKindSchemas)
    ++ config.system.build.chalkosNetworkdKinds;
  kindNames = config: map (k: k.kind) (kindsOf config);
  builtinFixture =
    file: builtins.fromJSON (builtins.readFile (../fixtures/kinds/builtin + "/${file}"));
  # The paths of the fields a kind's schema s marks with x-chalkos-fixed, in name order: the walk
  # descends into properties and into the branches of an anyOf that have properties, and stops at
  # a marked node, a nullable field's anyOf among them, and at list items and map values.
  fixedPathsOf =
    prefix: s:
    if s ? "x-chalkos-fixed" then
      [ (lib.removeSuffix "." prefix) ]
    else
      lib.concatLists (
        lib.mapAttrsToList (name: fixedPathsOf "${prefix}${name}.") (s.properties or { })
        ++ map (fixedPathsOf prefix) (lib.filter (b: b ? properties) (s.anyOf or [ ]))
      );
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
        state
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
  # The schema of a kind T of the state given, null or { fact; description; }, whose spec has the
  # option declarations given.
  stateKindOf =
    state: spec:
    schema.kindSchema {
      kind = "T";
      group = "t.example.org";
      version = "v1";
      multiple = false;
      inherit state spec;
    };
  # The state of a node that holds a Kubernetes share, as the Kubernetes layer names it.
  shareState = {
    fact = "share";
    description = "the node holds a Kubernetes share";
  };
  # The rule fixed = "node" generates for the field x, with messageExpression when given.
  fixedNodeRuleOf =
    messageExpression:
    {
      description = "The field x stays once the node holds a Kubernetes share.";
      rule = "facts.share == null || self == oldSelf";
      message = "the config changes x; it is fixed once the node holds a Kubernetes share, so keep it, or reinstall the node";
      takes = "refuse";
    }
    // lib.optionalAttrs (messageExpression != null) { inherit messageExpression; };
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
{
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
  # fixed = "node" generates the rule that keeps the field once the node holds its kind's state,
  # with the old and new values in the message of a string or a list of strings.
  testFixedNodeRule = {
    expr =
      map
        (
          type:
          (stateKindOf shareState {
            x = chalkosLib.mkOption {
              inherit type;
              fixed = "node";
            };
          }).properties.spec.properties.x."x-chalkos-rules"
        )
        [
          types.str
          (types.listOf types.str)
          types.int
          # The messageExpression fails on null, and chalkd shows the static message instead.
          (types.nullOr types.str)
          (types.listOf (
            types.enum [
              "IPv4"
              "IPv6"
            ]
          ))
        ];
    expected =
      let
        str = fixedNodeRuleOf ''"the config changes x from " + oldSelf + " to " + self + "; it is fixed once the node holds a Kubernetes share, so keep it, or reinstall the node"'';
        list = fixedNodeRuleOf ''"the config changes x from " + oldSelf.join(", ") + " to " + self.join(", ") + "; it is fixed once the node holds a Kubernetes share, so keep it, or reinstall the node"'';
      in
      [
        [ str ]
        [ list ]
        [ (fixedNodeRuleOf null) ]
        [ str ]
        [ list ]
      ];
  };
  # The generated rule follows the field's own rules and names a nested field by its path below
  # the spec.
  testFixedNodeRuleFollowsFieldRules = {
    expr =
      map (r: r.description or null)
        (stateKindOf shareState {
          net.x = chalkosLib.mkOption {
            type = types.int;
            fixed = "node";
            rules = [
              {
                rule = "self > 0";
                message = "x is positive";
              }
            ];
          };
        }).properties.spec.properties.net.properties.x."x-chalkos-rules";
    expected = [
      null
      "The field net.x stays once the node holds a Kubernetes share."
    ];
  };
  # fixed = "cluster" marks the field itself with x-chalkos-fixed, and nothing else: it adds no
  # rule, the groups above the field stay unmarked, and so does an unmarked field.
  testFixedCluster = {
    expr =
      let
        s = stateKindOf null {
          net.domain = chalkosLib.mkOption {
            type = types.str;
            fixed = "cluster";
          };
          free = mkOption { type = types.str; };
        };
        spec = s.properties.spec;
        marked = v: lib.any (k: lib.hasPrefix "x-chalkos-fixed" k) (lib.attrNames v);
      in
      {
        root = marked s;
        spec = marked spec;
        net = marked spec.properties.net;
        domain = spec.properties.net.properties.domain."x-chalkos-fixed" or null;
        rules = spec.properties.net.properties.domain ? "x-chalkos-rules";
        free = marked spec.properties.free;
      };
    expected = {
      root = false;
      spec = false;
      net = false;
      domain = "cluster";
      rules = false;
      free = false;
    };
  };
  # Both marks together: the rule and x-chalkos-fixed.
  testFixedBoth = {
    expr =
      let
        x =
          (stateKindOf shareState {
            x = chalkosLib.mkOption {
              type = types.int;
              fixed = [
                "node"
                "cluster"
              ];
            };
          }).properties.spec.properties.x;
      in
      {
        fixed = x."x-chalkos-fixed";
        rules = x."x-chalkos-rules";
      };
    expected = {
      fixed = "cluster";
      rules = [ (fixedNodeRuleOf null) ];
    };
  };
  # A marked field inside a nullable group carries x-chalkos-fixed on its own node, below the
  # group's anyOf, and a nullable marked field on the node that holds the anyOf. The rule of
  # fixed = "node" lands below the anyOf too, and names the field by its path, x.y.
  testFixedInsideANullableGroup = {
    expr =
      let
        spec =
          (stateKindOf shareState {
            x = mkOption {
              type = types.nullOr (
                types.submodule {
                  options.y = chalkosLib.mkOption {
                    type = types.str;
                    fixed = [
                      "node"
                      "cluster"
                    ];
                  };
                }
              );
              default = null;
            };
            z = chalkosLib.mkOption {
              type = types.nullOr types.str;
              default = null;
              fixed = "cluster";
            };
          }).properties.spec;
      in
      {
        outer = spec.properties.x ? "x-chalkos-fixed";
        y = (lib.head spec.properties.x.anyOf).properties.y."x-chalkos-fixed" or null;
        rules = map (r: { inherit (r) description message; }) (
          (lib.head spec.properties.x.anyOf).properties.y."x-chalkos-rules" or [ ]
        );
        z = spec.properties.z."x-chalkos-fixed" or null;
      };
    expected = {
      outer = false;
      y = "cluster";
      rules = [
        {
          description = "The field x.y stays once the node holds a Kubernetes share.";
          message = "the config changes x.y; it is fixed once the node holds a Kubernetes share, so keep it, or reinstall the node";
        }
      ];
      z = "cluster";
    };
  };
  # fixed = "node" without the kind's state, a value that is none of node and cluster, an empty
  # list, and a mark inside a list's items or a map's values fail the evaluation, each by its own
  # refusal.
  testFixedRefusals =
    let
      none = ''is none of "node", "cluster" and [ "node" "cluster" ]'';
      item = "a field of a list's items or a map's values is compared as part of the list or map; mark the list or the map itself";
      # A field y in the items of the list or map of the type given.
      inItems =
        of: fixed:
        of (
          types.submodule {
            options.y = chalkosLib.mkOption {
              type = types.str;
              inherit fixed;
            };
          }
        );
      cases = [
        {
          state = null;
          fixed = "node";
          refusal = ''x.fixed: "node" keeps the field once the node holds its layer's state, and chalkos.kinds.T.state names none; set it to the fact that is null until then'';
        }
        {
          fixed = "kind";
          refusal = ''x.fixed: "kind" ${none}'';
        }
        {
          fixed = [ ];
          refusal = "x.fixed: [] ${none}";
        }
        {
          fixed = "cluster";
          type = inItems types.listOf;
          inItem = true;
          refusal = "x.fixed: ${item}";
        }
        {
          fixed = "node";
          type = inItems types.attrsOf;
          inItem = true;
          refusal = "x.fixed: ${item}";
        }
      ];
      stateOf = x: x.state or shareState;
    in
    {
      expr = map (x: {
        fails = fails (
          stateKindOf (stateOf x) {
            x =
              if x ? type then
                mkOption { type = x.type x.fixed; }
              else
                chalkosLib.mkOption {
                  type = types.str;
                  inherit (x) fixed;
                };
          }
        );
        refusal = schema.fixedRefusal {
          kind = "T";
          state = stateOf x;
          inItem = x.inItem or false;
        } "x.fixed" x.fixed;
      }) cases;
      expected = map (x: {
        fails = true;
        inherit (x) refusal;
      }) cases;
    };
  # A mark in one branch of a types.either fails the evaluation, since kinds accept no either:
  # sdk.FixedChanges could not follow a value written as another branch. Should the converter
  # learn types.either, fixedRefusal must refuse such a mark.
  testFixedBelowEitherFails = {
    expr = fails (
      stateKindOf shareState {
        x = mkOption {
          type =
            let
              group = types.submodule {
                options.y = chalkosLib.mkOption {
                  type = types.str;
                  fixed = "cluster";
                };
              };
            in
            types.either group (types.listOf group);
        };
      }
    );
    expected = true;
  };
  # A field's rules come raw from chalkosLib.mkOption: an attribute a rule does not have, a takes
  # that is none, and a missing or empty rule fail the evaluation with the rule's option path, as
  # the typed rules of a kind do.
  testFieldRuleRefusals =
    let
      at = "chalkos.kinds.T.spec.x.rules[0]";
      cases = [
        {
          rule = {
            rule = "self == oldSelf";
            mesage = "x";
          };
          refusal = "${at}: mesage is none of rule, message, messageExpression, takes, reason and description";
        }
        {
          rule = {
            rule = "self == oldSelf";
            takes = "rest";
          };
          refusal = "${at}.takes: \"rest\" is none of refuse, reset and reinstall";
        }
        {
          rule.message = "x";
          refusal = "${at} has no rule; set rule to the CEL expression that is true for every change the node accepts";
        }
      ];
    in
    {
      expr = map (x: {
        fails = fails (specOf {
          x = chalkosLib.mkOption {
            type = types.str;
            rules = [ x.rule ];
          };
        });
        refusal = schema.ruleRefusal at x.rule;
      }) cases;
      expected = map (x: {
        fails = true;
        inherit (x) refusal;
      }) cases;
    };
  # A type where a field's option belongs, a common slip for lib.mkOption { type = ...; }, fails
  # the evaluation and says so, at the top of the spec and within a group.
  testBareTypeRefused = {
    expr = {
      top = fails (specOf {
        asn = types.int;
      });
      nested = fails (specOf {
        peer.asn = types.int;
      });
      refusal = schema.groupRefusal "chalkos.kinds.T.spec.asn" types.int;
      group = schema.groupRefusal "chalkos.kinds.T.spec.peer" { asn = mkOption { type = types.int; }; };
    };
    expected = {
      top = true;
      nested = true;
      refusal = "chalkos.kinds.T.spec.asn is a type; declare the field as lib.mkOption { type = ...; }";
      group = null;
    };
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
  # The values Nix shares with Go come from pkg/constants/constants.json, which Go embeds: the API
  # groups, networkd's kinds, the patterns of names, the normalisers and the rules' reasons.
  testSharedConstants = {
    expr = {
      inherit (chalkosLib.internal.api)
        version
        groups
        networkdKinds
        normalisers
        ruleReasons
        ;
      inherit (kindTypes) documentName;
      inherit (chalkosLib.internal) plainName;
    };
    expected = builtins.fromJSON (builtins.readFile ../../pkg/constants/constants.json);
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
    expr = lib.any (k: k.group == "demo.example.org" && k.kind == "Demo" && k.multiple) (
      kindsOf withDemo
    );
    expected = true;
  };
  testNetworkdKindsListed = {
    expr = map (k: { inherit (k) group kind multiple; }) (
      lib.filter (k: k.group == "networkd.chalkos.dev") (kindsOf withDemo)
    );
    expected =
      map
        (kind: {
          group = "networkd.chalkos.dev";
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
      "ClusterRegistry"
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
  # The cluster domain is a lower-case DNS name without the root's dot: CoreDNS's Corefile, the
  # service account issuer and the API server's certificate take it as written.
  testClusterDomain = {
    expr = lib.genAttrs [
      "cluster.local"
      "k8s.example.org"
      "Cluster.local"
      "cluster.local."
      "a b"
      "a}"
    ] kubernetesImage.chalkos.kinds.Cluster.spec.domain.type.check;
    expected = {
      "cluster.local" = true;
      "k8s.example.org" = true;
      "Cluster.local" = false;
      "cluster.local." = false;
      "a b" = false;
      "a}" = false;
    };
  };
  # Every node of a Kubernetes cluster holds its endpoint, domain, families and ranges alike; the
  # images, the components' flags and the nodes' parts of the pod ranges may differ.
  testClusterFixedFields = {
    expr = lib.unique (fixedPathsOf "" (builtinSchema "Cluster"));
    expected = [
      "spec.dnsIPs.ipv4"
      "spec.dnsIPs.ipv6"
      "spec.domain"
      "spec.endpoint"
      "spec.ipFamilies"
      "spec.podCIDRs.ipv4"
      "spec.podCIDRs.ipv6"
      "spec.serviceCIDRs.ipv4"
      "spec.serviceCIDRs.ipv6"
    ];
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
          run = "birdc  configure";
          chalkd = "kubernetes:reload";
        };
      })).bird;
    expected = {
      outputs = [ ];
      kinds = [ ];
      when = "facts.share != oldFacts.share";
      restart = [ "bird.service" ];
      reload = [ ];
      run = [
        "birdc"
        "configure"
      ];
      start = true;
      chalkd = "kubernetes:reload";
      hint = null;
    };
  };
  # An output reaches chalkd with its render's and its check's commands split at white space.
  testOutputsReachChalkd =
    let
      config = withBird {
        chalkos.outputs.bird-conf = {
          inputs = [ "Demo" ];
          render = "/nix/store/x-render/bin/render";
          check = "/nix/store/x-bird/bin/bird -p -c";
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
        inherit (output) check;
        outputs = (effectsOf config).bird.outputs;
        dirOption = config.chalkos.outputs.bird-conf.dir;
      };
      expected = {
        inputs = [ "Demo" ];
        render = [ "/nix/store/x-render/bin/render" ];
        dir = "/run/chalkos/outputs/bird-conf";
        user = "bird";
        group = "root";
        mode = "0644";
        check = [
          "/nix/store/x-bird/bin/bird"
          "-p"
          "-c"
        ];
        outputs = [ "bird-conf" ];
        dirOption = "/run/chalkos/outputs/bird-conf";
      };
    };
  # A check is a program: a command split at white space, a package as its path, or chalkos-render's
  # run of the program and arguments of chalkosLib.check.run or .each with $OUT and $FILE left for it
  # to replace, each argument as it is, white space and all. A check of another form, or without
  # a program, fails the evaluation.
  testChecks =
    let
      outputs =
        checks:
        withBird (
          { chalkosLib, pkgs, ... }:
          {
            chalkos.outputs = lib.mapAttrs (_: check: {
              render = "/nix/store/x-render/bin/render";
              check = check chalkosLib pkgs;
            }) checks;
          }
        );
      config = outputs {
        none = _: _: null;
        package = _: pkgs: pkgs.writeShellScript "c" "true";
        run =
          chalkosLib: _:
          chalkosLib.check.run [
            "/nix/store/x-nft/bin/nft"
            "--check"
            "--file"
            "$OUT/ruleset.nft"
          ];
        each =
          chalkosLib: _:
          chalkosLib.check.each "*.conf" [
            "/nix/store/x-dnsmasq/bin/dnsmasq"
            "--conf-file=$FILE"
          ];
      };
      check = name: lib.concatStringsSep " " (outputsOf config).${name}.check;
      refused =
        check:
        fails (
          outputsOf (outputs {
            x = check;
          })
        );
      render = builtins.match "/nix/store/[^ ]+-chalkos-render-[^ ]+/bin/chalkos-render (.*)";
    in
    {
      expr = {
        none = check "none";
        package = lib.hasPrefix "/nix/store/" (check "package");
        run = render (check "run");
        each = render (check "each");
        value = chalkosLib.check.each "*.conf" [ "x" ];
        spaced =
          lib.tail
            (outputsOf (outputs {
              x =
                chalkosLib: _:
                chalkosLib.check.each "a b" [
                  "/nix/store/x-a/bin/a"
                  "b c"
                ];
            })).x.check;
        empty = refused (chalkosLib: _: chalkosLib.check.run [ ]);
        other = refused (_: _: { script = "true"; });
      };
      expected = {
        none = "";
        package = true;
        run = [ "check run /nix/store/x-nft/bin/nft --check --file $OUT/ruleset.nft" ];
        each = [ "check each *.conf /nix/store/x-dnsmasq/bin/dnsmasq --conf-file=$FILE" ];
        value = {
          each = "*.conf";
          run = [ "x" ];
        };
        spaced = [
          "check"
          "each"
          "a b"
          "/nix/store/x-a/bin/a"
          "b c"
        ];
        empty = true;
        other = true;
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
            d = {
              inputs = [
                "Demo"
                "Network"
              ];
              render = chalkosLib.render.documents [
                "Demo"
                "Network"
              ];
            };
            j.render = chalkosLib.render.jq (builtins.toFile "bird.conf.jq" ".");
            p.render = pkgs.writeShellScript "r" "true";
          };
        }
      );
      render = name: lib.concatStringsSep " " (outputsOf config).${name}.render;
    in
    {
      expr = {
        value = chalkosLib.render.documents [ "Peer" ];
        template = builtins.match ".*/bin/chalkos-render template /nix/store/[^ ]*-greeting.tmpl (.*)" (
          render "t"
        );
        documents = builtins.match ".*/bin/chalkos-render (documents Demo Network)" (render "d");
        jq = builtins.match ".*/bin/chalkos-render jq /nix/store/[^ ]+/bin/jq /nix/store/[^ ]+-bird.conf.jq (bird.conf)" (
          render "j"
        );
        package = lib.hasPrefix "/nix/store/" (render "p");
      };
      expected = {
        value.documents = [ "Peer" ];
        template = [ "greeting" ];
        documents = [ "documents Demo Network" ];
        jq = [ "bird.conf" ];
        package = true;
      };
    };
  # A validator reaches chalkd with its input kinds and its program's argv: a command split at white
  # space, a package as its path, and a jq program of chalkosLib.validate as chalkos-render's run
  # of it.
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
        package = lib.hasPrefix "/nix/store/" (lib.head validators.p.program);
        jq = builtins.match ".*/bin/chalkos-render validate-jq /nix/store/[^ ]+/bin/jq /nix/store/[^ ]+-(demo.jq)" (
          lib.concatStringsSep " " validators.j.program
        );
        value = chalkosLib.validate.jq "x.jq";
      };
      expected = {
        c = {
          inputs = [ "Demo" ];
          program = [
            "/nix/store/x-validate/bin/validate"
            "--strict"
          ];
        };
        inputs = [
          "Demo"
          "Network"
        ];
        package = true;
        jq = [ "demo.jq" ];
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
            documents.bird-conf = {
              inputs = [ "Demo" ];
              render.documents = [ "Demo" ];
            };
            documentsNotInInputs.bird-conf = {
              inputs = [ "Demo" ];
              render.documents = [
                "Demo"
                "Network"
              ];
            };
            documentsNone.bird-conf = {
              inputs = [ "Demo" ];
              render.documents = [ ];
            };
          };
      expected = {
        accepted = false;
        renderer = true;
        twoRenderers = true;
        unknownKind = true;
        secret = true;
        name = true;
        documents = false;
        documentsNotInInputs = true;
        documentsNone = true;
      };
    };
  # An effect's chalkd action, or a run of chalkd whose subcommand names a layer of chalkos, names
  # a layer of the image; another layer fails. chalkd's own subcommands and a layer's command
  # without the layer's name name no layer.
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
          { run = "/nix/store/x-chalkd/bin/chalkd vxlan-rule"; }
          {
            run = [
              "/nix/store/x-chalkd/bin/chalkd"
              "router"
              "x"
            ];
          }
        ];
    expected = [
      true
      true
      false
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
  # An output's dir is a clean path below /run, out of the directories systemd, NixOS and chalkd
  # keep their state in but networkd's, out of chalkos's own directories but its own and out of
  # every other output's dir, since chalkd replaces what it holds with a link.
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
        chalkd = accepted { a = "/run/chalkd"; };
        credentials = accepted { a = "/run/credentials/bird.service"; };
        currentSystem = accepted { a = "/run/current-system"; };
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
        systemd = false;
        chalkd = false;
        credentials = false;
        currentSystem = false;
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
          builtins.match "/nix/store/[^ ]+-chalkos-render-[^ ]+/bin/chalkos-render networkd /nix/store/[^ ]+-chalkos-networkd-schemas" (
            lib.concatStringsSep " " output.render
          ) != null;
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
      check = [ ];
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
        script = lib.hasSuffix "/bin/chalkd apply-links" unit.serviceConfig.ExecStart;
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
  # The outputs' files are on a noexec tmpfs of their own, with room for each output's files twice and
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
        "noexec"
        "size=176M"
        "nr_inodes=180224"
      ];
      extension = [
        "mode=0755"
        "nosuid"
        "nodev"
        "noexec"
        "size=240M"
        "nr_inodes=245760"
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
      lib.hasInfix "/bin/chalkcfg get Rack rack -o 'jsonpath={.spec.location}'" c.systemd.services.rack-location.serviceConfig.ExecStart;
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
  # helper of chalkosLib: a function, or a type, as the leaves of its attribute sets, and each
  # sandbox.
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
              # A sandbox is a value: the settings of a unit's serviceConfig.
              if lib.isAttrs v && !lib.isType "option-type" v && prefix != "sandbox." then
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
  # with its inputs, each argument of a list as it is.
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
                command = [
                  "/nix/store/x-probe/bin/probe"
                  "facts $NODE"
                ];
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
        command = [
          "/nix/store/x-probe/bin/probe"
          "facts $NODE"
        ];
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
        disks = lib.last defined.disks.command == "disks" && !(defined.disks ? inputs);
        fallback =
          lib.last defined.fallback.command == "fallback" && defined.fallback.inputs == [ "fallbackSecret" ];
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
