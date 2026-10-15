# Evaluation tests for config kinds: the schema converter and the chalkos.kinds option of images.
# Returns lib.runTests failures, empty on success.
{ lib, mkCluster }:
let
  inherit (lib) mkOption types;
  schema = import ../../modules/node/kinds/schema.nix { inherit lib; };
  getLib = import ../../modules/node/kinds/get.nix { inherit lib; };
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
  # A unit that reads the Demo kind and networkd's Network.
  kindConsumerModule = {
    systemd.services.bird.serviceConfig.ExecStart = "/bin/true";
    chalkos.consumers.bird.kinds = [
      "Demo"
      "Network"
    ];
  };
  withKindConsumer = image [
    demoModule
    kindConsumerModule
  ];
  consumersOf = config: builtins.fromJSON config.environment.etc."chalkos/consumers.json".text;
  consumerOf =
    kinds:
    image [
      demoModule
      {
        systemd.services.bird.serviceConfig.ExecStart = "/bin/true";
        chalkos.consumers.bird.kinds = kinds;
      }
    ];
  # Forces the schemas an image ships, and with them the checks of its kinds' names.
  schemasOf = config: config.system.build.chalkosSchemas.drvPath;

  # The worker image of a cluster with a Kubernetes worker role and no extra modules.
  kubernetesImage =
    (mkCluster {
      modules = [
        {
          chalkos.cluster = {
            name = "t";
            endpoint = "https://10.0.0.1:6443";
          };
          chalkos.roles.worker.kubernetes.kind = "worker";
        }
      ];
    }).roles.worker.nixos.metal.config;
  # The image of a role without Kubernetes.
  plainImage =
    (mkCluster {
      modules = [
        {
          chalkos.cluster = {
            name = "t";
            endpoint = "https://10.0.0.1:6443";
          };
          chalkos.roles.worker.kubernetes.kind = null;
        }
      ];
    }).roles.worker.nixos.metal.config;
  # The installer of a one-role cluster.
  installer =
    (mkCluster {
      modules = [
        {
          chalkos.cluster = {
            name = "t";
            endpoint = "https://10.0.0.1:6443";
          };
          chalkos.roles.worker = { };
        }
      ];
    }).installer.nixos.config;
  hasChalkcfg = packages: lib.any (p: lib.getName p == "chalkcfg") packages;
  # The options of the Peering kind: a nested group, a nullOr submodule, a list of submodules,
  # an attribute set and a secretRef.
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
    labels = mkOption {
      type = types.attrsOf types.str;
      default = { };
    };
    token = mkOption {
      type = types.nullOr secretRef;
      default = null;
    };
  };
  withPeering = image [
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
  # The arguments of the command get builds, and whether it runs the image's chalkcfg.
  getPeering =
    args:
    let
      words = lib.splitString " " (withPeering.chalkos.kinds.Peering.get args);
    in
    {
      chalkcfg =
        lib.hasSuffix "/bin/chalkcfg" (lib.head words) && lib.hasPrefix "/nix/store/" (lib.head words);
      args = lib.concatStringsSep " " (lib.tail words);
    };
  resolvePeering = getLib.resolve (peeringSpec schema.types.secretRef);
  homelab =
    (mkCluster { modules = [ ../../examples/homelab/cluster.nix ]; }).roles.worker.nixos.metal.config;
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
  testBuiltinKindsOfKubernetesImage = {
    expr = lib.sort lib.lessThan (kindNames kubernetesImage);
    expected = [
      "Cluster"
      "Image"
      "Kubelet"
      "Link"
      "NetDev"
      "Network"
      "Node"
      "Secret"
      "SystemDisk"
      "TimeSync"
      "Volume"
    ];
  };
  testImageWithoutKubernetesHasNoKubelet = {
    expr = lib.elem "Kubelet" (kindNames plainImage);
    expected = false;
  };
  testBuiltinSchemasMatchFixtures = {
    expr = map (kind: builtinSchema kind) [
      "Cluster"
      "Image"
      "Kubelet"
      "Node"
      "Secret"
      "SystemDisk"
      "TimeSync"
      "Volume"
    ];
    expected = map builtinFixture [
      "chalkos.dev_v1alpha1_Cluster.json"
      "chalkos.dev_v1alpha1_Image.json"
      "kubernetes.chalkos.dev_v1alpha1_Kubelet.json"
      "chalkos.dev_v1alpha1_Node.json"
      "chalkos.dev_v1alpha1_Secret.json"
      "storage.chalkos.dev_v1alpha1_SystemDisk.json"
      "time.chalkos.dev_v1alpha1_TimeSync.json"
      "storage.chalkos.dev_v1alpha1_Volume.json"
    ];
  };
  testReservedGroupFails = {
    expr = fails (
      schemasOf (image [
        {
          chalkos.kinds.Mine = {
            group = "extra.chalkos.dev";
            version = "v1";
          };
        }
      ])
    );
    expected = true;
  };
  testBuiltinNameIsTaken = {
    expr =
      fails
        (image [
          {
            chalkos.kinds.Node = {
              group = "demo.example.org";
              version = "v1";
            };
          }
        ]).chalkos.kinds.Node.group;
    expected = true;
  };
  testKindConsumerLoadsItsSecrets = {
    expr = withKindConsumer.systemd.services.bird.serviceConfig.LoadCredential;
    expected = [ "secret:/run/chalkos/credentials/bird" ];
  };
  # A shell on a node queries the config with chalkcfg; the installer has no config to query.
  testRoleImageHasChalkcfg = {
    expr = hasChalkcfg (image [ ]).environment.systemPackages;
    expected = true;
  };
  testInstallerHasNoChalkcfg = {
    expr = hasChalkcfg installer.environment.systemPackages;
    expected = false;
  };
  testKindConsumerHasChalkcfgOnPath = {
    expr = hasChalkcfg withKindConsumer.systemd.services.bird.path;
    expected = true;
  };
  testKindConsumerRecordedForChalkd = {
    expr = (consumersOf withKindConsumer).bird;
    expected = {
      kinds = [
        "Demo"
        "Network"
      ];
      restartOnChange = true;
    };
  };
  testKindConsumerWithServiceDoesNotWarn = {
    expr = withKindConsumer.warnings;
    expected = [ ];
  };
  testKindConsumerWithoutServiceWarns = {
    expr =
      lib.any (lib.hasInfix "chalkos.consumers.orphan")
        (image [
          { chalkos.consumers.orphan.kinds = [ "Network" ]; }
        ]).warnings;
    expected = true;
  };
  testKindConsumerOfUnknownKindFails = {
    expr = fails (consumersOf (consumerOf [ "Nope" ]));
    expected = true;
  };
  # A consumer reads at least one kind; one of none would load credentials no one writes.
  testKindConsumerOfNoKindsFails = {
    expr = fails (consumersOf (consumerOf [ ]));
    expected = true;
  };
  testKindConsumerOfSecretFails = {
    expr = fails (consumersOf (consumerOf [ "Secret" ]));
    expected = true;
  };
  testUnknownKindFailsTheUnits = {
    expr = fails (consumerOf [ "Nope" ]).systemd.services.bird.serviceConfig.LoadCredential;
    expected = true;
  };
  # Units read config kinds alone: there are no identity keys to read.
  testDottedConsumersAreGone = {
    expr = fails (
      consumersOf (image [
        {
          systemd.services.bird.serviceConfig.ExecStart = "/bin/true";
          chalkos.node.consumers.bird.keys = [ "hostname" ];
        }
      ])
    );
    expected = true;
  };
  testNodeFileIsGone = {
    expr = (image [ ]).chalkos ? node;
    expected = false;
  };
  testKubernetesImageDirectoryHasClusterFile = {
    expr = lib.hasInfix "$out/kubernetes.json" kubernetesImage.system.build.chalkosImage.buildCommand;
    expected = true;
  };
  testImageDirectoryWithoutKubernetesHasNoClusterFile = {
    expr = lib.hasInfix "kubernetes.json" plainImage.system.build.chalkosImage.buildCommand;
    expected = false;
  };
  testGetPath = {
    expr = getPeering {
      name = "uplink";
      path = "spec.asn";
    };
    expected = {
      chalkcfg = true;
      args = "get Peering uplink -o 'jsonpath={.spec.asn}'";
    };
  };
  testGetNestedGroup = {
    expr = (getPeering { path = "spec.peer.address"; }).args;
    expected = "get Peering -o 'jsonpath={.spec.peer.address}'";
  };
  testGetNullOrSubmodule = {
    expr = resolvePeering "spec.timers.hold";
    expected.jsonpath = "{.spec.timers.hold}";
  };
  testGetListElements = {
    expr = (getPeering { path = "spec.neighbours.*.address"; }).args;
    expected = "get Peering -o 'jsonpath={.spec.neighbours[*].address}'";
  };
  testGetListIndex = {
    expr = resolvePeering "spec.neighbours.0.address";
    expected.jsonpath = "{.spec.neighbours[0].address}";
  };
  testGetAttrsOfKey = {
    expr = resolvePeering "spec.labels.zone-a";
    expected.jsonpath = "{.spec.labels['zone-a']}";
  };
  testGetAttrsOfIdentifierKey = {
    expr = resolvePeering "spec.labels.zone";
    expected.jsonpath = "{.spec.labels.zone}";
  };
  testGetAttrsOfAll = {
    expr = resolvePeering "spec.labels.*";
    expected.jsonpath = "{.spec.labels[*]}";
  };
  testGetSecretRef = {
    expr = resolvePeering "spec.token.secretRef.name";
    expected.jsonpath = "{.spec.token.secretRef.name}";
  };
  testGetMetadataName = {
    expr = resolvePeering "metadata.name";
    expected.jsonpath = "{.metadata.name}";
  };
  testGetWithoutPath = {
    expr = getPeering { };
    expected = {
      chalkcfg = true;
      args = "get Peering";
    };
  };
  testGetOutput = {
    expr =
      (getPeering {
        name = "uplink";
        output = "json";
      }).args;
    expected = "get Peering uplink -o json";
  };
  testGetQuotesTheName = {
    expr = (getPeering { name = "a b"; }).args;
    expected = "get Peering 'a b'";
  };
  testGetUnknownFieldListsFields = {
    expr = resolvePeering "spec.asnn";
    expected.error = "spec has no field asnn; its fields are asn, labels, neighbours, peer, timers, token";
  };
  testGetUnknownTopField = {
    expr = resolvePeering "status";
    expected.error = "the document has no field status; its fields are apiVersion, kind, metadata, spec";
  };
  testGetBelowValue = {
    expr = resolvePeering "spec.peer.address.port";
    expected.error = "spec.peer.address holds a string and has no fields";
  };
  testGetListNeedsIndex = {
    expr = resolvePeering "spec.neighbours.address";
    expected.error = "spec.neighbours is a list: follow it with * or an index such as 0, not address";
  };
  testGetEmptyField = {
    expr = resolvePeering "spec..asn";
    expected.error = "the path has an empty field name";
  };
  testGetUnquotableKey = {
    expr = resolvePeering "spec.labels.it's";
    expected.error = "jsonpath cannot name the field it's";
  };
  testGetInvalidPathFails = {
    expr = fails (getPeering {
      path = "spec.asnn";
    });
    expected = true;
  };
  testGetPathAndOutputFail = {
    expr = fails (getPeering {
      path = "spec.asn";
      output = "json";
    });
    expected = true;
  };
  testGetUnknownOutputFails = {
    expr = fails (getPeering {
      output = "xml";
    });
    expected = true;
  };
  # The homelab's unit reads the node's Rack with chalkcfg, by a path the evaluation checked.
  testHomelabRackReadsWithChalkcfg = {
    expr = lib.hasInfix "/bin/chalkcfg get Rack rack -o 'jsonpath={.spec.location}'" homelab.systemd.services.rack-location.serviceConfig.ExecStart.text;
    expected = true;
  };
  testBuiltinKindHasGet = {
    expr = lib.hasSuffix "/bin/chalkcfg get Node -o 'jsonpath={.spec.type}'" (
      kubernetesImage.chalkos.kinds.Node.get { path = "spec.type"; }
    );
    expected = true;
  };
}
