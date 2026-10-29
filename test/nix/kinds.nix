# Evaluation tests for config kinds: the schema converter and the chalkos.kinds option of images.
# Returns lib.runTests failures, empty on success.
{
  lib,
  mkImages,
}:
let
  inherit (lib) mkOption types;
  schema = import ../../lib/kinds/schema.nix { inherit lib; };
  kindTypes = import ../../lib/kinds/types.nix { inherit lib; };
  getLib = import ../../lib/kinds/get.nix { inherit lib; };
  libChalkos = import ../../lib/chalkos.nix { inherit lib; };
  inherit (import ./lib.nix { inherit mkImages; }) fails worker;

  demoModule =
    { lib, ... }:
    {
      chalkos.kinds.Demo = {
        group = "demo.example.org";
        version = "v1alpha1";
        multiple = true;
        spec.token = lib.mkOption {
          type = lib.types.nullOr lib.chalkos.types.secretRef;
          default = null;
        };
      };
    };
  withDemo = worker [ demoModule ];
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
  etcJSON =
    file: config:
    builtins.fromJSON (
      builtins.unsafeDiscardStringContext config.environment.etc."chalkos/${file}".text
    );
  servicesOf = etcJSON "services.json";
  effectsOf = etcJSON "effects.json";
  outputsOf = etcJSON "outputs.json";
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

  # The metal image of a variant with Kubernetes and no extra modules.
  kubernetesImage = worker [ ];
  # The metal image of a variant without Kubernetes.
  plainImage = (mkImages { variants.worker.kubernetes = false; }).nixos.worker-metal.config;
  # A router image, and the kinds it adds.
  routerImage =
    (mkImages {
      variants.edge = {
        kubernetes = false;
        router = true;
      };
    }).nixos.edge-metal.config;
  routerKinds = [
    "AddressSet"
    "BGP"
    "BGPPeer"
    "CommunityList"
    "FirewallPolicy"
    "FirewallZone"
    "NAT64"
    "NATRule"
    "PrefixList"
    "Resolver"
    "Router"
    "RoutingPolicy"
  ];
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
  withPeering = worker [
    (
      { lib, ... }:
      {
        chalkos.kinds.Peering = {
          group = "bgp.example.org";
          version = "v1alpha1";
          multiple = true;
          spec = peeringSpec lib.chalkos.types.secretRef;
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
  resolvePeering = getLib.resolve (peeringSpec kindTypes.secretRef);
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
        description
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
  # A port or a range of ports, as the router kinds take them: a number, or "<first>-<last>", from
  # port 1.
  testPortRange = {
    expr = propOf kindTypes.portRange;
    expected.anyOf = [
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
  };
  # A BGP neighbour's AS, as BGPPeer takes it: external, internal or a 32-bit AS number.
  testPeerAS = {
    expr = propOf kindTypes.peerAS;
    expected.anyOf = [
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
      (worker [
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
      schemasOf (worker [
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
      schemasOf (worker [
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
      schemasOf (worker [
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
        (worker [
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
        (worker [
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
  # The kinds of router.chalkos.dev exist on router images alone.
  testRouterKindsOnRouterImagesAlone = {
    expr = {
      router = lib.intersectLists routerKinds (kindNames routerImage);
      kubernetes = lib.intersectLists routerKinds (kindNames kubernetesImage);
      plain = lib.intersectLists routerKinds (kindNames plainImage);
    };
    expected = {
      router = routerKinds;
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
  # A NAT rule translates to a port from 1, as nftables takes it.
  testNATRulePortFromOne = {
    expr =
      (builtins.head
        (builtins.head
          routerImage.system.build.chalkosKindSchemas."router.chalkos.dev_v1alpha1_NATRule.json".properties.spec.properties.to.anyOf
        ).properties.port.anyOf
      ).minimum;
    expected = 1;
  };
  testImageWithoutKubernetesHasRegistryAndMirror = {
    expr = lib.subtractLists (kindNames plainImage) [
      "Registry"
      "Mirror"
    ];
    expected = [ ];
  };
  # Flannel is a kind only where flannel is the pod network.
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
  testFlannelKindFollowsTheProvider = {
    expr = lib.elem "Flannel" (kindNames (worker [ { chalkos.cni.provider = "none"; } ]));
    expected = false;
  };
  # A Kubernetes object: an object with apiVersion, kind and metadata, and any other field.
  testKubernetesObjectSchema = {
    expr = propOf kindTypes.kubernetesObject;
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
      (worker [
        (
          { lib, ... }:
          {
            chalkos.kinds.Demo = {
              group = "demo.example.org";
              version = "v1alpha1";
              spec.x = mkOption { type = lib.chalkos.types.kubernetesObject or null; };
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
  # effects.json and services.json of the default image.
  testBuiltinEffectsMatchFixture = {
    expr = effectsOf kubernetesImage;
    expected = builtins.fromJSON (builtins.readFile ../fixtures/kinds/effects.json);
  };
  testBuiltinServicesMatchFixture = {
    expr = servicesOf kubernetesImage;
    expected = builtins.fromJSON (builtins.readFile ../fixtures/kinds/services.json);
  };
  testReservedGroupFails = {
    expr = fails (
      schemasOf (worker [
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
        (worker [
          {
            chalkos.kinds.Host = {
              group = "demo.example.org";
              version = "v1";
            };
          }
        ]).chalkos.kinds.Host.group;
    expected = true;
  };
  testGrantLoadsItsSecrets = {
    expr = withGrant.systemd.services.bird.serviceConfig.LoadCredential;
    expected = [ "secret:/run/chalkos/credentials/bird" ];
  };
  # A shell on a node queries the config with chalkcfg; the installer has no config to query.
  testImageHasChalkcfg = {
    expr = hasChalkcfg (worker [ ]).environment.systemPackages;
    expected = true;
  };
  testInstallerHasNoChalkcfg = {
    expr = hasChalkcfg installer.environment.systemPackages;
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
  # lib.chalkos's renderers are data, which an output's render turns into the program's command,
  # naming the file it writes after the template or program.
  testRenderers =
    let
      config = withBird (
        { pkgs, ... }:
        {
          chalkos.outputs = {
            t.render = libChalkos.render.template ../../modules/testing/greeting.tmpl;
            d.render = libChalkos.render.documents [
              "Peer"
              "Link"
            ];
            j.render = libChalkos.render.jq (builtins.toFile "bird.conf.jq" ".");
            p.render = pkgs.writeShellScript "r" "true";
          };
        }
      );
      render = name: (outputsOf config).${name}.render;
    in
    {
      expr = {
        value = libChalkos.render.documents [ "Peer" ];
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
  # An attribute set that is none of lib.chalkos's renderers, or a mix of more than one, fails the
  # evaluation.
  testUnknownRendererFails = {
    expr = {
      unknownKey = fails (
        outputsOf (withBird {
          chalkos.outputs.t.render = {
            python = "x.py";
          };
        })
      );
      twoKeys = fails (
        outputsOf (withBird {
          chalkos.outputs.t.render = {
            jq = "a.jq";
            template = "a.tmpl";
          };
        })
      );
    };
    expected = {
      unknownKey = true;
      twoKeys = true;
    };
  };
  # lib.chalkos's effects act on units when outputs or the documents of kinds change.
  testEffectHelpers = {
    expr = {
      restart = libChalkos.restartOn { outputs = [ "bird-conf" ]; } [ "bird.service" ];
      reload = libChalkos.reloadOn { kinds = [ "Demo" ]; } [ "bird.service" ];
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
  # An effect's outputs name outputs alone, so a misspelled one fails the evaluation.
  testOutputsRefusesANameThatIsNoOutput = {
    expr = fails (
      effectsOf (withBird {
        chalkos.effects.bird = {
          outputs = [ "bird-cnf" ];
          reload = [ "bird.service" ];
        };
      })
    );
    expected = true;
  };
  # An effect on a kind reaches chalkd; a kind the image lacks, Secret and Addon fail.
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
  testEffectOnAnUnknownKindFails = {
    expr =
      map
        (
          kind:
          fails (
            effectsOf (withBird {
              chalkos.effects.bird = {
                kinds = [ kind ];
                restart = [ "bird.service" ];
              };
            })
          )
        )
        [
          "Nope"
          "Secret"
          "Addon"
        ];
    expected = [
      true
      true
      true
    ];
  };
  testEffectWithoutTriggerFails = {
    expr = fails (
      effectsOf (withBird {
        chalkos.effects.bird.restart = [ "bird.service" ];
      })
    );
    expected = true;
  };
  testEffectWithoutActionFails = {
    expr = fails (
      effectsOf (withBird {
        chalkos.effects.bird.when = "true";
      })
    );
    expected = true;
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
        ];
    expected = [
      true
      true
      false
      false
      false
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
  testOutputOfUnknownKindFails = {
    expr = fails (
      outputsOf (withBird {
        chalkos.outputs.bird-conf = {
          inputs = [ "Nope" ];
          render = "/nix/store/x-render/bin/render";
        };
      })
    );
    expected = true;
  };
  testOutputOfSecretFails = {
    expr = fails (
      outputsOf (withBird {
        chalkos.outputs.bird-conf = {
          inputs = [ "Secret" ];
          render = "/nix/store/x-render/bin/render";
        };
      })
    );
    expected = true;
  };
  testOutputNameIsPlain = {
    expr = fails (
      outputsOf (withBird {
        chalkos.outputs."Bird.conf".render = "/nix/store/x-render/bin/render";
      })
    );
    expected = true;
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
  # chalkos-render with the image's networkd schemas and examples, which networkd reloads.
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
        examples = lib.attrNames (builtins.readDir withDemo.chalkos.outputs.networkd.examples);
        effect = (effectsOf withDemo).networkd;
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
      examples = [
        "none.yaml"
        "static.yaml"
        "vlan.yaml"
      ];
      effect = {
        outputs = [ "networkd" ];
        kinds = [ ];
        reload = [ "systemd-networkd.service" ];
        restart = [ ];
        run = "";
        start = false;
        when = "";
        chalkd = "";
        hint = null;
      };
    };
  };
  # Every image has the unprivileged user its renderers run as, since it renders the built-in
  # outputs.
  testRenderUser = {
    expr = withDemo.users.users.chalkos-render.isSystemUser;
    expected = true;
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
  # The image's rules check reads the effects, the outputs and the units.
  testRulesCheckReadsTheImage = {
    expr = lib.hasInfix "chalkos-rules-image" withGrant.system.build.chalkosRulesCheck.buildCommand;
    expected = true;
  };
  testGrantWithServiceDoesNotWarn = {
    expr = withGrant.warnings;
    expected = [ ];
  };
  testGrantWithoutServiceWarns = {
    expr =
      lib.any (lib.hasInfix "systemd.services.orphan.chalkos.kinds")
        (worker [
          { systemd.services.orphan.chalkos.kinds = [ "Network" ]; }
        ]).warnings;
    expected = true;
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
  testUnknownKindFailsTheUnits = {
    expr = fails (grantOf [ "Nope" ]).systemd.services.bird.serviceConfig.LoadCredential;
    expected = true;
  };
  testKubernetesImageDirectoryHasImageFacts = {
    expr = lib.hasInfix "$out/image.json" kubernetesImage.system.build.chalkosImage.buildCommand;
    expected = true;
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
            x = libChalkos.mkOption {
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
                options.y = libChalkos.mkOption {
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
  # The Extension API page, whose table of lib.chalkos is in nix/docs-templates, lists each
  # helper of lib.chalkos: a function, or a type, as the leaves of its attribute sets.
  testExtensionAPIListsEveryHelper = {
    expr =
      let
        rows = lib.filter (l: lib.hasPrefix "| `lib.chalkos." (lib.trim l)) (
          lib.splitString "\n" (builtins.readFile ../../nix/docs-templates/extension-api.tail.md)
        );
      in
      lib.sort lib.lessThan (
        map (l: lib.head (builtins.match "[|] `lib[.]chalkos[.]([A-Za-z.]+)`.*" (lib.trim l))) rows
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
      lib.sort lib.lessThan (leaves "" libChalkos);
  };
  # An option without rules is lib.mkOption's.
  testMkOptionWithoutRules = {
    expr = libChalkos.mkOption { type = types.str; } == mkOption { type = types.str; };
    expected = true;
  };
  # A kind's rules reach x-chalkos-kind; a kind without rules has none there.
  testKindRulesReachTheSchema = {
    expr =
      let
        schemas =
          (worker [
            demoModule
            {
              chalkos.kinds.Demo.rules = [
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
          description = "A node holds one Demo at most.";
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
  # The test image's Greeting refuses a message too long for one log line, and a new one while
  # its lock fact holds one; the image declares the fact, and checks the rules when it is built.
  testGreetingRulesAndFact = {
    expr =
      let
        build = (worker [ ../../modules/testing/greeting.nix ]).system.build;
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
    expr = fails (worker [ { chalkos.facts.disk-ids.file = "/x"; } ]).system.build.chalkosFacts.drvPath;
    expected = true;
  };
  testFactWithFileAndCommandFails = {
    expr =
      fails
        (worker [
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
    expr = fails (worker [ { chalkos.facts.x = { }; } ]).system.build.chalkosFacts.drvPath;
    expected = true;
  };
  testFileFactWithInputsFails = {
    expr =
      fails
        (worker [
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
        (worker [
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
        (worker [
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
