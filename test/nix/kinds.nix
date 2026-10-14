# Evaluation tests for config kinds: the schema converter and the chalkos.kinds option of images.
# Returns lib.runTests failures, empty on success.
{ lib, mkCluster }:
let
  inherit (lib) mkOption types;
  schema = import ../../modules/node/kinds/schema.nix { inherit lib; };
  fails = value: !(builtins.tryEval (builtins.deepSeq value true)).success;

  # The worker image of a one-role cluster whose role carries the given NixOS modules.
  image =
    modules:
    (mkCluster {
      modules = [
        {
          chalkos.cluster = {
            name = "t";
            endpoint = "https://10.0.0.1:6443";
          };
          chalkos.roles.worker.nixosModules = modules;
        }
      ];
    }).roles.worker.nixos.metal.config;

  demoModule =
    { chalkosLib, ... }:
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
  withDemo = image [ demoModule ];
  # Forces the schemas an image ships, and with them the checks of its kinds' names.
  schemasOf = config: config.system.build.chalkosSchemas.drvPath;

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
  # The schema of a single option of the given type.
  propOf = type: (specOf { x = mkOption { inherit type; }; }).properties.x;

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
        type = types.nullOr schema.types.secretRef;
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
  testDemoSchemaMatchesFixture = {
    expr = schema.kindSchema demo;
    expected = builtins.fromJSON (
      builtins.readFile ../fixtures/kinds/image/schemas/demo.example.org_v1alpha1_Demo.json
    );
  };
  testFileName = {
    expr = schema.fileName demo;
    expected = "demo.example.org_v1alpha1_Demo.json";
  };
  testStrMatchingIsAnchored = {
    expr = propOf (types.strMatching "[a-z]+");
    expected = {
      type = "string";
      pattern = "^(?:[a-z]+)$";
    };
  };
  testBool = {
    expr = propOf types.bool;
    expected.type = "boolean";
  };
  testInt = {
    expr = propOf types.int;
    expected.type = "integer";
  };
  testIntsBetween = {
    expr = propOf (types.ints.between (-5) 5);
    expected = {
      type = "integer";
      minimum = -5;
      maximum = 5;
    };
  };
  testPort = {
    expr = propOf types.port;
    expected = {
      type = "integer";
      minimum = 0;
      maximum = 65535;
    };
  };
  testPositive = {
    expr = propOf types.ints.positive;
    expected = {
      type = "integer";
      minimum = 1;
    };
  };
  testFloat = {
    expr = propOf types.float;
    expected.type = "number";
  };
  testAttrsOf = {
    expr = propOf (types.attrsOf types.int);
    expected = {
      type = "object";
      additionalProperties.type = "integer";
    };
  };
  testLazyAttrsOf = {
    expr = propOf (types.lazyAttrsOf types.int);
    expected = {
      type = "object";
      additionalProperties.type = "integer";
    };
  };
  testIntsUnsigned = {
    expr = propOf types.ints.unsigned;
    expected = {
      type = "integer";
      minimum = 0;
    };
  };
  testIntsS8 = {
    expr = propOf types.ints.s8;
    expected = {
      type = "integer";
      minimum = -128;
      maximum = 127;
    };
  };
  testNullOrStr = {
    expr = propOf (types.nullOr types.str);
    expected.anyOf = [
      { type = "string"; }
      { type = "null"; }
    ];
  };
  testNonEmptyListOf = {
    expr = propOf (types.nonEmptyListOf types.str);
    expected = {
      type = "array";
      items.type = "string";
      minItems = 1;
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
  # attrsOf and lazyAttrsOf instantiate their submodule once to read its options, before any
  # attribute name is known, so a default computed from the submodule's name argument is the
  # module system's placeholder rather than a real value and must not reach the schema.
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
  testKindMultipleDefaultsToFalse = {
    expr =
      (image [
        {
          chalkos.kinds.Single = {
            group = "demo.example.org";
            version = "v1";
          };
        }
      ]).chalkos.kinds.Single.multiple;
    expected = false;
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
  testNetworkdKindNameIsTaken = {
    expr = fails (
      schemasOf (image [
        {
          chalkos.kinds.Network = {
            group = "demo.example.org";
            version = "v1";
          };
        }
      ])
    );
    expected = true;
  };
  testNetworkdGroupIsReserved = {
    expr = fails (
      schemasOf (image [
        {
          chalkos.kinds.Bridge = {
            group = "networkd.chalkos.dev";
            version = "v1alpha1";
          };
        }
      ])
    );
    expected = true;
  };
  testLowerCaseKindFails = {
    expr = fails (
      schemasOf (image [
        {
          chalkos.kinds.demo = {
            group = "demo.example.org";
            version = "v1";
          };
        }
      ])
    );
    expected = true;
  };
  testInvalidGroupFails = {
    expr =
      fails
        (image [
          {
            chalkos.kinds.Demo = {
              group = "Demo";
              version = "v1";
            };
          }
        ]).chalkos.kinds.Demo.group;
    expected = true;
  };
  testInvalidVersionFails = {
    expr =
      fails
        (image [
          {
            chalkos.kinds.Demo = {
              group = "demo.example.org";
              version = "1.0";
            };
          }
        ]).chalkos.kinds.Demo.version;
    expected = true;
  };
}
