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
        storage.system.disk = "/dev/vda";
        taints = [
          {
            key = "dedicated";
            effect = "NoSchedule";
          }
        ];
      };
    }
  ];

  # A cluster with node n1 of role worker on /dev/vda plus the given modules.
  storageCluster =
    modules:
    cluster (
      [
        {
          chalkos.nodes.n1 = {
            role = "worker";
            storage.system.disk = lib.mkDefault "/dev/vda";
          };
        }
      ]
      ++ modules
    );
  # Node n1's storage options with the given definitions.
  nodeOptions =
    storage: (storageCluster [ { chalkos.nodes.n1.storage = storage; } ]).nodes.n1.storage;
  roleStorage = {
    system.disk = "/dev/sda";
    encryption.mode = "none";
    var.size = "4G";
    volumes.data = {
      disk = {
        model = "Samsung SSD 870*";
        type = "ssd";
      };
      size = "10G";
      format = "xfs";
      mountPoint = "/srv/data";
    };
  };
  # Node n1 of a role with roleStorage, plus the node's own storage definitions.
  withRoleStorage =
    storage:
    (cluster [
      {
        chalkos.roles.worker.storage = roleStorage;
        chalkos.nodes.n1 = {
          role = "worker";
          inherit storage;
        };
      }
    ]).nodes.n1.storage;
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
  testConsumerWithServiceDoesNotWarn = {
    expr = withConsumer.warnings;
    expected = [ ];
  };
  testConsumerWithoutServiceWarns = {
    expr = lib.any (lib.hasInfix "chalkos.node.consumers.orphan") withOrphanConsumer.warnings;
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
  testFlakeModulePassesNixpkgs = {
    expr =
      (lib.evalModules {
        modules = [
          flakeModule
          { options.flake = lib.mkOption { type = lib.types.lazyAttrsOf lib.types.raw; }; }
          {
            chalkos.nixpkgs = {
              inherit lib;
              marker = "https://10.0.0.2:6443";
            };
            chalkos.clusters.demo =
              { nixpkgs, ... }:
              {
                chalkos.cluster.endpoint = nixpkgs.marker;
              };
          }
        ];
      }).config.flake.chalkos.demo.cluster.endpoint;
    expected = "https://10.0.0.2:6443";
  };
  testStorageRequiresSystemDisk = {
    expr = fails (cluster [ { chalkos.nodes.n1.role = "worker"; } ]).nodes.n1.storage.system.disk;
    expected = true;
  };
  testStorageOptionDefaults = {
    expr = removeAttrs (nodeOptions { volumes.data = { }; }) [ "system" ];
    expected = {
      encryption = {
        mode = "tpm2";
        fallback = "recovery-key";
      };
      var = {
        size = null;
        encryption.mode = null;
      };
      volumes.data = {
        disk = null;
        size = null;
        format = "ext4";
        mountPoint = null;
        encryption.mode = null;
        repart = { };
      };
    };
  };
  testStorageRoleDefaults = {
    expr = withRoleStorage { };
    expected = roleStorage // {
      encryption = {
        mode = "none";
        fallback = "recovery-key";
      };
      var = {
        size = "4G";
        encryption.mode = null;
      };
      volumes.data = roleStorage.volumes.data // {
        encryption.mode = null;
        repart = { };
      };
    };
  };
  testStorageNodeOverridesRoleAttributes = {
    expr =
      let
        s = withRoleStorage {
          encryption.mode = "tpm2";
          volumes.data.size = "20G";
          volumes.extra.size = "1G";
        };
      in
      {
        inherit (s.system) disk;
        inherit (s.encryption) mode;
        inherit (s.volumes.data) size format mountPoint;
        volumes = lib.attrNames s.volumes;
      };
    expected = {
      disk = "/dev/sda";
      mode = "tpm2";
      size = "20G";
      format = "xfs";
      mountPoint = "/srv/data";
      volumes = [
        "data"
        "extra"
      ];
    };
  };
  testStorageNodeReplacesRoleDisk = {
    expr =
      (withRoleStorage {
        volumes.data.disk = {
          serial = "S1";
        };
      }).volumes.data.disk;
    expected = {
      serial = "S1";
    };
  };
  testStorageAcceptsDiskReferences = {
    expr = map (disk: (nodeOptions { system.disk = disk; }).system.disk) [
      "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001"
      {
        model = "Samsung SSD 990*";
        type = "nvme";
      }
      { size = ">= 1T"; }
      { wwn = "0x5002538e40a1b2c3"; }
    ];
    expected = [
      "/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100001"
      {
        model = "Samsung SSD 990*";
        type = "nvme";
      }
      { size = ">= 1T"; }
      { wwn = "0x5002538e40a1b2c3"; }
    ];
  };
  testStorageRejectsDiskReference = {
    expr = map (disk: fails (nodeOptions { system.disk = disk; }).system.disk) [
      "sda"
      { }
      { vendor = "x"; }
      { type = "tape"; }
      { size = "about 1T"; }
    ];
    expected = lib.replicate 5 true;
  };
  testStorageRejectsSize = {
    expr = fails (nodeOptions { var.size = "10 GB"; }).var.size;
    expected = true;
  };
  testStorageWarnsUnencryptedState = {
    expr =
      lib.any (lib.hasInfix "STATE")
        (storageCluster [
          { chalkos.nodes.n1.storage.encryption.mode = "none"; }
        ]).warnings;
    expected = true;
  };
  testStorageNoWarningWhenEncrypted = {
    expr = (storageCluster [ ]).warnings;
    expected = [ ];
  };
}
