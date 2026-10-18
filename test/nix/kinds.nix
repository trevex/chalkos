# Evaluation tests for config kinds: the schema converter and the chalkos.kinds option of images.
# Returns lib.runTests failures, empty on success.
{
  lib,
  mkImages,
}:
let
  inherit (lib) mkOption types;
  schema = import ../../modules/node/kinds/schema.nix { inherit lib; };
  getLib = import ../../modules/node/kinds/get.nix { inherit lib; };
  fails = value: !(builtins.tryEval (builtins.deepSeq value true)).success;

  # The metal image of a variant worker with Kubernetes and the given NixOS modules.
  image = modules: (mkImages { variants.worker.modules = modules; }).nixos.worker-metal.config;

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

  # The metal image of a variant with Kubernetes and no extra modules.
  kubernetesImage = image [ ];
  # The metal image of a variant without Kubernetes.
  plainImage = (mkImages { variants.worker.kubernetes = false; }).nixos.worker-metal.config;
  # The installer.
  installer = (mkImages { variants = { }; }).nixos.installer.config;
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
  # The homelab example's image, with its Rack extension.
  homelab =
    (mkImages { variants.node.modules = [ ../../examples/homelab/extensions/rack.nix ]; })
    .nixos.node-metal.config;
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
      "Addon"
      "Flannel"
      "Host"
      "Kubernetes"
      "Link"
      "NetDev"
      "Network"
      "Node"
      "RegistryMirror"
      "Secret"
      "SystemDisk"
      "TimeSync"
      "Volume"
    ];
  };
  testImageWithoutKubernetesHasNoNode = {
    expr = lib.elem "Node" (kindNames plainImage);
    expected = false;
  };
  testImageWithoutKubernetesHasNoKubernetesKinds = {
    expr = lib.intersectLists [
      "Addon"
      "Flannel"
      "Kubernetes"
      "RegistryMirror"
    ] (kindNames plainImage);
    expected = [ ];
  };
  # Flannel is a kind only where flannel is the pod network.
  # Registries and mirrors as containerd takes them: IPv6 hosts in brackets, and mirrors without a
  # scheme, which containerd reaches with https.
  testRegistryMirrorHosts =
    let
      spec = kubernetesImage.chalkos.kinds.RegistryMirror.spec;
      registry = spec.registry.type.check;
      mirror = spec.mirrors.type.nestedTypes.elemType.check;
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
        mirrors = map mirror [
          "https://mirror.example.com"
          "http://[fd00::1]:5000"
          "mirror.lan:5000"
          "[fd00::1]:5000"
        ];
        badMirrors = map mirror [
          "ftp://mirror.example.com"
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
        mirrors = [
          true
          true
          true
          true
        ];
        badMirrors = [
          false
          false
        ];
      };
    };
  testFlannelKindFollowsTheProvider = {
    expr = lib.elem "Flannel" (kindNames (image [ { chalkos.cni.provider = "none"; } ]));
    expected = false;
  };
  # A Kubernetes object: an object with apiVersion, kind and metadata, and any other field.
  testKubernetesObjectSchema = {
    expr = propOf schema.types.kubernetesObject;
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
  # Extensions declare no raw objects.
  testKubernetesObjectIsNotPublic = {
    expr =
      (image [
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
    expr = map (kind: builtinSchema kind) [
      "Host"
      "Flannel"
      "Kubernetes"
      "Addon"
      "Node"
      "RegistryMirror"
      "Secret"
      "SystemDisk"
      "TimeSync"
      "Volume"
    ];
    expected = map builtinFixture [
      "chalkos.dev_v1alpha1_Host.json"
      "kubernetes.chalkos.dev_v1alpha1_Flannel.json"
      "kubernetes.chalkos.dev_v1alpha1_Kubernetes.json"
      "kubernetes.chalkos.dev_v1alpha1_Addon.json"
      "kubernetes.chalkos.dev_v1alpha1_Node.json"
      "kubernetes.chalkos.dev_v1alpha1_RegistryMirror.json"
      "chalkos.dev_v1alpha1_Secret.json"
      "storage.chalkos.dev_v1alpha1_SystemDisk.json"
      "time.chalkos.dev_v1alpha1_TimeSync.json"
      "storage.chalkos.dev_v1alpha1_Volume.json"
    ];
  };
  # chalkd's tests carry out the built-in consumers' rules from this fixture, so it is the
  # consumers.json of the default image.
  testBuiltinConsumersMatchFixture = {
    expr = consumersOf kubernetesImage;
    expected = builtins.fromJSON (builtins.readFile ../fixtures/kinds/consumers.json);
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
            chalkos.kinds.Host = {
              group = "demo.example.org";
              version = "v1";
            };
          }
        ]).chalkos.kinds.Host.group;
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
  # By default a unit restarts when one of its kinds changes, and never reloads.
  testKindConsumerRecordedForChalkd = {
    expr = (consumersOf withKindConsumer).bird;
    expected = {
      kinds = [
        "Demo"
        "Network"
      ];
      restart = {
        when = ''"Demo" in changedKinds || "Network" in changedKinds'';
        start = false;
      };
      reload = {
        when = "false";
        command = "systemctl reload bird";
      };
    };
  };
  testConsumerRulesReachChalkd = {
    expr =
      (consumersOf (image [
        {
          systemd.services.bird.serviceConfig.ExecStart = "/bin/true";
          chalkos.consumers.bird = {
            restart = {
              when = "facts.share != oldFacts.share";
              start = true;
            };
            reload = {
              when = ''changedKinds.exists(k, k in ["Link", "NetDev", "Network"])'';
              command = "birdc configure";
            };
          };
        }
      ])).bird;
    expected = {
      kinds = [ ];
      restart = {
        when = "facts.share != oldFacts.share";
        start = true;
      };
      reload = {
        when = ''changedKinds.exists(k, k in ["Link", "NetDev", "Network"])'';
        command = "birdc configure";
      };
    };
  };
  # The image's rules check compiles the consumers' rules too.
  testRulesCheckReadsTheConsumers = {
    expr = lib.hasInfix "chalkos-consumers.json" withKindConsumer.system.build.chalkosRulesCheck.buildCommand;
    expected = true;
  };
  testRestartOnChangeIsGone = {
    expr = fails (
      consumersOf (image [
        {
          systemd.services.bird.serviceConfig.ExecStart = "/bin/true";
          chalkos.consumers.bird = {
            kinds = [ "Demo" ];
            restartOnChange = false;
          };
        }
      ])
    );
    expected = true;
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
  # A consumer of no kinds reads no documents, so it gets no credentials, no chalkcfg and no
  # warning, whose unit may come from systemd.packages.
  testConsumerOfNoKindsLoadsNoCredentials = {
    expr =
      let
        config = image [
          {
            systemd.services.bird.serviceConfig.ExecStart = "/bin/true";
            chalkos.consumers.bird.reload.when = "true";
            chalkos.consumers.systemd-journald.reload.when = "true";
          }
        ];
      in
      {
        credentials = config.systemd.services.bird.serviceConfig ? LoadCredential;
        chalkcfg = hasChalkcfg config.systemd.services.bird.path;
        warnings = config.warnings;
        restart = (consumersOf config).bird.restart.when;
      };
    expected = {
      credentials = false;
      chalkcfg = false;
      warnings = [ ];
      restart = "false";
    };
  };
  testKindConsumerOfSecretFails = {
    expr = fails (consumersOf (consumerOf [ "Secret" ]));
    expected = true;
  };
  # Addon documents never reach /run, so no unit reads them.
  testKindConsumerOfAddonFails = {
    expr = fails (consumersOf (consumerOf [ "Addon" ]));
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
  testKubernetesImageDirectoryHasImageFacts = {
    expr = {
      facts = lib.hasInfix "$out/image.json" kubernetesImage.system.build.chalkosImage.buildCommand;
      clusterFile = lib.hasInfix "kubernetes.json" kubernetesImage.system.build.chalkosImage.buildCommand;
    };
    expected = {
      facts = true;
      clusterFile = false;
    };
  };
  testImageDirectoryWithoutKubernetesHasNoImageFacts = {
    expr = lib.hasInfix "image.json" plainImage.system.build.chalkosImage.buildCommand;
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
  # client-go's jsonpath takes [*] for lists only; .* takes every value of an object.
  testGetAttrsOfAll = {
    expr = resolvePeering "spec.labels.*";
    expected.jsonpath = "{.spec.labels.*}";
  };
  testGetAttrsOfSubmoduleAll = {
    expr = (getPeering { path = "spec.pools.*.cidr"; }).args;
    expected = "get Peering -o 'jsonpath={.spec.pools.*.cidr}'";
  };
  testGetAttrsOfKeyWithSlashColonAndDigits = {
    expr = map resolvePeering [
      "spec.labels.zone/a"
      "spec.labels.a:b"
      "spec.labels.0"
    ];
    expected = [
      { jsonpath = "{.spec.labels['zone/a']}"; }
      { jsonpath = "{.spec.labels['a:b']}"; }
      { jsonpath = "{.spec.labels['0']}"; }
    ];
  };
  # client-go's jsonpath does not quote within ['…'], so it misreads keys with these characters.
  testGetKeyWithSpaceFails = {
    expr = resolvePeering "spec.labels.a b";
    expected.error = "the key a b cannot be addressed by a jsonpath; use output with a go-template";
  };
  testGetKeyWithAtFails = {
    expr = resolvePeering "spec.labels.a@b";
    expected.error = "the key a@b cannot be addressed by a jsonpath; use output with a go-template";
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
  # chalkcfg reads only names that follow metadata.name's pattern.
  testGetDottedName = {
    expr = (getPeering { name = "uplink.a-1"; }).args;
    expected = "get Peering uplink.a-1";
  };
  testGetInvalidNameFails = {
    expr =
      map
        (
          name:
          fails (getPeering {
            inherit name;
          })
        )
        [
          "a b"
          "Uplink"
          "-a"
          "a."
          "../x"
          ""
        ];
    expected = [
      true
      true
      true
      true
      true
      true
    ];
  };
  testGetUnknownFieldListsFields = {
    expr = resolvePeering "spec.asnn";
    expected.error = "spec has no field asnn; its fields are asn, labels, neighbours, peer, pools, timers, token";
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
    expected.error = "the key it's cannot be addressed by a jsonpath; use output with a go-template";
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
  # A field's rules reach its property, also in a submodule, with takes defaulted and the
  # attributes left null left out.
  testFieldRulesReachTheSchema = {
    expr =
      let
        props =
          (specOf {
            x = mkOption { type = types.str; } // {
              "x-chalkos-rules" = [
                {
                  rule = "self == oldSelf";
                  message = "x is fixed";
                  messageExpression = null;
                }
              ];
            };
            group = mkOption {
              type = types.submodule {
                options.y = mkOption { type = types.int; } // {
                  "x-chalkos-rules" = [
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
  # A kind's rules reach x-chalkos-kind; a kind without rules has none there.
  testKindRulesReachTheSchema = {
    expr =
      let
        schemas =
          (image [
            demoModule
            {
              chalkos.kinds.Demo.rules = [
                {
                  rule = "size(documents) <= 1";
                  message = "one Demo";
                }
                {
                  rule = "documents == oldDocuments";
                  messageExpression = "\"keep \" + oldDocuments.map(n, n).join(\", \")";
                  takes = "reinstall";
                }
              ];
            }
          ]).system.build.chalkosKindSchemas;
      in
      {
        demo = schemas."demo.example.org_v1alpha1_Demo.json"."x-chalkos-kind".rules;
        host = schemas."chalkos.dev_v1alpha1_Host.json"."x-chalkos-kind" ? rules;
      };
    expected = {
      demo = [
        {
          rule = "size(documents) <= 1";
          message = "one Demo";
          takes = "refuse";
        }
        {
          rule = "documents == oldDocuments";
          messageExpression = "\"keep \" + oldDocuments.map(n, n).join(\", \")";
          takes = "reinstall";
        }
      ];
      host = false;
    };
  };
  # The image's facts reach /etc/chalkos/facts.json: a file with the fields it keeps, or a command
  # with its inputs.
  testFactsReachTheImage = {
    expr =
      let
        config = image [
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
        seen = defined.seen;
        probe = defined.probe;
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
  # Every image hashes the networkd units and the time sources chalkd writes, which networkd's
  # and chrony's reload rules compare.
  testBuiltinFacts = {
    expr =
      let
        defined = kubernetesImage.system.build.chalkosFactDefinitions;
      in
      {
        names = lib.attrNames defined;
        plain = lib.attrNames plainImage.system.build.chalkosFactDefinitions;
        recorded = defined.recorded;
        share = defined.share;
        cluster = defined.cluster;
        disks = lib.hasSuffix " disks" defined.disks.command && !(defined.disks ? inputs);
        fallback =
          lib.hasSuffix " fallback" defined.fallback.command
          && defined.fallback.inputs == [ "fallbackSecret" ];
        networkUnits = lib.hasSuffix "-chalkos-hash-files /run/systemd/network" defined.networkUnits.command;
        timeSources = lib.hasSuffix "-chalkos-hash-files /run/chalkos/chrony/identity.sources" defined.timeSources.command;
      };
    expected = {
      names = [
        "cluster"
        "disks"
        "fallback"
        "networkUnits"
        "recorded"
        "share"
        "timeSources"
      ];
      plain = [
        "disks"
        "fallback"
        "networkUnits"
        "recorded"
        "timeSources"
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
      networkUnits = true;
      timeSources = true;
    };
  };
  # The test image's Greeting refuses a message too long for one log line, and a new one while
  # its lock fact holds one; the image declares the fact, and checks the rules when it is built.
  testGreetingRulesAndFact = {
    expr =
      let
        build = (image [ ../../modules/testing/greeting.nix ]).system.build;
        greeting = build.chalkosKindSchemas."greeting.example.org_v1alpha1_Greeting.json";
      in
      {
        kind = map (r: r.takes) greeting."x-chalkos-kind".rules;
        field = map (r: r.rule) greeting.properties.spec.properties.message."x-chalkos-rules";
        fact = build.chalkosFactDefinitions.greetingLock;
        checked = lib.hasPrefix "chalkos-rules-check" build.chalkosRulesCheck.name;
      };
    expected = {
      kind = [ "refuse" ];
      field = [ "facts.greetingLock == null || self == oldSelf" ];
      fact.file = "/run/chalktest-greeting-lock.json";
      checked = true;
    };
  };
  testFactNameIsAnIdentifier = {
    expr = fails (image [ { chalkos.facts.disk-ids.file = "/x"; } ]).system.build.chalkosFacts.drvPath;
    expected = true;
  };
  testFactWithFileAndCommandFails = {
    expr =
      fails
        (image [
          {
            chalkos.facts.x = {
              file = "/x";
              command = "/bin/x";
            };
          }
        ]).system.build.chalkosFacts.drvPath;
    expected = true;
  };
  testFactWithoutSourceFails = {
    expr = fails (image [ { chalkos.facts.x = { }; } ]).system.build.chalkosFacts.drvPath;
    expected = true;
  };
  testFileFactWithInputsFails = {
    expr =
      fails
        (image [
          {
            chalkos.facts.x = {
              file = "/x";
              inputs = [ "fallbackSecret" ];
            };
          }
        ]).system.build.chalkosFacts.drvPath;
    expected = true;
  };
  testCommandFactWithFieldsFails = {
    expr =
      fails
        (image [
          {
            chalkos.facts.x = {
              command = "/bin/x";
              fields = [ "kind" ];
            };
          }
        ]).system.build.chalkosFacts.drvPath;
    expected = true;
  };
  testCommandFactThatHashesFails = {
    expr =
      fails
        (image [
          {
            chalkos.facts.x = {
              command = "/bin/x";
              hashOthers = true;
            };
          }
        ]).system.build.chalkosFacts.drvPath;
    expected = true;
  };
}
