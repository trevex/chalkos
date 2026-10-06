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
    ];
    expected = lib.replicate 8 true;
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
}
