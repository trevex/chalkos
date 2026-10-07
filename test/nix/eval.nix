# Evaluation tests for the cluster definition; returns lib.runTests failures (empty on success).
{
  lib,
  pkgs,
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
  # Node n1 with the given networkd configuration.
  networkCluster =
    network:
    cluster [
      {
        chalkos.nodes.n1 = {
          role = "worker";
          storage.system.disk = "/dev/vda";
          inherit network;
        };
      }
    ];
  storageOf = c: c.manifest.nodes.n1.identity.storage;
  # Node n1's rendered storage section with the given definitions.
  nodeStorage = storage: storageOf (storageCluster [ { chalkos.nodes.n1.storage = storage; } ]);
  invalidStorage = storage: fails (nodeStorage storage);
  storageErrors = (import ../../modules/cluster/storage.nix { inherit lib; }).errors;
  # Partition types hash the label; see modules/cluster/storage.nix.
  varType = "65f335d7-a1f7-f6df-b954-a97d9a5db9e6";
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
  testRoleRunsChalkd = {
    expr =
      let
        services = (role (cluster [ ])).systemd.services;
      in
      {
        chalkd = services.chalkd.wantedBy;
        identity = services.chalkos-identity.before;
      };
    expected = {
      chalkd = [ "multi-user.target" ];
      identity = [
        "sysinit.target"
        "network-pre.target"
        "systemd-networkd.service"
      ];
    };
  };
  testRoleCarriesOSCA = {
    expr =
      let
        pub = builtins.toFile "secrets.pub.json" ''{"version": 1, "osCA": {"certificate": "PEM"}}'';
      in
      {
        withCA = (role (cluster [ { chalkos.cluster.osCA = pub; } ])).environment.etc ? "chalkos/os-ca.crt";
        withoutCA = (role (cluster [ ])).environment.etc ? "chalkos/os-ca.crt";
      };
    expected = {
      withCA = true;
      withoutCA = false;
    };
  };
  testClusterBuildsInstaller = {
    expr =
      let
        pub = builtins.toFile "secrets.pub.json" ''{"version": 1, "osCA": {"certificate": "PEM"}}'';
        installer = (cluster [ { chalkos.cluster.osCA = pub; } ]).installer;
      in
      lib.isDerivation installer && lib.hasInfix "chalkos-installer" installer.name;
    expected = true;
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
    expr = removeAttrs twoNodes.manifest.nodes.n1.identity [ "storage" ];
    expected = {
      hostname = "n1";
      network = { };
      networkUnits = { };
      labels = { };
      taints = [
        {
          key = "dedicated";
          value = null;
          effect = "NoSchedule";
        }
      ];
      kubernetes = {
        nodeName = "n1";
        nodeIP = null;
        validSubnets = null;
      };
      extensions = {
        rack.location = "a1";
      };
    };
  };
  testNetworkUnits = {
    expr =
      (networkCluster {
        networks."10-uplink" = {
          name = "enp1s0";
          DHCP = "no";
          address = [ "10.0.0.11/24" ];
          gateway = [ "10.0.0.1" ];
          routes = [
            {
              Destination = "10.1.0.0/16";
              Gateway = "10.0.0.2";
            }
          ];
        };
        networks."20-off".enable = false;
        netdevs."30-vlan" = {
          netdevConfig = {
            Name = "vlan10";
            Kind = "vlan";
          };
          vlanConfig.Id = 10;
        };
        links."40-nic" = {
          matchConfig.MACAddress = "aa:bb:cc:dd:ee:01";
          linkConfig.Name = "uplink";
        };
      }).manifest.nodes.n1.identity.networkUnits;
    expected = {
      "10-uplink.network" = ''
        [Match]
        Name=enp1s0

        [Network]
        DHCP=no
        Address=10.0.0.11/24
        Gateway=10.0.0.1

        [Route]
        Destination=10.1.0.0/16
        Gateway=10.0.0.2

      '';
      "30-vlan.netdev" = ''
        [NetDev]
        Kind=vlan
        Name=vlan10

        [VLAN]
        Id=10

      '';
      "40-nic.link" = ''
        [Match]
        MACAddress=aa:bb:cc:dd:ee:01

        [Link]
        Name=uplink

      '';
    };
  };
  testNetworkRejectsUnknownKind = {
    expr = fails (networkCluster { network = { }; }).manifest.nodes.n1.identity.networkUnits;
    expected = true;
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
        enable = true;
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
        enable = true;
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
  testStorageDefaultLayout = {
    expr = nodeStorage { };
    expected = {
      disks.system = {
        ref = "/dev/vda";
        seed = "2bbba487-cd05-5f11-064a-2a1f1faff0e3";
        repart."50-var.conf" = ''
          [Partition]
          Encrypt=tpm2
          Format=ext4
          Label=var
          TPM2PCRs=7
          Type=${varType}
        '';
      };
      volumes.var = {
        disk = "system";
        label = "var";
        format = "ext4";
        mountPoint = "/var";
        encryption = "tpm2";
        size = null;
      };
      fallback = "recovery-key";
      encryption = "tpm2";
    };
  };
  testStorageVarSize = {
    expr = (nodeStorage { var.size = "8G"; }).disks.system.repart."50-var.conf";
    expected = ''
      [Partition]
      Encrypt=tpm2
      Format=ext4
      Label=var
      SizeMaxBytes=8G
      SizeMinBytes=8G
      TPM2PCRs=7
      Type=${varType}
    '';
  };
  testStorageUnencryptedVolume = {
    expr =
      (nodeStorage {
        var.size = "8G";
        volumes.plain = {
          mountPoint = "/srv/plain";
          encryption.mode = "none";
        };
      }).disks.system.repart."60-plain.conf";
    expected = ''
      [Partition]
      Format=ext4
      Label=plain
      Type=47422e04-fdc4-6cef-975b-d2f7794454ed
    '';
  };
  testStorageRawVolume = {
    expr =
      let
        s = nodeStorage {
          volumes.raw = {
            size = "1G";
            format = null;
          };
        };
      in
      {
        definition = s.disks.system.repart."60-raw.conf";
        volume = s.volumes.raw;
      };
    expected = {
      definition = ''
        [Partition]
        Encrypt=tpm2
        Label=raw
        SizeMaxBytes=1G
        SizeMinBytes=1G
        TPM2PCRs=7
        Type=357ddd11-f8dd-4a47-8d2b-9573d2595f0d
      '';
      volume = {
        disk = "system";
        label = "raw";
        format = null;
        mountPoint = null;
        encryption = "tpm2";
        size = "1G";
      };
    };
  };
  testStoragePassthrough = {
    expr =
      (nodeStorage {
        volumes.data = {
          size = "1G";
          repart = {
            Weight = 2000;
            CopyFiles = [
              "/a"
              "/b"
            ];
          };
        };
      }).disks.system.repart."60-data.conf";
    expected = ''
      [Partition]
      CopyFiles=/a
      CopyFiles=/b
      Encrypt=tpm2
      Format=ext4
      Label=data
      SizeMaxBytes=1G
      SizeMinBytes=1G
      TPM2PCRs=7
      Type=63dd1eb4-1dd2-0d0b-0c1e-27c204214ec8
      Weight=2000
    '';
  };
  testStorageVolumeOnOwnDisk = {
    expr =
      let
        s = nodeStorage {
          volumes.data = {
            disk = {
              serial = "chalk-data";
              size = ">= 1G";
            };
            format = "xfs";
            mountPoint = "/var/lib/data";
          };
        };
      in
      {
        inherit (s.disks.data) ref seed;
        files = lib.attrNames s.disks.data.repart;
        volume = s.volumes.data;
      };
    expected = {
      ref = {
        serial = "chalk-data";
        size = ">= 1G";
      };
      seed = "308311fb-9c22-3be4-b458-3ff560058604";
      files = [ "10-data.conf" ];
      volume = {
        disk = "data";
        label = "data";
        format = "xfs";
        mountPoint = "/var/lib/data";
        encryption = "tpm2";
        size = null;
      };
    };
  };
  testStorageNodeModeNone = {
    expr =
      let
        s = nodeStorage {
          encryption.mode = "none";
          var.size = "8G";
          volumes.secret = {
            mountPoint = "/srv/secret";
            encryption.mode = "tpm2";
          };
        };
      in
      {
        state = s.encryption;
        var = s.volumes.var.encryption;
        secret = s.volumes.secret.encryption;
        varEncrypted = lib.hasInfix "Encrypt=" s.disks.system.repart."50-var.conf";
      };
    expected = {
      state = "none";
      var = "none";
      secret = "tpm2";
      varEncrypted = false;
    };
  };
  testStorageVarModeOverride = {
    expr = (nodeStorage { var.encryption.mode = "none"; }).volumes.var.encryption;
    expected = "none";
  };
  testStorageFallback = {
    expr = (nodeStorage { encryption.fallback = "password"; }).fallback;
    expected = "password";
  };
  testStorageAllowsMountBelowVar = {
    expr =
      (nodeStorage {
        volumes.data = {
          size = "1G";
          mountPoint = "/var/lib/data";
        };
      }).volumes.data.mountPoint;
    expected = "/var/lib/data";
  };
  testStorageRejectsVolumeName = {
    expr = map invalidStorage [
      { volumes.Data.size = "1G"; }
      { volumes."-data".size = "1G"; }
      { volumes.${lib.concatStrings (lib.replicate 33 "a")}.size = "1G"; }
      { volumes.var.size = "1G"; }
      { volumes.state.size = "1G"; }
      { volumes.esp.size = "1G"; }
      { volumes.store.size = "1G"; }
      { volumes.store-verity.size = "1G"; }
      { volumes.system.size = "1G"; }
    ];
    expected = lib.replicate 9 true;
  };
  testStorageRejectsMountPoint = {
    expr =
      map
        (
          p:
          invalidStorage {
            volumes.data = {
              size = "1G";
              mountPoint = p;
            };
          }
        )
        [
          "srv/data"
          "/"
          "/nix"
          "/nix/store/x"
          "/state"
          "/state/x"
          "/var"
          "/srv/../nix"
          "/srv/"
          "/srv/my data"
          "/srv/d\\x2data"
          "/srv/dätä"
          "/srv/a:b"
          "/srv/a\nb"
        ];
    expected = lib.replicate 14 true;
  };
  testStorageAllowsMountPointCharacters = {
    expr =
      (nodeStorage {
        volumes.data = {
          size = "1G";
          mountPoint = "/srv/My_data-2.0";
        };
      }).volumes.data.mountPoint;
    expected = "/srv/My_data-2.0";
  };
  testStateHasItsOwnPartitionType = {
    expr = (role (cluster [ ])).systemd.repart.partitions."50-state".Type;
    expected = "480b7842-1236-1abb-0339-e5503b43122a";
  };
  testStorageRejectsDuplicateMountPoints = {
    expr = map invalidStorage [
      {
        volumes.a = {
          size = "1G";
          mountPoint = "/srv/data";
        };
        volumes.b = {
          size = "1G";
          mountPoint = "/srv/data";
        };
      }
      {
        volumes.a = {
          size = "1G";
          mountPoint = "/var";
        };
      }
      {
        volumes.a = {
          size = "1G";
          mountPoint = "/srv/data";
        };
        volumes.b = {
          enable = false;
          size = "1G";
          mountPoint = "/srv/data";
        };
      }
    ];
    expected = [
      true
      true
      false
    ];
  };
  testStorageDuplicateMountPointError = {
    expr = storageErrors (nodeOptions {
      volumes.a = {
        size = "1G";
        mountPoint = "/srv/data";
      };
      volumes.b = {
        size = "1G";
        mountPoint = "/srv/data";
      };
    });
    expected = [
      "volumes.a and volumes.b have the same mountPoint /srv/data"
    ];
  };
  testStorageRejectsMountedRawOrSwap = {
    expr = map invalidStorage [
      {
        volumes.data = {
          size = "1G";
          format = null;
          mountPoint = "/srv/data";
        };
      }
      {
        volumes.swap = {
          size = "1G";
          format = "swap";
          mountPoint = "/srv/swap";
        };
      }
    ];
    expected = [
      true
      true
    ];
  };
  testStorageRejectsReservedRepartKeys = {
    expr =
      map
        (
          key:
          invalidStorage {
            volumes.data = {
              size = "1G";
              repart.${key} = "x";
            };
          }
        )
        [
          "Type"
          "Label"
          "Encrypt"
          "Format"
          "SizeMinBytes"
          "SizeMaxBytes"
          "MountPoint"
        ];
    expected = lib.replicate 7 true;
  };
  testStorageRejectsTwoFillingVolumes = {
    expr = invalidStorage { volumes.data = { }; };
    expected = true;
  };
  testStorageRejectsSharedDisk = {
    expr = map invalidStorage [
      {
        volumes.a.disk = "/dev/sdb";
        volumes.b.disk = "/dev/sdb";
      }
      { volumes.a.disk = "/dev/vda"; }
    ];
    expected = [
      true
      true
    ];
  };
  testStorageNodeDropsRoleVolume = {
    expr =
      let
        c = cluster [
          {
            chalkos.roles.worker.storage.volumes.scratch = {
              size = "1G";
              mountPoint = "/srv/scratch";
            };
            chalkos.nodes.n1 = {
              role = "worker";
              storage.system.disk = "/dev/vda";
              storage.volumes.scratch.enable = false;
            };
            chalkos.nodes.n2 = {
              role = "worker";
              storage.system.disk = "/dev/vdb";
            };
          }
        ];
        s1 = c.manifest.nodes.n1.identity.storage;
        s2 = c.manifest.nodes.n2.identity.storage;
      in
      {
        n1Volumes = lib.attrNames s1.volumes;
        n1Files = lib.attrNames s1.disks.system.repart;
        n2HasScratch = lib.hasAttr "scratch" s2.volumes;
      };
    expected = {
      n1Volumes = [ "var" ];
      n1Files = [ "50-var.conf" ];
      n2HasScratch = true;
    };
  };
  testRoleKindDefaultsToWorker = {
    expr = {
      manifest = twoNodes.manifest.roles.worker.kind;
      image = (role (cluster [ ])).chalkos.role.kubernetes.kind;
    };
    expected = {
      manifest = "worker";
      image = "worker";
    };
  };
  testRoleWithoutKubernetes = {
    expr =
      let
        c = cluster [
          {
            chalkos.roles.worker.kubernetes.kind = null;
            chalkos.nodes.n1 = {
              role = "worker";
              storage.system.disk = "/dev/vda";
            };
          }
        ];
      in
      {
        manifest = c.manifest.roles.worker.kind;
        identity = c.manifest.nodes.n1.identity.kubernetes;
        image = (role c).chalkos.role.kubernetes.kind;
      };
    expected = {
      manifest = null;
      identity = null;
      image = null;
    };
  };
  testNodeIPDefaultsToFirstStaticAddress = {
    expr =
      (networkCluster {
        networks."20-b".address = [ "10.0.1.5/24" ];
        networks."10-a".address = [ "10.0.0.5/24" ];
      }).manifest.nodes.n1.identity.kubernetes;
    expected = {
      nodeName = "n1";
      nodeIP = "10.0.0.5";
      validSubnets = null;
    };
  };
  # Subnets that apply to a node replace the default from its static address: the node picks its
  # address at boot.
  testValidSubnets = {
    expr =
      let
        node = modules: (cluster modules).manifest.nodes.n1.identity.kubernetes;
        static = {
          chalkos.nodes.n1 = {
            role = "worker";
            storage.system.disk = "/dev/vda";
            network.networks."10-a".address = [ "10.0.0.5/24" ];
          };
        };
        clusterSubnets.chalkos.cluster.kubernetes.nodeIP.validSubnets = [
          "10.0.0.0/8"
          "!10.0.0.10/32"
        ];
      in
      {
        nodeSubnets = node [
          static
          { chalkos.nodes.n1.kubernetes.validSubnets = [ "192.168.100.0/24" ]; }
        ];
        clusterSubnets = node [
          static
          clusterSubnets
        ];
        # An empty list is the default filter, which the static address stands for.
        nodeOverridesCluster = node [
          static
          clusterSubnets
          { chalkos.nodes.n1.kubernetes.validSubnets = [ ]; }
        ];
        fixedWins = node [
          static
          clusterSubnets
          { chalkos.nodes.n1.kubernetes.nodeIP = "10.0.0.5"; }
        ];
        clusterFile =
          (builtins.fromJSON
            (role (cluster [ clusterSubnets ])).environment.etc."chalkos/kubernetes/cluster.json".text
          ).nodeIP;
        notASubnet = fails (node [
          static
          { chalkos.nodes.n1.kubernetes.validSubnets = [ "192.168.100.0" ]; }
        ]);
        notAClusterSubnet =
          fails
            (role (cluster [ { chalkos.cluster.kubernetes.nodeIP.validSubnets = [ "eth0" ]; } ]))
            .environment.etc."chalkos/kubernetes/cluster.json".text;
      };
    expected = {
      nodeSubnets = {
        nodeName = "n1";
        nodeIP = null;
        validSubnets = [ "192.168.100.0/24" ];
      };
      clusterSubnets = {
        nodeName = "n1";
        nodeIP = null;
        validSubnets = null;
      };
      nodeOverridesCluster = {
        nodeName = "n1";
        nodeIP = "10.0.0.5";
        validSubnets = [ ];
      };
      fixedWins = {
        nodeName = "n1";
        nodeIP = "10.0.0.5";
        validSubnets = null;
      };
      clusterFile = {
        validSubnets = [
          "10.0.0.0/8"
          "!10.0.0.10/32"
        ];
        timeout = 300;
      };
      notASubnet = true;
      notAClusterSubnet = true;
    };
  };
  testEndpointWarnsWithoutControlPlaneAddress = {
    expr =
      let
        warns =
          endpoint:
          lib.any (lib.hasInfix "must reach a control-plane node")
            (cluster [
              {
                chalkos.cluster.endpoint = lib.mkForce endpoint;
                chalkos.roles.cp.kubernetes.kind = "controlplane";
                chalkos.nodes.cp1 = {
                  role = "cp";
                  storage.system.disk = "/dev/vda";
                  kubernetes.nodeIP = "10.0.0.11";
                };
                chalkos.nodes.ip6 = {
                  role = "cp";
                  storage.system.disk = "/dev/vda";
                  kubernetes.nodeIP = "fd00::11";
                };
                chalkos.nodes.w1 = {
                  role = "worker";
                  storage.system.disk = "/dev/vda";
                  kubernetes.nodeIP = "10.0.0.20";
                };
              }
            ]).warnings;
        # A control-plane node that picks its address at boot may hold the endpoint's.
        picked =
          lib.any (lib.hasInfix "must reach a control-plane node")
            (cluster [
              {
                chalkos.cluster.endpoint = lib.mkForce "https://10.0.0.10:6443";
                chalkos.roles.cp.kubernetes.kind = "controlplane";
                chalkos.nodes.cp1 = {
                  role = "cp";
                  storage.system.disk = "/dev/vda";
                  kubernetes.nodeIP = "10.0.0.11";
                };
                chalkos.nodes.cp2 = {
                  role = "cp";
                  storage.system.disk = "/dev/vda";
                  kubernetes.validSubnets = [ "10.0.0.0/24" ];
                };
              }
            ]).warnings;
      in
      {
        inherit picked;
        vip = warns "https://10.0.0.10:6443";
        worker = warns "https://10.0.0.20:6443";
        controlPlane = warns "https://10.0.0.11:6443";
        controlPlaneWithoutPort = warns "https://10.0.0.11";
        ipv6 = warns "https://[fd00::10]:6443";
        ipv6ControlPlane = warns "https://[fd00::11]:6443";
        hostname = warns "https://k8s.example.com:6443";
      };
    expected = {
      picked = false;
      vip = true;
      worker = true;
      controlPlane = false;
      controlPlaneWithoutPort = false;
      ipv6 = true;
      ipv6ControlPlane = false;
      hostname = false;
    };
  };
  testKubernetesNodeRejectsSwap = {
    expr =
      let
        swapManifest =
          kind:
          (cluster [
            {
              chalkos.roles.worker.kubernetes.kind = kind;
              chalkos.nodes.n1 = {
                role = "worker";
                storage.system.disk = "/dev/vda";
                kubernetes.nodeIP = "10.0.0.5";
                storage.volumes.swap = {
                  size = "1G";
                  format = "swap";
                };
              };
            }
          ]).manifest;
      in
      {
        worker = fails (swapManifest "worker");
        controlPlane = fails (swapManifest "controlplane");
        withoutKubernetes = fails (swapManifest null);
        disabled =
          fails
            (cluster [
              {
                chalkos.roles.worker.storage.volumes.swap = {
                  size = "1G";
                  format = "swap";
                };
                chalkos.nodes.n1 = {
                  role = "worker";
                  storage.system.disk = "/dev/vda";
                  storage.volumes.swap.enable = false;
                };
              }
            ]).manifest;
      };
    expected = {
      worker = true;
      controlPlane = true;
      withoutKubernetes = false;
      disabled = false;
    };
  };
  testKubernetesNodeRejectsRestrictedLabels = {
    expr =
      let
        rejects =
          kind: label:
          fails
            (cluster [
              {
                chalkos.roles.worker.kubernetes.kind = kind;
                chalkos.nodes.n1 = {
                  role = "worker";
                  storage.system.disk = "/dev/vda";
                  labels.${label} = "x";
                };
              }
            ]).manifest;
      in
      {
        refused = map (rejects "worker") [
          "kubernetes.io/role"
          "node-role.kubernetes.io/control-plane"
          "beta.kubernetes.io/fluentd"
          "k8s.io/team"
          "storage.k8s.io/class"
        ];
        allowed = map (rejects "worker") [
          "kubernetes.io/hostname"
          "kubernetes.io/arch"
          "kubernetes.io/os"
          "beta.kubernetes.io/arch"
          "beta.kubernetes.io/os"
          "beta.kubernetes.io/instance-type"
          "failure-domain.beta.kubernetes.io/region"
          "failure-domain.beta.kubernetes.io/zone"
          "topology.kubernetes.io/region"
          "topology.kubernetes.io/zone"
          "node.kubernetes.io/instance-type"
          "node.kubernetes.io/storage"
          "kubelet.kubernetes.io/pool"
          "a.node.kubernetes.io/b"
          "a.kubelet.kubernetes.io/b"
          "example.com/team"
          "team"
        ];
        withoutKubernetes = rejects null "node-role.kubernetes.io/control-plane";
      };
    expected = {
      refused = lib.replicate 5 true;
      allowed = lib.replicate 17 false;
      withoutKubernetes = false;
    };
  };
  # A control-plane node picks its address at boot like any other node.
  testControlPlaneWithoutNodeIP = {
    expr =
      (cluster [
        {
          chalkos.roles.worker.kubernetes.kind = "controlplane";
          chalkos.nodes.n1 = {
            role = "worker";
            storage.system.disk = "/dev/vda";
          };
        }
      ]).manifest.nodes.n1.identity.kubernetes;
    expected = {
      nodeName = "n1";
      nodeIP = null;
      validSubnets = null;
    };
  };
  testKubernetesOptionDefaults = {
    expr = removeAttrs (cluster [ ]).cluster.kubernetes [
      "package"
      "extraArgs"
      "addons"
    ];
    expected = {
      podCIDR = "10.244.0.0/16";
      serviceCIDR = "10.96.0.0/12";
      dnsIP = "10.96.0.10";
      domain = "cluster.local";
      allowSchedulingOnControlPlanes = false;
      nodeIP = {
        validSubnets = [ ];
        timeout = 300;
      };
      images = {
        etcd = "registry.k8s.io/etcd:3.7.0-0";
        pause = "registry.k8s.io/pause:3.10.2";
        coredns = "registry.k8s.io/coredns/coredns:v1.14.6";
      };
    };
  };
  testRejectsUnknownComponentFlags = {
    expr = fails (cluster [ { chalkos.cluster.kubernetes.extraArgs.kube-dns.v = "2"; } ]).cluster;
    expected = true;
  };
  testBuiltinManifests = {
    expr =
      let
        k =
          (cluster [
            {
              chalkos.cluster.kubernetes = {
                podCIDR = "10.250.0.0/16";
                dnsIP = "10.100.0.10";
                domain = "lab.local";
              };
            }
          ]).cluster.kubernetes;
        named = kind: name: lib.findFirst (m: m.kind == kind && m.metadata.name == name) null k.addons;
      in
      {
        # RBAC first, then kube-proxy, flannel and CoreDNS.
        order = map (m: "${m.kind}/${m.metadata.name}") k.addons;
        kubeProxyImage =
          (builtins.head (named "DaemonSet" "kube-proxy").spec.template.spec.containers).image;
        flannelNetwork =
          (builtins.fromJSON (named "ConfigMap" "kube-flannel-cfg").data."net-conf.json").Network;
        dnsIP = (named "Service" "kube-dns").spec.clusterIP;
        corefileDomain = lib.hasInfix "kubernetes lab.local in-addr.arpa" (named "ConfigMap" "coredns")
        .data.Corefile;
      };
    expected = {
      order = [
        "ClusterRoleBinding/chalkos:cluster-admins"
        "ClusterRoleBinding/chalkos:selfnodeclient"
        "ClusterRoleBinding/chalkos:apiserver-kubelet"
        "ServiceAccount/kube-proxy"
        "ClusterRoleBinding/chalkos:node-proxier"
        "ConfigMap/kube-proxy"
        "DaemonSet/kube-proxy"
        "Namespace/kube-flannel"
        "ClusterRole/flannel"
        "ClusterRoleBinding/flannel"
        "ServiceAccount/flannel"
        "ConfigMap/kube-flannel-cfg"
        "DaemonSet/kube-flannel-ds"
        "ServiceAccount/coredns"
        "ClusterRole/system:coredns"
        "ClusterRoleBinding/system:coredns"
        "ConfigMap/coredns"
        "Deployment/coredns"
        "Service/kube-dns"
      ];
      kubeProxyImage = "registry.k8s.io/kube-proxy:v1.37.1";
      flannelNetwork = "10.250.0.0/16";
      dnsIP = "10.100.0.10";
      corefileDomain = true;
    };
  };
  testCNIProviderNone = {
    expr =
      lib.any (m: m.metadata.namespace or "" == "kube-flannel")
        (cluster [
          { chalkos.cni.provider = "none"; }
        ]).cluster.kubernetes.addons;
    expected = false;
  };
  testRBACBindsChalkosIdentities = {
    expr = lib.listToAttrs (
      map (m: lib.nameValuePair m.metadata.name (builtins.head m.subjects).name) (
        lib.filter (m: lib.hasPrefix "chalkos:" m.metadata.name && m.kind == "ClusterRoleBinding")
          (cluster [ ]).cluster.kubernetes.addons
      )
    );
    expected = {
      "chalkos:cluster-admins" = "chalkos:cluster-admins";
      "chalkos:selfnodeclient" = "system:nodes";
      "chalkos:apiserver-kubelet" = "chalkos:kube-apiserver-kubelet-client";
      "chalkos:node-proxier" = "kube-proxy";
    };
  };
  testKubernetesOnlyInKubernetesRoles = {
    expr =
      let
        image = kind: role (cluster [ { chalkos.roles.worker.kubernetes.kind = kind; } ]);
        services = config: {
          kubelet = config.systemd.services ? kubelet;
          containerd = config.virtualisation.containerd.enable;
          prepare = config.systemd.services ? chalkos-kubernetes;
        };
      in
      {
        none = services (image null);
        worker = services (image "worker");
        prepare = lib.hasSuffix "/bin/chalkd prepare-kubernetes" (image "worker")
        .systemd.services.chalkos-kubernetes.serviceConfig.ExecStart;
      };
    expected = {
      none = {
        kubelet = false;
        containerd = false;
        prepare = false;
      };
      worker = {
        kubelet = true;
        containerd = true;
        prepare = true;
      };
      prepare = true;
    };
  };
  testControlPlaneImage = {
    expr =
      let
        c = cluster [
          {
            chalkos.roles.worker.kubernetes.kind = "controlplane";
            chalkos.cluster.manifests = [
              {
                apiVersion = "v1";
                kind = "Namespace";
                metadata.name = "apps";
              }
            ];
          }
        ];
        config = role c;
        clusterFile = builtins.fromJSON config.environment.etc."chalkos/kubernetes/cluster.json".text;
      in
      {
        inherit (clusterFile) kind version endpoint;
        apiServer = clusterFile.images.kubeAPIServer;
        firewall = lib.elem 6443 config.networking.firewall.allowedTCPPorts;
        # The built-in objects, then the cluster's own.
        manifests =
          builtins.fromJSON config.environment.etc."chalkos/kubernetes/manifests.json".text
          == c.cluster.kubernetes.addons ++ c.cluster.manifests;
        etcdDir = lib.elem "d /var/lib/etcd 0700 root root -" config.systemd.tmpfiles.rules;
      };
    expected = {
      kind = "controlplane";
      version = "1.37.1";
      endpoint = "https://10.0.0.1:6443";
      apiServer = "registry.k8s.io/kube-apiserver:v1.37.1";
      firewall = true;
      manifests = true;
      etcdDir = true;
    };
  };
  testWorkerImageHasNoManifests = {
    expr =
      let
        config = role (cluster [ ]);
      in
      {
        manifests = config.environment.etc ? "chalkos/kubernetes/manifests.json";
        apiServerPort = lib.elem 6443 config.networking.firewall.allowedTCPPorts;
      };
    expected = {
      manifests = false;
      apiServerPort = false;
    };
  };
  testCNIPluginsFollowProvider = {
    expr =
      let
        dirs =
          provider:
          (role (cluster [ { chalkos.cni.provider = provider; } ]))
          .virtualisation.containerd.settings.plugins."io.containerd.cri.v1.runtime".cni.bin_dirs;
      in
      {
        flannel = dirs "flannel";
        none = dirs "none";
      };
    expected = {
      flannel = [
        "${pkgs.cni-plugins}/bin"
        "${pkgs.cni-plugin-flannel}/bin"
      ];
      none = [ "${pkgs.cni-plugins}/bin" ];
    };
  };
  testRegistryMirrors = {
    expr =
      let
        etc =
          (role (cluster [
            {
              chalkos.cluster.registries.allowPlainHTTP = true;
              chalkos.cluster.registries.mirrors = {
                "docker.io" = [
                  "http://10.0.2.100:5000"
                  "https://mirror.example.com"
                ];
                "ghcr.io" = [ "http://10.0.2.100:5000" ];
              };
            }
          ])).environment.etc;
      in
      {
        docker = etc."containerd/certs.d/docker.io/hosts.toml".text;
        ghcr = etc."containerd/certs.d/ghcr.io/hosts.toml".text;
      };
    expected = {
      docker = ''
        server = "https://registry-1.docker.io"

        [host."http://10.0.2.100:5000"]
          capabilities = ["pull", "resolve"]

        [host."https://mirror.example.com"]
          capabilities = ["pull", "resolve"]
      '';
      ghcr = ''
        server = "https://ghcr.io"

        [host."http://10.0.2.100:5000"]
          capabilities = ["pull", "resolve"]
      '';
    };
  };
  testRegistryMirrorsRequireHTTPS = {
    expr =
      let
        hostsToml =
          registries:
          (role (cluster [ { chalkos.cluster = { inherit registries; }; } ]))
          .environment.etc."containerd/certs.d/docker.io/hosts.toml".text;
      in
      {
        plain = fails (hostsToml {
          mirrors."docker.io" = [
            "https://mirror.example.com"
            "http://10.0.2.100:5000"
          ];
        });
        allowed = fails (hostsToml {
          allowPlainHTTP = true;
          mirrors."docker.io" = [ "http://10.0.2.100:5000" ];
        });
        https = fails (hostsToml {
          mirrors."docker.io" = [ "https://mirror.example.com" ];
        });
      };
    expected = {
      plain = true;
      allowed = false;
      https = false;
    };
  };
  testKubeletConfiguration = {
    expr =
      let
        config = role (cluster [
          {
            chalkos.cluster.kubernetes = {
              dnsIP = "10.100.0.10";
              domain = "lab.local";
              extraArgs.kubelet.v = "2";
            };
          }
        ]);
        kubelet = builtins.fromJSON config.environment.etc."chalkos/kubernetes/kubelet.json".text;
      in
      {
        inherit (kubelet)
          clusterDNS
          clusterDomain
          cgroupDriver
          rotateCertificates
          serverTLSBootstrap
          staticPodPath
          resolvConf
          ;
        anonymous = kubelet.authentication.anonymous.enabled;
        authorization = kubelet.authorization.mode;
        inherit (kubelet) readOnlyPort tlsMinVersion protectKernelDefaults;
        # protectKernelDefaults makes the kubelet fail unless the kernel has these values.
        sysctl = lib.getAttrs [
          "vm.overcommit_memory"
          "vm.panic_on_oom"
          "kernel.panic"
          "kernel.panic_on_oops"
          "kernel.keys.root_maxkeys"
          "kernel.keys.root_maxbytes"
        ] config.boot.kernel.sysctl;
        extraArg = lib.hasSuffix "$KUBELET_ARGS --v=2" config.systemd.services.kubelet.serviceConfig.ExecStart;
        noFullPackage =
          !lib.hasInfix "-kubernetes-1.37.1/" config.systemd.services.kubelet.serviceConfig.ExecStart;
      };
    expected = {
      clusterDNS = [ "10.100.0.10" ];
      clusterDomain = "lab.local";
      cgroupDriver = "systemd";
      rotateCertificates = true;
      serverTLSBootstrap = true;
      staticPodPath = "/run/chalkos/kubernetes/manifests";
      resolvConf = "/run/systemd/resolve/resolv.conf";
      anonymous = false;
      authorization = "Webhook";
      readOnlyPort = 0;
      tlsMinVersion = "VersionTLS12";
      protectKernelDefaults = true;
      sysctl = {
        "vm.overcommit_memory" = 1;
        "vm.panic_on_oom" = 0;
        "kernel.panic" = 10;
        "kernel.panic_on_oops" = 1;
        "kernel.keys.root_maxkeys" = 1000000;
        "kernel.keys.root_maxbytes" = 25000000;
      };
      extraArg = true;
      noFullPackage = true;
    };
  };
  testAPIServerExtraArgsCannotOverrideAuthentication = {
    expr =
      let
        clusterFile =
          flags:
          (role (cluster [
            {
              chalkos.roles.worker.kubernetes.kind = "controlplane";
              chalkos.cluster.kubernetes.extraArgs.kube-apiserver = flags;
            }
          ])).environment.etc."chalkos/kubernetes/cluster.json".text;
      in
      {
        authorization = fails (clusterFile {
          authorization-mode = "AlwaysAllow";
        });
        anonymous = fails (clusterFile {
          anonymous-auth = "true";
        });
        authenticationConfig = fails (clusterFile {
          authentication-config = "/tmp/a.json";
        });
        bootstrapTokens = fails (clusterFile {
          enable-bootstrap-token-auth = "true";
        });
        other = fails (clusterFile {
          audit-log-maxage = "30";
        });
      };
    expected = {
      authorization = true;
      anonymous = true;
      authenticationConfig = true;
      bootstrapTokens = true;
      other = false;
    };
  };
  # VXLAN is accepted only to the address chalkd picks: its preparation fills the firewall's
  # chain, which a restarted firewall fills again from the same file.
  testFlannelVXLANOnlyToNodeIP = {
    expr =
      let
        vxlan =
          modules:
          let
            config = role (cluster modules);
            inherit (config.networking) firewall;
            prepare = config.systemd.services.chalkos-kubernetes;
            fills = command: lib.hasInfix "chalkos-vxlan-rule /run/chalkos/kubernetes/node-ip" command;
          in
          {
            open = lib.elem 8472 firewall.allowedUDPPorts;
            chain = lib.hasInfix "-A nixos-fw -j chalkos-vxlan" firewall.extraCommands;
            firewallFills = fills firewall.extraCommands;
            # Emptied before the address is picked, filled after.
            emptied = lib.hasSuffix "/bin/chalkos-vxlan-rule" (prepare.serviceConfig.ExecStartPre or "");
            prepareFills = fills (prepare.serviceConfig.ExecStartPost or "");
            afterFirewall = lib.elem "firewall.service" prepare.after;
            # The firewall no longer reads the identity.
            firewallAfterIdentity = lib.elem "chalkos-identity.service" config.systemd.services.firewall.after;
          };
      in
      {
        flannel = vxlan [ { chalkos.cni.provider = "flannel"; } ];
        none = vxlan [ { chalkos.cni.provider = "none"; } ];
        # Without a firewall there is no chain to fill.
        withoutFirewall = removeAttrs (vxlan [
          { chalkos.roles.worker.nixosModules = [ { networking.firewall.enable = false; } ]; }
        ]) [ "firewallAfterIdentity" ];
      };
    expected = {
      flannel = {
        open = false;
        chain = true;
        firewallFills = true;
        emptied = true;
        prepareFills = true;
        afterFirewall = true;
        firewallAfterIdentity = false;
      };
      none = {
        open = false;
        chain = false;
        firewallFills = false;
        emptied = false;
        prepareFills = false;
        afterFirewall = false;
        firewallAfterIdentity = false;
      };
      withoutFirewall = {
        open = false;
        chain = true;
        firewallFills = true;
        emptied = false;
        prepareFills = false;
        afterFirewall = false;
      };
    };
  };
  # chalkd is the only way to the node, so it answers while the preparation waits for the node's
  # address and after it failed; it waits for the preparation's marker instead.
  testChalkdStartsWithoutKubernetesPreparation = {
    expr =
      let
        chalkd = (role (cluster [ ])).systemd.services.chalkd;
      in
      {
        after = lib.elem "chalkos-kubernetes.service" chalkd.after;
        depends = lib.any (lib.elem "chalkos-kubernetes.service") [
          chalkd.requires
          chalkd.wants
          chalkd.bindsTo
          chalkd.requisite
        ];
      };
    expected = {
      after = false;
      depends = false;
    };
  };
}
