# Evaluation tests for config kinds: the schema converter and the chalkos.kinds option of images.
# Returns lib.runTests failures, empty on success.
{ lib }:
let
  inherit (lib) mkOption types;
  schema = import ../../modules/node/kinds/schema.nix { inherit lib; };
  fails = value: !(builtins.tryEval (builtins.deepSeq value true)).success;

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
}
