# Evaluation tests for the cluster definition; returns lib.runTests failures (empty on success).
{
  lib,
  mkCluster,
  flakeModule,
}:
let
  cluster =
    modules:
    mkCluster {
      modules = [
        {
          chalkos.cluster = {
            name = "t";
            endpoint = "https://10.0.0.1:6443";
          };
          chalkos.roles.worker = { };
        }
      ]
      ++ modules;
    };
  role = c: c.roles.worker.nixos.config;
  fails = value: !(builtins.tryEval (builtins.deepSeq value true)).success;

  demoExtension =
    { config, lib, ... }:
    {
      options.chalkos.demo.enable = lib.mkEnableOption "demo";
      config.chalkos.demo.enable = true;
    };

  consumerModule = {
    systemd.services.demo.serviceConfig.ExecStart = "/bin/true";
    chalkos.node.consumers.demo.keys = [ "demo.key" ];
  };
  withConsumer = role (cluster [ { chalkos.roles.worker.nixosModules = [ consumerModule ]; } ]);
  withOrphanConsumer = role (cluster [
    {
      chalkos.roles.worker.nixosModules = [ { chalkos.node.consumers.orphan.keys = [ "demo.key" ]; } ];
    }
  ]);
  failedAssertions = config: map (a: a.message) (lib.filter (a: !a.assertion) config.assertions);

  rackExtension =
    { lib, ... }:
    {
      options.chalkos.nodes = lib.mkOption {
        type = lib.types.attrsOf (
          lib.types.submodule { options.rack.location = lib.mkOption { type = lib.types.str; }; }
        );
      };
    };
  twoNodes = cluster [
    rackExtension
    {
      chalkos.nodes.n1 = {
        role = "worker";
        rack.location = "a1";
        taints = [
          {
            key = "dedicated";
            effect = "NoSchedule";
          }
        ];
      };
    }
  ];
in
lib.runTests {
  testRoleReadsClusterEndpoint = {
    expr = (role (cluster [ ])).chalkos.cluster.endpoint;
    expected = "https://10.0.0.1:6443";
  };
  testRoleReadsFeatureNamespace = {
    expr = (role (cluster [ demoExtension ])).chalkos.demo.enable;
    expected = true;
  };
  testRoleCannotReadNodes = {
    expr = fails (role (cluster [ ])).chalkos.nodes;
    expected = true;
  };
  testRoleRejectsUnknownSetting = {
    expr =
      fails
        (role (cluster [
          { chalkos.roles.worker.nixosModules = [ { chalkos.disks.espSize = "1G"; } ]; }
        ])).chalkos.cluster.endpoint;
    expected = true;
  };
  testRoleCannotOverrideClusterSetting = {
    expr =
      fails
        (role (cluster [
          {
            chalkos.roles.worker.nixosModules = [
              { chalkos.cluster.endpoint = lib.mkForce "https://10.0.0.2:6443"; }
            ];
          }
        ])).chalkos.cluster.endpoint;
    expected = true;
  };
  testRoleSetsDiskOptions = {
    expr =
      (role (cluster [
        { chalkos.roles.worker.nixosModules = [ { chalkos.disk.espSize = "256M"; } ]; }
      ])).chalkos.disk.espSize;
    expected = "256M";
  };
  testRoleModulesApply = {
    expr =
      (role (cluster [
        { chalkos.roles.worker.nixosModules = [ { networking.hostName = "shared"; } ]; }
      ])).networking.hostName;
    expected = "shared";
  };
  testWrongNodeTypeFails = {
    expr =
      fails
        (cluster [
          {
            chalkos.nodes.n1 = {
              role = "worker";
              labels = 5;
            };
          }
        ]).nodes;
    expected = true;
  };
  testConsumerLoadsCredential = {
    expr = withConsumer.systemd.services.demo.serviceConfig.LoadCredential;
    expected = [ "demo.key:/run/chalkos/credentials/demo.key" ];
  };
  testConsumersRecordedForChalkd = {
    expr = builtins.fromJSON withConsumer.environment.etc."chalkos/consumers.json".text;
    expected = {
      demo = {
        keys = [ "demo.key" ];
        restartOnChange = true;
      };
    };
  };
  testConsumerWithServicePassesAssertions = {
    expr = failedAssertions withConsumer;
    expected = [ ];
  };
  testConsumerWithoutServiceFails = {
    expr = lib.any (lib.hasInfix "orphan") (failedAssertions withOrphanConsumer);
    expected = true;
  };
  testNodeFilePath = {
    expr = withConsumer.chalkos.node.file;
    expected = "/run/chalkos/node.json";
  };
  testManifestVersion = {
    expr = twoNodes.manifest.schemaVersion;
    expected = 0;
  };
  testManifestRoleImagePath = {
    expr = twoNodes.manifest.roles.worker.image;
    expected = "roles.worker.image";
  };
  testManifestIdentity = {
    expr = twoNodes.manifest.nodes.n1.identity;
    expected = {
      hostname = "n1";
      network = { };
      labels = { };
      taints = [
        {
          key = "dedicated";
          value = null;
          effect = "NoSchedule";
        }
      ];
      extensions = {
        rack.location = "a1";
      };
    };
  };
  testManifestIsJson = {
    expr = builtins.isString (builtins.toJSON twoNodes.manifest);
    expected = true;
  };
  testUnknownRoleFails = {
    expr =
      fails
        (cluster [
          {
            chalkos.nodes.n1.role = "nope";
          }
        ]).nodes.n1.role;
    expected = true;
  };
  testMkClusterPassesNixpkgsToModules = {
    expr =
      (mkCluster {
        nixpkgs = {
          inherit lib;
          marker = "custom";
        };
        modules = [
          (
            { nixpkgs, ... }:
            {
              chalkos.cluster = {
                name = nixpkgs.marker;
                endpoint = "https://10.0.0.1:6443";
              };
            }
          )
        ];
      }).cluster.name;
    expected = "custom";
  };
  testFlakeModuleNamesClusters = {
    expr =
      (lib.evalModules {
        modules = [
          flakeModule
          # Stand-in for flake-parts' `flake` option.
          { options.flake = lib.mkOption { type = lib.types.lazyAttrsOf lib.types.raw; }; }
          {
            chalkos.clusters.demo = {
              chalkos.cluster.endpoint = "https://10.0.0.1:6443";
            };
          }
        ];
      }).config.flake.chalkos.demo.manifest.cluster.name;
    expected = "demo";
  };
}
