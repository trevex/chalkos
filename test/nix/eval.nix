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
  role = c: c.roles.worker.nixos.metal.config;
  # The cluster's Kubernetes settings as the manifest carries them into every Kubernetes node's
  # Kubernetes document; evaluating them runs the options' checks.
  kubernetesSettings = modules: (cluster modules).manifest.kubernetes;
  fails = value: !(builtins.tryEval (builtins.deepSeq value true)).success;
  defaultTimeServers = [
    "ptbtime1.ptb.de"
    "ptbtime2.ptb.de"
    "ptbtime3.ptb.de"
  ];

  demoExtension =
    { config, lib, ... }:
    {
      options.chalkos.demo.enable = lib.mkEnableOption "demo";
      config.chalkos.demo.enable = true;
    };

  rackExtension =
    { lib, ... }:
    {
      options.chalkos.nodes = lib.mkOption {
        type = lib.types.attrsOf (
          lib.types.submodule (
            { config, ... }:
            {
              options.rack.location = lib.mkOption { type = lib.types.str; };
              config.documents = [
                {
                  apiVersion = "rack.example.org/v1alpha1";
                  kind = "Rack";
                  metadata.name = "rack";
                  spec.location = config.rack.location;
                }
              ];
            }
          )
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
        config = services.chalkos-config.before;
        # The VIP holder announces its addresses with gratuitous ARP and neighbour advertisements,
        # which need raw sockets, and no capability is taken away.
        packets = lib.elem "AF_PACKET" services.chalkd.serviceConfig.RestrictAddressFamilies;
        capabilities =
          services.chalkd.serviceConfig ? CapabilityBoundingSet || services.chalkd.serviceConfig ? User;
      };
    expected = {
      packets = true;
      capabilities = false;
      chalkd = [ "multi-user.target" ];
      config = [
        "sysinit.target"
        "network-pre.target"
        "systemd-networkd.service"
      ];
    };
  };
  # Nodes and the installer resolve names through DNS alone: systemd-resolved answers no LLMNR or
  # multicast DNS queries on their links.
  testNodesResolveNoLinkLocalNames = {
    expr =
      let
        c = cluster [ ];
        conf = config: lib.splitString "\n" config.environment.etc."systemd/resolved.conf".text;
      in
      map
        (lines: {
          llmnr = lib.elem "LLMNR=false" lines;
          mdns = lib.elem "MulticastDNS=false" lines;
        })
        [
          (conf (role c))
          (conf c.installer.nixos.config)
        ];
    expected = lib.replicate 2 {
      llmnr = true;
      mdns = true;
    };
  };
  # The OS CA is an input of the images, which the role images and the installer carry; the
  # cluster settings the images receive do not hold it.
  testImagesCarryOSCA = {
    expr =
      let
        pub = builtins.toFile "secrets.pub.json" ''{"version": 3, "osCA": {"certificate": "PEM"}}'';
        c = cluster [ { chalkos.osCA = pub; } ];
      in
      {
        withCA = (role c).environment.etc ? "chalkos/os-ca.crt";
        installer = c.installer.nixos.config.environment.etc ? "chalkos/os-ca.crt";
        withoutCA = (role (cluster [ ])).environment.etc ? "chalkos/os-ca.crt";
        setting = (role c).chalkos ? osCA;
      };
    expected = {
      withCA = true;
      installer = true;
      withoutCA = false;
      setting = false;
    };
  };
  # A cluster's runtime settings and its nodes reach the nodes as config, so two clusters that
  # differ in their nodes and in the runtime settings below build the same role images and
  # installer. The VIP's mode has one value, and the Kubernetes release and the CNI provider are
  # image inputs, so they are left alone; the OS CA is an image input too, and the same in both.
  testImagesAreClusterAgnostic = {
    expr =
      let
        osCA = builtins.toFile "secrets.pub.json" ''{"version": 3, "osCA": {"certificate": "PEM"}}'';
        base = cluster [
          {
            chalkos.osCA = osCA;
            chalkos.nodes.n1 = {
              role = "worker";
              kubernetes.kind = "controlplane";
              storage.system.disk = "/dev/vda";
              kubernetes.nodeIP = "10.0.0.1";
            };
          }
        ];
        other = mkCluster {
          modules = [
            {
              chalkos.osCA = osCA;
              chalkos.cluster = {
                name = "u";
                endpoint = "https://[fd00::10]:6443";
                kubernetes = {
                  ipFamilies = [
                    "ipv6"
                    "ipv4"
                  ];
                  podCIDRs = {
                    ipv4 = "10.32.0.0/16";
                    ipv6 = "fd00:32::/56";
                  };
                  serviceCIDRs = {
                    ipv4 = "10.16.0.0/16";
                    ipv6 = "fd00:16::/112";
                  };
                  dnsIPs = {
                    ipv4 = "10.16.0.53";
                    ipv6 = "fd00:16::35";
                  };
                  nodeCIDRMaskSizes = {
                    ipv4 = 26;
                    ipv6 = 72;
                  };
                  domain = "u.example";
                  allowSchedulingOnControlPlanes = true;
                  vip = {
                    addresses = [ "fd00::10" ];
                    interface = "bond0";
                  };
                  vxlanSourceSubnets = [
                    "fd00:100::/64"
                    "192.168.100.0/24"
                  ];
                  nodeIP = {
                    validSubnets = [
                      "fd00:100::/64"
                      "192.168.100.0/24"
                    ];
                    timeout = 30;
                  };
                  extraArgs = {
                    kubelet.v = "2";
                    etcd.log-level = "debug";
                    kube-apiserver.audit-log-maxage = "30";
                    kube-controller-manager.v = "2";
                    kube-scheduler.v = "2";
                  };
                };
                registries = {
                  mirrors."docker.io" = [ "http://mirror.example" ];
                  allowPlainHTTP = true;
                };
                manifests = [
                  {
                    apiVersion = "v1";
                    kind = "Namespace";
                    metadata.name = "apps";
                  }
                ];
              };
              chalkos.time = {
                servers = [ { host = "ntp.example"; } ];
                dhcpServers = true;
              };
              chalkos.cni.flannel.mtu = 1430;
              chalkos.roles.worker = { };
              chalkos.nodes = {
                x1 = {
                  role = "worker";
                  storage.system.disk = "/dev/sda";
                  network.networks."10-uplink" = {
                    matchConfig.Name = "eth0";
                    address = [
                      "fd00:100::21/64"
                      "192.168.100.21/24"
                    ];
                  };
                  labels.team = "a";
                };
                x2 = {
                  role = "worker";
                  kubernetes.kind = "controlplane";
                  storage.system.disk = "/dev/nvme0n1";
                  kubernetes.nodeIPs = [
                    "fd00:100::22"
                    "192.168.100.22"
                  ];
                };
              };
            }
          ];
        };
        same = f: f base == f other;
      in
      {
        # Both evaluate, so the comparison is of two real clusters.
        manifests = (builtins.deepSeq base.manifest true) && (builtins.deepSeq other.manifest true);
        metal = same (c: c.roles.worker.images.metal.drvPath);
        kvm = same (c: c.roles.worker.images.kvm.drvPath);
        installer = same (c: c.installer.image.drvPath);
      };
    expected = {
      manifests = true;
      metal = true;
      kvm = true;
      installer = true;
    };
  };
  testOSCARefusesOlderSecrets = {
    expr =
      let
        osCA =
          version:
          (role (cluster [
            {
              chalkos.osCA = builtins.toFile "secrets.pub.json" ''{"version": ${toString version}, "osCA": {"certificate": "PEM"}}'';
            }
          ])).environment.etc."chalkos/os-ca.crt".source.drvPath;
      in
      map (v: fails (osCA v)) [
        1
        2
        3
      ];
    expected = [
      true
      true
      false
    ];
  };
  testTimeServers = {
    expr =
      let
        timeOf =
          modules:
          lib.mapAttrs (_: n: n.identity.time.servers)
            (cluster (
              [
                {
                  chalkos.nodes.n1 = {
                    role = "worker";
                    storage.system.disk = "/dev/vda";
                  };
                  chalkos.nodes.n2 = {
                    role = "worker";
                    storage.system.disk = "/dev/vda";
                    time.servers = [
                      {
                        host = "10.0.2.2";
                        nts = false;
                        port = 12300;
                      }
                    ];
                  };
                }
              ]
              ++ modules
            )).manifest.nodes;
      in
      {
        default = timeOf [ ];
        cluster = timeOf [ { chalkos.time.servers = [ { host = "time.example.org"; } ]; } ];
        invalidHost = fails (timeOf [ { chalkos.time.servers = [ { host = "a b"; } ]; } ]);
        # A newline would end chrony's source line and start another.
        newlineHost = fails (timeOf [
          { chalkos.time.servers = [ { host = "time.example.org\nserver evil.example.org"; } ]; }
        ]);
        newlineNodeHost = fails (timeOf [
          { chalkos.nodes.n1.time.servers = [ { host = "time.example.org\nserver evil.example.org"; } ]; }
        ]);
      };
    expected = {
      default = {
        n1 = map (host: {
          inherit host;
          nts = true;
          port = null;
        }) defaultTimeServers;
        n2 = [
          {
            host = "10.0.2.2";
            nts = false;
            port = 12300;
          }
        ];
      };
      cluster = {
        n1 = [
          {
            host = "time.example.org";
            nts = true;
            port = null;
          }
        ];
        n2 = [
          {
            host = "10.0.2.2";
            nts = false;
            port = 12300;
          }
        ];
      };
      invalidHost = true;
      newlineHost = true;
      newlineNodeHost = true;
    };
  };
  testChrony = {
    expr =
      let
        image = modules: role (cluster modules);
        summary = c: {
          chrony = c.services.chrony.enable;
          timesyncd = c.services.timesyncd.enable;
          servers = c.services.chrony.servers;
          makestep = with c.services.chrony.makestep; [
            enable
            threshold
            limit
          ];
          sourcedirs = lib.filter (lib.hasPrefix "sourcedir") (
            lib.splitString "\n" c.services.chrony.extraConfig
          );
          certTimeCheck = lib.hasInfix "nocerttimecheck 1" c.services.chrony.extraConfig;
          ntsDump = lib.hasInfix "ntsdumpdir /var/lib/chrony" c.services.chrony.extraConfig;
          dhcpPath = c.systemd.paths ? chalkos-chrony-dhcp;
          # The servers DHCP announced at boot are read once networkd is up, not only on a change.
          dhcpAtBoot =
            c.systemd.services ? chalkos-chrony-dhcp
            && lib.elem "multi-user.target" c.systemd.services.chalkos-chrony-dhcp.wantedBy
            && lib.elem "systemd-networkd.service" c.systemd.services.chalkos-chrony-dhcp.after;
          dhcpDir = lib.elem "d /run/chalkos/chrony-dhcp 0755 root root - -" c.systemd.tmpfiles.rules;
          dhcpConsumer =
            (builtins.fromJSON c.environment.etc."chalkos/consumers.json".text).chalkos-chrony-dhcp.kinds;
          dhcpReadsConfig = lib.hasInfix "chalkcfg get TimeSync" c.systemd.services.chalkos-chrony-dhcp.script;
          # Active after it ran, so chalkd's try-restart runs it again; a link's change restarts it.
          dhcpRerunsOnChange =
            c.systemd.services.chalkos-chrony-dhcp.serviceConfig.RemainAfterExit
            && c.systemd.paths.chalkos-chrony-dhcp.pathConfig.Unit == "chalkos-chrony-dhcp-links.service"
            && lib.hasSuffix "systemctl restart chalkos-chrony-dhcp.service" c.systemd.services.chalkos-chrony-dhcp-links.serviceConfig.ExecStart;
          chalkdHasChronyc = lib.elem c.services.chrony.package c.systemd.services.chalkd.path;
          chalkdProtectsClock = c.systemd.services.chalkd.serviceConfig.ProtectClock;
        };
      in
      {
        default = summary (image [ ]);
        # The units are always there; TimeSync.spec.dhcp decides at runtime.
        sameWithDhcpOption =
          summary (image [ { chalkos.time.dhcpServers = true; } ]) == summary (image [ ]);
      };
    expected =
      let
        base = {
          chrony = true;
          timesyncd = false;
          servers = [ ];
          makestep = [
            true
            1
            3
          ];
          sourcedirs = [
            "sourcedir /run/chalkos/chrony"
            "sourcedir /run/chalkos/chrony-dhcp"
          ];
          certTimeCheck = true;
          ntsDump = true;
          dhcpPath = true;
          dhcpAtBoot = true;
          dhcpDir = true;
          dhcpConsumer = [ "TimeSync" ];
          dhcpReadsConfig = true;
          dhcpRerunsOnChange = true;
          chalkdHasChronyc = true;
          chalkdProtectsClock = true;
        };
      in
      {
        default = base;
        sameWithDhcpOption = true;
      };
  };
  # The renewal hook of chalkd exists on the test image alone.
  testRenewalHookOnTestImagesOnly = {
    expr =
      let
        hook = c: c.systemd.services.chalkd.environment ? CHALKD_TEST_RENEW_ON_APPLY_CONFIG;
      in
      {
        role = hook (role (cluster [ ]));
        testImage = hook (
          role (cluster [ { chalkos.roles.worker.nixosModules = [ ../../modules/testing/test-image.nix ]; } ])
        );
      };
    expected = {
      role = false;
      testImage = true;
    };
  };
  testClusterBuildsInstaller = {
    expr =
      let
        pub = builtins.toFile "secrets.pub.json" ''{"version": 3, "osCA": {"certificate": "PEM"}}'';
        installer = (cluster [ { chalkos.osCA = pub; } ]).installer.image;
      in
      lib.isDerivation installer && lib.hasInfix "chalkos-installer" installer.name;
    expected = true;
  };
  # The installer's modules reach its image, whose console is on the screen and the first serial
  # port; it belongs to no role and no platform, as it installs images of every one.
  testInstallerModules = {
    expr =
      let
        c = cluster [
          {
            chalkos.installer.nixosModules = [
              {
                systemd.network.networks."10-uplink" = {
                  matchConfig.MACAddress = "52:54:00:12:34:56";
                  address = [ "10.0.0.5/24" ];
                };
                chalkos.kernel.moduleGroups = [ "wireless" ];
              }
            ];
          }
        ];
        inherit (c.installer.nixos) config;
      in
      {
        uplink = config.systemd.network.networks."10-uplink".matchConfig.MACAddress;
        wireless = lib.elem "wireless" config.chalkos.kernel.moduleGroups;
        consoles = lib.filter (lib.hasPrefix "console=") config.boot.kernelParams;
        osRelease = lib.filterAttrs (
          k: _: lib.hasPrefix "CHALKOS_" k
        ) config.system.nixos.extraOSReleaseArgs;
        image = c.installer.image.drvPath == config.system.build.chalkosInstaller.drvPath;
      };
    expected = {
      uplink = "52:54:00:12:34:56";
      wireless = true;
      consoles = [
        "console=tty0"
        "console=ttyS0,115200"
      ];
      osRelease = {
        CHALKOS_BOOT_TRIES = "3";
      };
      image = true;
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
  # A role is built for each platform, metal and kvm unless a definition adds one; each image names
  # its platform in os-release and carries that platform's console and agents alone.
  testPlatformImages = {
    expr =
      let
        c = cluster [ ];
        summary = c: {
          platform = c.system.nixos.extraOSReleaseArgs.CHALKOS_PLATFORM;
          consoles = lib.filter (lib.hasPrefix "console=") c.boot.kernelParams;
          guestAgent = c.services.qemuGuest.enable;
          condition = c.systemd.services.qemu-guest-agent.unitConfig.ConditionVirtualization or null;
        };
      in
      {
        images = lib.attrNames c.roles.worker.images;
        imagesBuild = lib.all lib.isDerivation (lib.attrValues c.roles.worker.images);
        metal = summary c.roles.worker.nixos.metal.config;
        kvm = summary c.roles.worker.nixos.kvm.config;
      };
    expected = {
      images = [
        "kvm"
        "metal"
      ];
      imagesBuild = true;
      metal = {
        platform = "metal";
        consoles = [
          "console=tty0"
          "console=ttyS0,115200"
        ];
        guestAgent = false;
        condition = null;
      };
      kvm = {
        platform = "kvm";
        consoles = [ "console=ttyS0,115200" ];
        guestAgent = true;
        condition = "kvm";
      };
    };
  };
  # The guest agent answers what the host asks about the node and shuts it down; it runs no
  # commands and touches no files. It shuts the node down with systemd's commands, which it runs
  # from /sbin.
  testGuestAgentRestricted =
    let
      config = (cluster [ ]).roles.worker.nixos.kvm.config;
      service = config.systemd.services.qemu-guest-agent.serviceConfig;
    in
    {
      expr = {
        rpcs = lib.splitString "," (lib.head (builtins.match ".*--allow-rpcs=([^ ]*).*" service.ExecStart));
        sbin = service.BindReadOnlyPaths or [ ];
      };
      expected = {
        rpcs = [
          "guest-sync-delimited"
          "guest-sync"
          "guest-ping"
          "guest-info"
          "guest-get-osinfo"
          "guest-get-host-name"
          "guest-get-time"
          "guest-get-timezone"
          "guest-network-get-interfaces"
          "guest-shutdown"
        ];
        sbin = map (b: "${config.systemd.package}/bin/${b}:/sbin/${b}") [
          "poweroff"
          "halt"
          "reboot"
          "shutdown"
        ];
      };
    };
  # A kvm image finds its disks at boot: the initrd loads virtio's PCI transport and its block and
  # SCSI drivers, for the image the lab template's nodes run. A metal image
  # leaves them out, as real machines have no virtio disks.
  testKvmInitrdVirtio = {
    expr =
      let
        c = cluster [ ];
        virtio =
          platform: role:
          lib.sort lib.lessThan (
            lib.unique (
              lib.filter (lib.hasPrefix "virtio")
                c.roles.${role}.nixos.${platform}.config.boot.initrd.availableKernelModules
            )
          );
      in
      {
        kvm = virtio "kvm" "worker";
        metal = virtio "metal" "worker";
      };
    expected =
      let
        drivers = [
          "virtio_blk"
          "virtio_pci"
          "virtio_scsi"
        ];
      in
      {
        kvm = drivers;
        metal = [ ];
      };
  };
  # A definition adds a platform of its own, or modules to one of chalkos's. A role overrides a
  # value its platform sets by priority, as module order does not: lib.mkForce for a value the
  # platform defines plainly, lib.mkOverride below 50 for the guest agent's ExecStart, which kvm
  # forces. Both add to a list.
  testCustomPlatform = {
    expr =
      let
        c = cluster [
          {
            chalkos.platforms.esxi.nixosModules = [
              {
                services.openssh.enable = true;
                environment.variables.PLATFORM_TEST = "esxi";
              }
            ];
            chalkos.platforms.metal.nixosModules = [ { chalkos.kernel.moduleGroups = [ "gpu" ]; } ];
            chalkos.roles.worker.nixosModules = [
              {
                boot.kernelParams = [ "role" ];
                environment.variables.PLATFORM_TEST = lib.mkForce "role";
                systemd.services.qemu-guest-agent.serviceConfig.ExecStart = lib.mkOverride 49 "role-agent";
              }
            ];
          }
        ];
        nixos = c.roles.worker.nixos;
      in
      {
        images = lib.attrNames c.roles.worker.images;
        esxi = nixos.esxi.config.services.openssh.enable;
        esxiPlatform = nixos.esxi.config.system.nixos.extraOSReleaseArgs.CHALKOS_PLATFORM;
        kvm = nixos.kvm.config.services.openssh.enable;
        metalGPU = lib.elem "gpu" nixos.metal.config.chalkos.kernel.moduleGroups;
        kvmGPU = lib.elem "gpu" nixos.kvm.config.chalkos.kernel.moduleGroups;
        kernelParams = lib.sort lib.lessThan (
          lib.filter (p: p == "role" || p == "console=ttyS0,115200") nixos.kvm.config.boot.kernelParams
        );
        overridden = nixos.esxi.config.environment.variables.PLATFORM_TEST;
        agent = nixos.kvm.config.systemd.services.qemu-guest-agent.serviceConfig.ExecStart;
      };
    expected = {
      images = [
        "esxi"
        "kvm"
        "metal"
      ];
      esxi = true;
      esxiPlatform = "esxi";
      kvm = false;
      metalGPU = true;
      kvmGPU = false;
      kernelParams = [
        "console=ttyS0,115200"
        "role"
      ];
      overridden = "role";
      agent = "role-agent";
    };
  };
  # A node runs on metal unless it names a defined platform, which its identity carries.
  testNodePlatform = {
    expr =
      let
        node = platform: {
          chalkos.nodes.n1 = {
            role = "worker";
            inherit platform;
            storage.system.disk = "/dev/vda";
          };
        };
        manifest = modules: (cluster modules).manifest.nodes.n1;
      in
      {
        default = (storageCluster [ ]).manifest.nodes.n1.platform;
        kvm = {
          inherit (manifest [ (node "kvm") ]) platform;
          identity = (manifest [ (node "kvm") ]).identity.platform;
        };
        unknown = fails (manifest [ (node "esxi") ]);
        defined =
          (manifest [
            (node "esxi")
            { chalkos.platforms.esxi = { }; }
          ]).platform;
      };
    expected = {
      default = "metal";
      kvm = {
        platform = "kvm";
        identity = "kvm";
      };
      unknown = true;
      defined = "esxi";
    };
  };
  # A platform's name becomes CHALKOS_PLATFORM in its images's os-release.
  testPlatformNames = {
    expr = {
      bad = fails (cluster [ { chalkos.platforms.Bad = { }; } ]).manifest;
      dotted = fails (cluster [ { chalkos.platforms."x.y" = { }; } ]).manifest;
      good = (cluster [ { chalkos.platforms.esxi-8 = { }; } ]).manifest.roles.worker.images.esxi-8;
    };
    expected = {
      bad = true;
      dotted = true;
      good = "roles.worker.images.esxi-8";
    };
  };
  # The installer runs chalkd in installer mode and belongs to no platform, whatever its modules
  # say.
  testInstallerEssentials =
    let
      installer =
        modules: (cluster [ { chalkos.installer.nixosModules = modules; } ]).installer.image.drvPath;
    in
    {
      expr = {
        chalkd = fails (installer [ { systemd.services.chalkd.enable = false; } ]);
        mode = fails (installer [
          { systemd.services.chalkd.environment.CHALKD_INSTALLER = lib.mkForce "0"; }
        ]);
        platform = fails (installer [ { chalkos.platform.name = "kvm"; } ]);
      };
      expected = {
        chalkd = true;
        mode = true;
        platform = true;
      };
    };
  testManifestVersion = {
    expr = twoNodes.manifest.schemaVersion;
    expected = 0;
  };
  testManifestRoleImagePaths = {
    expr = twoNodes.manifest.roles.worker.images;
    expected = {
      kvm = "roles.worker.images.kvm";
      metal = "roles.worker.images.metal";
    };
  };
  testManifestIdentity = {
    expr = removeAttrs twoNodes.manifest.nodes.n1.identity [ "storage" ];
    expected = {
      hostname = "n1";
      cluster = "t";
      platform = "metal";
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
        nodeIPs = [ ];
        validSubnets = null;
      };
      time.servers = map (host: {
        inherit host;
        nts = true;
        port = null;
      }) defaultTimeServers;
    };
  };
  # An extension's per-node option reaches the node as a document of its kind.
  testManifestDocuments = {
    expr = twoNodes.manifest.nodes.n1.documents;
    expected = [
      {
        apiVersion = "rack.example.org/v1alpha1";
        kind = "Rack";
        metadata.name = "rack";
        spec.location = "a1";
      }
    ];
  };
  testManifestWithoutDocuments = {
    expr = (storageCluster [ ]).manifest.nodes.n1.documents;
    expected = [ ];
  };
  # The cluster-wide settings chalkctl turns into each node's Kubernetes, Flannel, RegistryMirror,
  # Addon and TimeSync documents.
  testManifestCarriesTheClusterSettings = {
    expr =
      let
        m =
          (cluster [
            {
              chalkos.cluster.kubernetes.vip.addresses = [ "10.0.0.1" ];
              chalkos.cluster.registries.mirrors."docker.io" = [ "https://mirror.example" ];
              chalkos.cluster.manifests = [
                {
                  apiVersion = "v1";
                  kind = "Namespace";
                  metadata.name = "apps";
                }
              ];
              chalkos.cni.flannel.mtu = 1430;
              chalkos.time.dhcpServers = true;
            }
          ]).manifest;
      in
      {
        inherit (m.kubernetes) domain ipFamilies;
        vip = m.kubernetes.vip.addresses;
        dnsIP = m.kubernetes.dnsIPs.ipv4;
        kubeletFlags = m.kubernetes.extraArgs.kubelet;
        inherit (m) flannel registries time;
        manifests = map (o: o.metadata.name) m.manifests;
        noFlannel = (cluster [ { chalkos.cni.provider = "none"; } ]).manifest.flannel;
      };
    expected = {
      domain = "cluster.local";
      ipFamilies = [ "ipv4" ];
      vip = [ "10.0.0.1" ];
      dnsIP = "10.96.0.10";
      kubeletFlags = { };
      flannel.mtu = 1430;
      registries = {
        mirrors."docker.io" = [ "https://mirror.example" ];
        allowPlainHTTP = false;
      };
      time.dhcpServers = true;
      manifests = [ "apps" ];
      noFlannel = null;
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
  testNodeKindDefaultsToWorker = {
    expr = {
      manifest = twoNodes.manifest.nodes.n1.kind;
      roleKind = twoNodes.manifest.roles.worker ? kind;
      image = (role (cluster [ ])).chalkos.role.kubernetes.enable;
    };
    expected = {
      manifest = "worker";
      roleKind = false;
      image = true;
    };
  };
  testRoleWithoutKubernetes = {
    expr =
      let
        c =
          kind:
          cluster [
            {
              chalkos.roles.worker.kubernetes.enable = false;
              chalkos.nodes.n1 = {
                role = "worker";
                storage.system.disk = "/dev/vda";
                kubernetes.kind = kind;
              };
            }
          ];
      in
      {
        manifest = (c "worker").manifest.nodes.n1.kind;
        identity = (c "worker").manifest.nodes.n1.identity.kubernetes;
        image = (role (c "worker")).chalkos.role.kubernetes.enable;
        controlPlane = fails (c "controlplane").manifest;
      };
    expected = {
      manifest = null;
      identity = null;
      image = false;
      controlPlane = true;
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
      nodeIPs = [ "10.0.0.5" ];
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
        clusterFile = (kubernetesSettings [ clusterSubnets ]).nodeIP;
        notASubnet = fails (node [
          static
          { chalkos.nodes.n1.kubernetes.validSubnets = [ "192.168.100.0" ]; }
        ]);
        notAClusterSubnet = fails (kubernetesSettings [
          { chalkos.cluster.kubernetes.nodeIP.validSubnets = [ "eth0" ]; }
        ]);
      };
    expected = {
      nodeSubnets = {
        nodeName = "n1";
        nodeIPs = [ ];
        validSubnets = [ "192.168.100.0/24" ];
      };
      clusterSubnets = {
        nodeName = "n1";
        nodeIPs = [ ];
        validSubnets = null;
      };
      nodeOverridesCluster = {
        nodeName = "n1";
        nodeIPs = [ "10.0.0.5" ];
        validSubnets = [ ];
      };
      fixedWins = {
        nodeName = "n1";
        nodeIPs = [ "10.0.0.5" ];
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
  # Every node has one address per family: fixed ones default to the first static address of each
  # family, and a family without one must be in the subnets the node picks from.
  testIPFamilies = {
    expr =
      let
        node =
          modules:
          (cluster (
            [
              {
                chalkos.nodes.n1 = {
                  role = "worker";
                  storage.system.disk = "/dev/vda";
                  network.networks."10-a".address = [
                    "fd00::5/64"
                    "10.0.0.5/24"
                  ];
                };
              }
            ]
            ++ modules
          )).manifest.nodes.n1.identity.kubernetes.nodeIPs;
        families = f: { chalkos.cluster.kubernetes.ipFamilies = f; };
        dual = families [
          "ipv4"
          "ipv6"
        ];
        set = k: { chalkos.nodes.n1.kubernetes = k; };
        clusterFile = modules: (kubernetesSettings modules).ipFamilies;
      in
      {
        ipv4 = node [ ];
        dual = node [ dual ];
        ipv6Primary = node [
          (families [
            "ipv6"
            "ipv4"
          ])
        ];
        shorthand = node [ (set { nodeIP = "10.0.0.7"; }) ];
        # The other family is picked from the default filter.
        oneFixed = node [
          dual
          (set {
            nodeIPs = [ "10.0.0.7" ];
          })
        ];
        clusterFile = clusterFile [ dual ];
        twoOfOneFamily = fails (node [
          (set {
            nodeIPs = [
              "10.0.0.5"
              "10.0.0.6"
            ];
          })
        ]);
        notAnAddress = fails (node [ (set { nodeIPs = [ "10.0.0.5/24" ]; }) ]);
        both = fails (node [
          (set {
            nodeIP = "10.0.0.7";
            nodeIPs = [ "10.0.0.5" ];
          })
        ]);
        otherFamily = fails (node [ (set { nodeIPs = [ "fd00::5" ]; }) ]);
        unpickable = fails (node [
          dual
          (set {
            nodeIPs = [ "10.0.0.5" ];
            validSubnets = [ "10.0.0.0/8" ];
          })
        ]);
        noFamily = fails (clusterFile [ (families [ ]) ]);
        familyTwice = fails (clusterFile [
          (families [
            "ipv4"
            "ipv4"
          ])
        ]);
        unknownFamily = fails (clusterFile [ (families [ "ipx" ]) ]);
      };
    expected = {
      ipv4 = [ "10.0.0.5" ];
      dual = [
        "10.0.0.5"
        "fd00::5"
      ];
      ipv6Primary = [
        "fd00::5"
        "10.0.0.5"
      ];
      shorthand = [ "10.0.0.7" ];
      oneFixed = [ "10.0.0.7" ];
      clusterFile = [
        "ipv4"
        "ipv6"
      ];
      twoOfOneFamily = true;
      notAnAddress = true;
      both = true;
      otherFamily = true;
      unpickable = true;
      noFamily = true;
      familyTwice = true;
      unknownFamily = true;
    };
  };
  # The ranges of the families in use, and the DNS address by default the 10th of the service range.
  testAddressRanges = {
    expr =
      let
        setup =
          families:
          let
            k = kubernetesSettings [ { chalkos.cluster.kubernetes.ipFamilies = families; } ];
            ofFamilies = byFamily: lib.genAttrs k.ipFamilies (family: byFamily.${family});
          in
          {
            inherit (k) ipFamilies;
            podCIDRs = ofFamilies k.podCIDRs;
            serviceCIDRs = ofFamilies k.serviceCIDRs;
            dnsIPs = ofFamilies k.dnsIPs;
            nodeCIDRMaskSizes = ofFamilies k.nodeCIDRMaskSizes;
          };
        # The DNS address defaults to the 10th address of the service range, written as Go writes
        # addresses.
        dnsIPs =
          serviceCIDRs:
          (cluster [
            {
              chalkos.cluster.kubernetes = {
                ipFamilies = [
                  "ipv4"
                  "ipv6"
                ];
                inherit serviceCIDRs;
              };
            }
          ]).cluster.kubernetes.dnsIPs;
      in
      {
        ipv4 = setup [ "ipv4" ];
        dual = setup [
          "ipv4"
          "ipv6"
        ];
        ipv6Primary = setup [
          "ipv6"
          "ipv4"
        ];
        ipv6 = setup [ "ipv6" ];
        defaultDNSIPs = dnsIPs {
          ipv4 = "10.100.0.0/16";
          ipv6 = "fd00:0:0:5::/112";
        };
        firstLongestZeros = (dnsIPs { ipv6 = "fd00:0:0:5:0:0:1:0/112"; }).ipv6;
        # A service range of fewer than ten addresses needs a DNS address set.
        tooSmallForDefault = fails (dnsIPs { ipv4 = "10.100.0.248/29"; }).ipv4;
      };
    expected = {
      ipv4 = {
        ipFamilies = [ "ipv4" ];
        podCIDRs.ipv4 = "10.244.0.0/16";
        serviceCIDRs.ipv4 = "10.96.0.0/12";
        dnsIPs.ipv4 = "10.96.0.10";
        nodeCIDRMaskSizes.ipv4 = 24;
      };
      dual = {
        ipFamilies = [
          "ipv4"
          "ipv6"
        ];
        podCIDRs = {
          ipv4 = "10.244.0.0/16";
          ipv6 = "fd00:10:244::/56";
        };
        serviceCIDRs = {
          ipv4 = "10.96.0.0/12";
          ipv6 = "fd00:10:96::/112";
        };
        dnsIPs = {
          ipv4 = "10.96.0.10";
          ipv6 = "fd00:10:96::a";
        };
        nodeCIDRMaskSizes = {
          ipv4 = 24;
          ipv6 = 64;
        };
      };
      ipv6Primary = {
        ipFamilies = [
          "ipv6"
          "ipv4"
        ];
        podCIDRs = {
          ipv4 = "10.244.0.0/16";
          ipv6 = "fd00:10:244::/56";
        };
        serviceCIDRs = {
          ipv4 = "10.96.0.0/12";
          ipv6 = "fd00:10:96::/112";
        };
        dnsIPs = {
          ipv4 = "10.96.0.10";
          ipv6 = "fd00:10:96::a";
        };
        nodeCIDRMaskSizes = {
          ipv4 = 24;
          ipv6 = 64;
        };
      };
      ipv6 = {
        ipFamilies = [ "ipv6" ];
        podCIDRs.ipv6 = "fd00:10:244::/56";
        serviceCIDRs.ipv6 = "fd00:10:96::/112";
        dnsIPs.ipv6 = "fd00:10:96::a";
        nodeCIDRMaskSizes.ipv6 = 64;
      };
      defaultDNSIPs = {
        ipv4 = "10.100.0.10";
        ipv6 = "fd00:0:0:5::a";
      };
      firstLongestZeros = "fd00::5:0:0:1:a";
      tooSmallForDefault = true;
    };
  };
  # The ranges are checked as kubeadm checks them, for the families in use only, and the removed
  # options name their replacements.
  testAddressRangeChecks = {
    expr =
      let
        dual = kubernetes: {
          chalkos.cluster.kubernetes = {
            ipFamilies = [
              "ipv4"
              "ipv6"
            ];
          }
          // kubernetes;
        };
        refused =
          kubernetes:
          let
            c = cluster [ (dual kubernetes) ];
          in
          fails (removeAttrs c.cluster.kubernetes [ "package" ]) && fails c.manifest.kubernetes;
      in
      {
        valid =
          !refused {
            podCIDRs = {
              ipv4 = "10.32.0.0/12";
              ipv6 = "fd00:32::/48";
            };
            serviceCIDRs = {
              ipv4 = "10.16.0.0/16";
              ipv6 = "fd00:16::/108";
            };
            nodeCIDRMaskSizes = {
              ipv4 = 28;
              ipv6 = 64;
            };
          };
        # Entries of a family not in use are ignored.
        otherFamily =
          !fails (kubernetesSettings [
            {
              chalkos.cluster.kubernetes = {
                podCIDRs.ipv6 = "10.244.0.0";
                dnsIPs.ipv6 = "dns";
                nodeCIDRMaskSizes.ipv6 = 0;
              };
            }
          ]);
        notARange = refused { podCIDRs.ipv4 = "10.244.0.0"; };
        exclusion = refused { podCIDRs.ipv4 = "!10.244.0.0/16"; };
        wrongFamily = refused { podCIDRs.ipv6 = "10.244.0.0/16"; };
        ipv4Mapped = refused { podCIDRs.ipv6 = "::ffff:10.244.0.0/112"; };
        hostBits = refused { serviceCIDRs.ipv4 = "10.96.0.1/12"; };
        overlap = refused { serviceCIDRs.ipv4 = "10.244.128.0/20"; };
        overlapIPv6 = refused { podCIDRs.ipv6 = "fd00:10::/32"; };
        tooManyServices = refused { serviceCIDRs.ipv4 = "10.96.0.0/11"; };
        tooManyServicesIPv6 = refused {
          serviceCIDRs.ipv6 = "fd00:10:96::/107";
          dnsIPs.ipv6 = "fd00:10:96::a";
        };
        maskNotLonger = refused { nodeCIDRMaskSizes.ipv4 = 16; };
        maskTooLong = refused { nodeCIDRMaskSizes.ipv6 = 73; };
        maskBeyondAddress = refused {
          podCIDRs.ipv4 = "10.244.0.0/24";
          nodeCIDRMaskSizes.ipv4 = 33;
        };
        maskBeyondAddressIPv6 = refused {
          podCIDRs.ipv6 = "fd00:10:244::/120";
          nodeCIDRMaskSizes.ipv6 = 129;
        };
        dnsOutside = refused { dnsIPs.ipv4 = "10.112.0.10"; };
        dnsNotAnAddress = refused { dnsIPs.ipv6 = "dns"; };
        dnsWrongFamily = refused { dnsIPs.ipv6 = "10.96.0.10"; };
        dnsKubernetesService = refused { dnsIPs.ipv4 = "10.96.0.1"; };
        dnsNetwork = refused { dnsIPs.ipv6 = "fd00:10:96::"; };
        podCIDR = refused { podCIDR = "10.244.0.0/16"; };
        serviceCIDR = refused { serviceCIDR = "10.96.0.0/12"; };
        dnsIP = refused { dnsIP = "10.96.0.10"; };
      };
    expected = {
      valid = true;
      otherFamily = true;
      notARange = true;
      exclusion = true;
      wrongFamily = true;
      ipv4Mapped = true;
      hostBits = true;
      overlap = true;
      overlapIPv6 = true;
      tooManyServices = true;
      tooManyServicesIPv6 = true;
      maskNotLonger = true;
      maskTooLong = true;
      maskBeyondAddress = true;
      maskBeyondAddressIPv6 = true;
      dnsOutside = true;
      dnsNotAnAddress = true;
      dnsWrongFamily = true;
      dnsKubernetesService = true;
      dnsNetwork = true;
      podCIDR = true;
      serviceCIDR = true;
      dnsIP = true;
    };
  };
  # flannel's MTU reaches the nodes in their Flannel document and must leave pods their family's
  # minimum; containerd runs portmap, which needs nft.
  testFlannelSettings = {
    expr = {
      flannel = map (modules: (cluster modules).manifest.flannel) [
        [ ]
        [ { chalkos.cni.flannel.mtu = 1430; } ]
        [ { chalkos.cni.provider = "none"; } ]
      ];
      notAnMTU = fails (cluster [ { chalkos.cni.flannel.mtu = 0; } ]).cni.flannel.mtu;
      # Pods get the MTU minus 50, which must leave IPv6 its 1280 and IPv4 its 576.
      tooSmall =
        map
          (
            { families, mtu }:
            fails
              (cluster [
                {
                  chalkos.cluster.kubernetes.ipFamilies = families;
                  chalkos.cni.flannel.mtu = mtu;
                }
              ]).cni.flannel.mtu
          )
          [
            {
              families = [ "ipv4" ];
              mtu = 625;
            }
            {
              families = [ "ipv4" ];
              mtu = 626;
            }
            {
              families = [
                "ipv4"
                "ipv6"
              ];
              mtu = 1329;
            }
            {
              families = [
                "ipv4"
                "ipv6"
              ];
              mtu = 1330;
            }
            {
              families = [ "ipv6" ];
              mtu = 1329;
            }
            {
              families = [ "ipv6" ];
              mtu = 1330;
            }
          ];
      # containerd runs portmap, which needs nft.
      portmapFindsNft =
        lib.any (p: (p.pname or "") == "nftables")
          (role (cluster [ ])).systemd.services.containerd.path;
    };
    expected = {
      flannel = [
        { mtu = null; }
        { mtu = 1430; }
        null
      ];
      notAnMTU = true;
      tooSmall = [
        true
        false
        true
        false
        true
        false
      ];
      portmapFindsNft = true;
    };
  };
  # VXLAN source ranges are networks of the cluster's families, one at least of each, and hold
  # every address a node may have; they reach the nodes in their Kubernetes document.
  testVXLANSourceSubnets = {
    expr =
      let
        dual = kubernetes: {
          chalkos.cluster.kubernetes = {
            ipFamilies = [
              "ipv4"
              "ipv6"
            ];
          }
          // kubernetes;
        };
        sources = subnets: dual { vxlanSourceSubnets = subnets; };
        both = sources [
          "192.168.0.0/16"
          "fd00:100::/64"
        ];
        clusterFile = modules: (kubernetesSettings modules).vxlanSourceSubnets;
        # Node n1 with a static address of each family, and the settings of its kubernetes.
        node =
          modules: kubernetes:
          (cluster (
            modules
            ++ [
              {
                chalkos.nodes.n1 = {
                  role = "worker";
                  storage.system.disk = "/dev/vda";
                  network.networks."10-a".address = [
                    "192.168.100.5/24"
                    "fd00:100::5/64"
                  ];
                  inherit kubernetes;
                };
              }
            ]
          )).manifest.nodes.n1.identity.kubernetes.nodeIPs;
      in
      {
        none = clusterFile [ ];
        set = clusterFile [ both ];
        notARange = fails (clusterFile [
          (sources [
            "192.168.0.0"
            "fd00:100::/64"
          ])
        ]);
        hostBits = fails (clusterFile [
          (sources [
            "192.168.0.1/16"
            "fd00:100::/64"
          ])
        ]);
        ipv4Mapped = fails (clusterFile [
          (sources [
            "192.168.0.0/16"
            "::ffff:10.0.0.0/104"
          ])
        ]);
        exclusion = fails (clusterFile [
          (sources [
            "192.168.0.0/16"
            "!fd00:100::/64"
          ])
        ]);
        tooLong = fails (clusterFile [
          (sources [
            "192.168.0.0/33"
            "fd00:100::/64"
          ])
        ]);
        missingFamily = fails (clusterFile [ (sources [ "192.168.0.0/16" ]) ]);
        # Pods could pass the source check from ranges overlapping the pod or service ranges.
        holdsPods = fails (clusterFile [
          (sources [
            "10.0.0.0/8"
            "fd00:100::/64"
          ])
        ]);
        inPods = fails (clusterFile [
          (sources [
            "192.168.0.0/16"
            "fd00:10:244:1::/64"
          ])
        ]);
        inServices = fails (clusterFile [
          (sources [
            "10.100.0.0/16"
            "fd00:100::/64"
          ])
        ]);
        inServicesIPv6 = fails (clusterFile [
          (sources [
            "192.168.0.0/16"
            "fd00:10:96::/120"
          ])
        ]);
        otherFamily = fails (clusterFile [
          {
            chalkos.cluster.kubernetes.vxlanSourceSubnets = [
              "192.168.0.0/16"
              "fd00::/48"
            ];
          }
        ]);
        # The node's static addresses, its fixed nodeIPs by default, are in the ranges.
        staticInside = node [ both ] { };
        fixedOutside = fails (node [ both ] { nodeIPs = [ "10.0.0.5" ]; });
        subnetInside = node [ both ] {
          validSubnets = [
            "192.168.100.0/24"
            "fd00:100::/64"
          ];
        };
        subnetOutside = fails (
          node [ both ] {
            validSubnets = [
              "192.168.0.0/15"
              "fd00:100::/64"
            ];
          }
        );
        # Exclusions need not be inside.
        exclusionOutside = node [ both ] {
          validSubnets = [
            "192.168.100.0/24"
            "fd00:100::/64"
            "!10.0.0.0/8"
          ];
        };
        clusterSubnetOutside = fails (
          node [
            both
            { chalkos.cluster.kubernetes.nodeIP.validSubnets = [ "10.0.0.0/8" ]; }
          ] { }
        );
        withoutSources = node [ ] { nodeIPs = [ "10.0.0.5" ]; };
      };
    expected = {
      none = [ ];
      set = [
        "192.168.0.0/16"
        "fd00:100::/64"
      ];
      notARange = true;
      hostBits = true;
      ipv4Mapped = true;
      exclusion = true;
      tooLong = true;
      missingFamily = true;
      holdsPods = true;
      inPods = true;
      inServices = true;
      inServicesIPv6 = true;
      otherFamily = true;
      staticInside = [
        "192.168.100.5"
        "fd00:100::5"
      ];
      fixedOutside = true;
      subnetInside = [ ];
      subnetOutside = true;
      exclusionOutside = [ ];
      clusterSubnetOutside = true;
      withoutSources = [ "10.0.0.5" ];
    };
  };
  # A node joining changes nothing on the others: their cluster file and image stay the same as
  # long as no cluster-wide option changes.
  testNodeJoinChangesNoOtherNode = {
    expr =
      let
        base = [
          {
            chalkos.cluster.kubernetes.vxlanSourceSubnets = [ "192.168.0.0/16" ];
            chalkos.nodes.n1 = {
              role = "worker";
              storage.system.disk = "/dev/vda";
              network.networks."10-a".address = [ "192.168.100.5/24" ];
            };
          }
        ];
        joined = base ++ [
          {
            chalkos.nodes.n2 = {
              role = "worker";
              storage.system.disk = "/dev/vda";
              network.networks."10-a".address = [ "192.168.100.6/24" ];
            };
          }
        ];
        image = modules: (cluster modules).roles.worker.images.metal.drvPath;
        identity = modules: (cluster modules).manifest.nodes.n1;
      in
      {
        image = image base == image joined;
        identity = identity base == identity joined;
      };
    expected = {
      image = true;
      identity = true;
    };
  };
  # The VIPs reach the nodes in their Kubernetes document; the endpoint is one of them.
  testVIP = {
    expr =
      let
        vipCluster =
          endpoint: vip:
          cluster [
            {
              chalkos.cluster.endpoint = lib.mkForce endpoint;
              chalkos.cluster.kubernetes.vip = vip;
              chalkos.nodes.cp1 = {
                role = "worker";
                kubernetes.kind = "controlplane";
                storage.system.disk = "/dev/vda";
                kubernetes.nodeIP = "10.0.0.11";
              };
            }
          ];
        clusterFile = c: c.manifest.kubernetes.vip;
        warns = c: lib.any (lib.hasInfix "must reach a control-plane node") c.warnings;
      in
      {
        clusterFile = clusterFile (vipCluster "https://10.0.0.10:6443" { addresses = [ "10.0.0.10" ]; });
        withInterface = clusterFile (
          vipCluster "https://10.0.0.10:6443" {
            addresses = [ "10.0.0.10" ];
            interface = "bond0";
          }
        );
        # The VIP is the endpoint, which no node's address is.
        warns = warns (vipCluster "https://10.0.0.10:6443" { addresses = [ "10.0.0.10" ]; });
        hostname = clusterFile (vipCluster "https://k8s.example.com:6443" { addresses = [ "10.0.0.10" ]; });
        endpointNotAVIP = fails (
          clusterFile (vipCluster "https://10.0.0.20:6443" { addresses = [ "10.0.0.10" ]; })
        );
        twoOfOneFamily = fails (
          clusterFile (
            vipCluster "https://10.0.0.10:6443" {
              addresses = [
                "10.0.0.10"
                "10.0.0.11"
              ];
            }
          )
        );
        otherFamily = fails (
          clusterFile (vipCluster "https://[fd00::10]:6443" { addresses = [ "fd00::10" ]; })
        );
        notAnAddress = fails (
          clusterFile (vipCluster "https://10.0.0.10:6443" { addresses = [ "10.0.0.10/32" ]; })
        );
        unknownMode = fails (
          clusterFile (
            vipCluster "https://10.0.0.10:6443" {
              addresses = [ "10.0.0.10" ];
              mode = "bgp";
            }
          )
        );
      };
    expected = {
      clusterFile = {
        addresses = [ "10.0.0.10" ];
        mode = "l2";
        interface = null;
      };
      withInterface = {
        addresses = [ "10.0.0.10" ];
        mode = "l2";
        interface = "bond0";
      };
      warns = false;
      hostname = {
        addresses = [ "10.0.0.10" ];
        mode = "l2";
        interface = null;
      };
      endpointNotAVIP = true;
      twoOfOneFamily = true;
      otherFamily = true;
      notAnAddress = true;
      unknownMode = true;
    };
  };
  # A subnet every node would refuse at boot is refused before any image is built.
  testSubnetsAreChecked =
    let
      good = [
        "10.0.0.0/8"
        "!10.0.0.10/32"
        "0.0.0.0/0"
        "192.168.100.12/24"
        "fd00::/64"
        "!fd00::1/128"
        "FD00:0:0:0:0:0:0:1/128"
        "::/0"
        "::1/128"
        "fd00::/8"
        "64:ff9b::192.0.2.1/128"
      ];
      bad = [
        "999.1.1.1/40"
        "a/1"
        "::::/999"
        "10.0.0.0/33"
        "fd00::/129"
        "10.0.0/8"
        "010.0.0.0/8"
        "10.0.0.0/08"
        "fd00::1::/64"
        "fd00:1:2:3:4:5:6:7:8/64"
        "1:2:3:4:5:6:7::8/64"
        "fd00:12345::/64"
        "!!10.0.0.0/8"
        " 10.0.0.0/8"
        "fd00::/"
        # IPv4-mapped: the node compares its addresses in their IPv4 form.
        "::ffff:10.0.0.0/104"
        "!::ffff:10.0.0.10/128"
        "::ffff:a00:a/128"
      ];
      accepted =
        subnet:
        let
          c = cluster [
            {
              chalkos.cluster.kubernetes.nodeIP.validSubnets = [ subnet ];
              # The node picks the address of the subnet's family.
              chalkos.cluster.kubernetes.ipFamilies = [ (if lib.hasInfix ":" subnet then "ipv6" else "ipv4") ];
              chalkos.nodes.n1 = {
                role = "worker";
                storage.system.disk = "/dev/vda";
                kubernetes.validSubnets = [ subnet ];
              };
            }
          ];
        in
        {
          cluster = !fails c.manifest.kubernetes;
          node = !fails c.manifest.nodes.n1.identity.kubernetes;
        };
      all = value: _: {
        cluster = value;
        node = value;
      };
    in
    {
      expr = {
        good = lib.genAttrs good accepted;
        bad = lib.genAttrs bad accepted;
      };
      expected = {
        good = lib.genAttrs good (all true);
        bad = lib.genAttrs bad (all false);
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
                chalkos.cluster.kubernetes.ipFamilies = [
                  "ipv4"
                  "ipv6"
                ];
                chalkos.roles.cp = { };
                chalkos.nodes.cp1 = {
                  role = "cp";
                  kubernetes.kind = "controlplane";
                  storage.system.disk = "/dev/vda";
                  kubernetes.nodeIPs = [
                    "10.0.0.11"
                    "fd00::12"
                  ];
                };
                chalkos.nodes.ip6 = {
                  role = "cp";
                  kubernetes.kind = "controlplane";
                  storage.system.disk = "/dev/vda";
                  kubernetes.nodeIPs = [
                    "fd00::11"
                    "10.0.0.12"
                  ];
                };
                chalkos.nodes.w1 = {
                  role = "worker";
                  storage.system.disk = "/dev/vda";
                  kubernetes.nodeIPs = [
                    "10.0.0.20"
                    "fd00::20"
                  ];
                };
              }
            ]).warnings;
        # A control-plane node that picks its address at boot may hold the endpoint's.
        picked =
          lib.any (lib.hasInfix "must reach a control-plane node")
            (cluster [
              {
                chalkos.cluster.endpoint = lib.mkForce "https://10.0.0.10:6443";
                chalkos.roles.cp = { };
                chalkos.nodes.cp1 = {
                  role = "cp";
                  kubernetes.kind = "controlplane";
                  storage.system.disk = "/dev/vda";
                  kubernetes.nodeIP = "10.0.0.11";
                };
                chalkos.nodes.cp2 = {
                  role = "cp";
                  kubernetes.kind = "controlplane";
                  storage.system.disk = "/dev/vda";
                  kubernetes.validSubnets = [ "10.0.0.0/24" ];
                };
              }
            ]).warnings;
        # Control-plane nodes that pick their address at boot hold the endpoint's only when it lies
        # in the subnets that apply to them. The nodes have the endpoint's family alone.
        picks =
          endpoint: clusterSubnets: nodeSubnets:
          let
            ipv6 = lib.hasInfix "[" endpoint;
          in
          lib.any (lib.hasInfix "must reach a control-plane node")
            (cluster [
              {
                chalkos.cluster.endpoint = lib.mkForce endpoint;
                chalkos.cluster.kubernetes.nodeIP.validSubnets = clusterSubnets;
                chalkos.cluster.kubernetes.ipFamilies = [ (if ipv6 then "ipv6" else "ipv4") ];
                chalkos.roles.cp = { };
                chalkos.nodes.cp1 = {
                  role = "cp";
                  kubernetes.kind = "controlplane";
                  storage.system.disk = "/dev/vda";
                  kubernetes.nodeIPs = [ (if ipv6 then "fd00:1::11" else "10.0.0.11") ];
                };
                chalkos.nodes.cp2 = {
                  role = "cp";
                  kubernetes.kind = "controlplane";
                  storage.system.disk = "/dev/vda";
                  kubernetes.validSubnets = nodeSubnets;
                };
              }
            ]).warnings;
      in
      {
        inherit picked;
        picksOutside = picks "https://10.0.0.10:6443" [ ] [ "192.168.0.0/24" ];
        picksExcluded =
          picks "https://10.0.0.10:6443"
            [ ]
            [
              "10.0.0.0/24"
              "!10.0.0.8/29"
            ];
        picksOnlyExclusions = picks "https://10.0.0.10:6443" [ ] [ "!192.168.0.0/16" ];
        picksAny = picks "https://10.0.0.10:6443" [ ] [ ];
        picksClusterSubnets = picks "https://10.0.0.10:6443" [ "10.0.0.0/8" ] null;
        picksOutsideClusterSubnets = picks "https://10.0.0.10:6443" [ "192.168.0.0/16" ] null;
        picksIPv6 = picks "https://[fd00::10]:6443" [ ] [ "fd00::/64" ];
        # A node of the endpoint's family alone whose subnets hold none of that family is refused.
        picksOtherFamily = fails (picks "https://[fd00::10]:6443" [ ] [ "10.0.0.0/8" ]);
        picksIPv6Outside = picks "https://[fd00:0:0:1::10]:6443" [ ] [ "fd00::/64" ];
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
      picksOutside = true;
      picksExcluded = true;
      picksOnlyExclusions = false;
      picksAny = false;
      picksClusterSubnets = false;
      picksOutsideClusterSubnets = true;
      picksIPv6 = false;
      picksOtherFamily = true;
      picksIPv6Outside = true;
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
        # A node of the kind, or with null on a role without Kubernetes.
        kindOf =
          kind:
          if kind == null then
            { chalkos.roles.worker.kubernetes.enable = false; }
          else
            { chalkos.nodes.n1.kubernetes.kind = kind; };
        swapManifest =
          kind:
          (cluster [
            (kindOf kind)
            {
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
        # A node of the kind, or with null on a role without Kubernetes.
        kindOf =
          kind:
          if kind == null then
            { chalkos.roles.worker.kubernetes.enable = false; }
          else
            { chalkos.nodes.n1.kubernetes.kind = kind; };
        rejects =
          kind: label:
          fails
            (cluster [
              (kindOf kind)
              {
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
          chalkos.nodes.n1 = {
            role = "worker";
            kubernetes.kind = "controlplane";
            storage.system.disk = "/dev/vda";
          };
        }
      ]).manifest.nodes.n1.identity.kubernetes;
    expected = {
      nodeName = "n1";
      nodeIPs = [ ];
      validSubnets = null;
    };
  };
  testKubernetesOptionDefaults = {
    expr = removeAttrs (cluster [ ]).cluster.kubernetes [
      "package"
      "extraArgs"
    ];
    expected = {
      podCIDRs = {
        ipv4 = "10.244.0.0/16";
        ipv6 = "fd00:10:244::/56";
      };
      serviceCIDRs = {
        ipv4 = "10.96.0.0/12";
        ipv6 = "fd00:10:96::/112";
      };
      dnsIPs = {
        ipv4 = "10.96.0.10";
        ipv6 = "fd00:10:96::a";
      };
      nodeCIDRMaskSizes = {
        ipv4 = 24;
        ipv6 = 64;
      };
      # Removed: they only name their replacements.
      podCIDR = null;
      serviceCIDR = null;
      dnsIP = null;
      domain = "cluster.local";
      allowSchedulingOnControlPlanes = false;
      ipFamilies = [ "ipv4" ];
      vxlanSourceSubnets = [ ];
      nodeIP = {
        validSubnets = [ ];
        timeout = 300;
      };
      vip = {
        addresses = [ ];
        mode = "l2";
        interface = null;
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
  testKubernetesOnlyInKubernetesRoles = {
    expr =
      let
        image = enable: role (cluster [ { chalkos.roles.worker.kubernetes.enable = enable; } ]);
        services = config: {
          kubelet = config.systemd.services ? kubelet;
          containerd = config.virtualisation.containerd.enable;
          prepare = config.systemd.services ? chalkos-kubernetes;
        };
      in
      {
        none = services (image false);
        worker = services (image true);
        prepare = lib.hasInfix "/bin/chalkd prepare-kubernetes" (image true)
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
  testKubernetesImage = {
    expr =
      let
        c = cluster [
          {
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
      in
      {
        clusterFile = config.environment.etc ? "chalkos/kubernetes/cluster.json";
        # The image opens neither the API server's nor etcd's ports, which etcd authenticates by its
        # CA, nor makes etcd's directory: chalkos-controlplane does both by Node.spec.type.
        firewall = lib.any (port: lib.elem port config.networking.firewall.allowedTCPPorts) [
          6443
          2379
          2380
        ];
        apiServerPort = lib.elem 6443 config.networking.firewall.allowedTCPPorts;
        markedPorts = lib.hasInfix "tcp dport { 6443, 2379, 2380 } meta mark & 0x02000000 == 0x02000000 accept" config.networking.firewall.extraInputRules;
        # chalkd renders the addons and reads the manifests from the config.
        manifests = config.environment.etc ? "chalkos/kubernetes/manifests.json";
        etcdDir = lib.elem "d /var/lib/etcd 0700 root root -" config.systemd.tmpfiles.rules;
        consumer =
          (builtins.fromJSON config.environment.etc."chalkos/consumers.json".text).chalkos-controlplane.kinds;
        beforeKubernetes = lib.all (u: lib.elem u config.systemd.services.chalkos-controlplane.before) [
          "chalkos-kubernetes.service"
          "kubelet.service"
        ];
        etcdMount = config.systemd.services.chalkos-controlplane.unitConfig.RequiresMountsFor;
        # The kubelet waits for the mount too: its etcd pod would otherwise start on an empty directory.
        kubeletEtcdMount = config.systemd.services.kubelet.unitConfig.RequiresMountsFor;
        # Without the firewall the unit makes etcd's directory alone, and does not wait for it.
        afterAbsentFirewall =
          let
            unit =
              (role (cluster [
                {
                  chalkos.roles.worker.nixosModules = [ { networking.firewall.enable = false; } ];
                }
              ])).systemd.services.chalkos-controlplane;
          in
          lib.elem "nftables.service" unit.after;
      };
    expected = {
      clusterFile = false;
      firewall = false;
      apiServerPort = false;
      markedPorts = true;
      manifests = false;
      etcdDir = false;
      consumer = [ "Node" ];
      beforeKubernetes = true;
      etcdMount = "/var/lib/etcd";
      kubeletEtcdMount = "/var/lib/etcd";
      afterAbsentFirewall = false;
    };
  };
  # One directory with the plugins the cluster names, copied out of nixpkgs's packages: those
  # flannel's network runs, plus flannel's own plugin, or with no network every reference plugin;
  # a cluster adds to them or narrows them.
  testCNIPluginsFollowProvider = {
    expr =
      let
        image = settings: role (cluster [ { chalkos.cni = settings; } ]);
        plugins =
          settings:
          let
            c = image settings;
            dir = c.system.build.chalkosCNIPlugins;
          in
          {
            plugins = map baseNameOf dir.plugins;
            binDirs = c.virtualisation.containerd.settings.plugins."io.containerd.cri.v1.runtime".cni.bin_dirs;
            inherit (dir) name;
          };
        flannel = plugins { provider = "flannel"; };
        none = plugins { provider = "none"; };
      in
      {
        flannel = flannel.plugins;
        none = none.plugins;
        binDirs = map (p: p.binDirs) [
          flannel
          none
        ];
        added = (plugins { plugins = [ "tuning" ]; }).plugins;
        narrowed =
          (plugins {
            provider = "none";
            plugins = lib.mkForce [ "ptp" ];
          }).plugins;
        name = lib.hasPrefix "cni-plugins-chalkos-${pkgs.cni-plugins.version}" flannel.name;
        unknown = fails (plugins { plugins = [ "no-such-plugin" ]; }).plugins;
      };
    expected = {
      flannel = [
        "bridge"
        "host-local"
        "loopback"
        "portmap"
        "flannel"
      ];
      none = lib.sort lib.lessThan (map baseNameOf pkgs.cni-plugins.subPackages);
      binDirs =
        let
          dir =
            provider:
            "${(role (cluster [ { chalkos.cni.provider = provider; } ])).system.build.chalkosCNIPlugins}/bin";
        in
        [
          [ (dir "flannel") ]
          [ (dir "none") ]
        ];
      added = [
        "bridge"
        "host-local"
        "loopback"
        "portmap"
        "tuning"
        "flannel"
      ];
      narrowed = [ "ptp" ];
      name = true;
      unknown = true;
    };
  };
  # A role carries the base groups of kernel modules plus what it adds; the initrd takes its
  # modules from the same tree, and a module the image loads must be in it. The build check
  # kernel-modules runs the filter and the check.
  testKernelModules = {
    expr =
      let
        image = modules: role (cluster [ { chalkos.roles.worker.nixosModules = modules; } ]);
        c = image [ ];
        gpu = image [
          {
            chalkos.kernel.moduleGroups = [ "gpu" ];
            chalkos.kernel.extraModules = [ "kvm_amd" ];
          }
        ];
        all = image [ { chalkos.kernel.allModules = true; } ];
        tree = c: c.system.build.chalkosKernelModules;
        kernelModules = c: "${lib.getOutput "modules" c.boot.kernelPackages.kernel}";
        check = lib.findFirst (d: d.name == "kernel-modules-check") null c.system.checks;
        loaded = lib.splitString "\n" check.loaded;
      in
      {
        groups = c.chalkos.kernel.moduleGroups;
        gpuDirectory = map (c: lib.elem "drivers/gpu" (tree c).directories) [
          c
          gpu
        ];
        extra = lib.elem "kvm_amd" (tree gpu).names;
        # The image's module tree is made of the filtered tree alone, not of the kernel's.
        filtered = (tree c) ? directories && map toString c.system.modulesTree.paths == [ "${tree c}" ];
        all = "${tree all}" == kernelModules all;
        loadsChecked = lib.all (m: lib.elem m loaded) (
          c.boot.kernelModules ++ c.boot.initrd.kernelModules ++ c.boot.initrd.availableKernelModules
        );
      };
    expected = {
      groups = [
        "storage"
        "network"
        "virtualisation"
        "filesystems"
        "kubernetes"
        "platform"
      ];
      gpuDirectory = [
        false
        true
      ];
      extra = true;
      filtered = true;
      all = true;
      loadsChecked = true;
    };
  };
  # The tools nodes run, without what they never do: xfs_scrub, ctr and containerd-stress.
  testTrimmedTools = {
    expr =
      let
        c = role (cluster [ ]);
        named = name: lib.findFirst (p: lib.getName p == name) null;
        xfsprogs = named "xfsprogs" c.systemd.services.chalkd.path;
        containerd = named "containerd" c.systemd.services.containerd.path;
      in
      {
        scrub = lib.elem "--enable-scrub=no" xfsprogs.configureFlags;
        python = lib.any (p: lib.getName p == "python3") xfsprogs.buildInputs;
        icu = lib.any (p: lib.getName p == "icu4c") (
          xfsprogs.buildInputs ++ xfsprogs.propagatedBuildInputs ++ xfsprogs.nativeBuildInputs
        );
        inherit (containerd) binaries;
        containerdOutputs = containerd.meta.outputsToInstall;
        # NixOS would generate xfs_scrub_all with a PATH alone.
        scrubUnit = lib.elem "xfs_scrub_all.service" c.systemd.suppressedSystemUnits;
      };
    expected = {
      scrub = true;
      python = false;
      icu = false;
      binaries = [
        "containerd"
        "containerd-shim-runc-v2"
      ];
      containerdOutputs = [ "out" ];
      scrubUnit = true;
    };
  };
  # Mirrors come from the RegistryMirror documents, written to /run before containerd starts.
  testRegistryMirrorsFromConfig =
    let
      config = role (cluster [
        { chalkos.cluster.registries.mirrors."docker.io" = [ "https://mirror.example" ]; }
      ]);
      unit = config.systemd.services.chalkos-registry-mirrors;
      consumers = builtins.fromJSON config.environment.etc."chalkos/consumers.json".text;
    in
    {
      expr = {
        baked = lib.any (lib.hasPrefix "containerd/certs.d") (lib.attrNames config.environment.etc);
        configPath =
          config.virtualisation.containerd.settings.plugins."io.containerd.cri.v1.images".registry.config_path;
        consumer = consumers.chalkos-registry-mirrors.kinds;
        beforeContainerd =
          lib.elem "containerd.service" unit.before && lib.elem "containerd.service" unit.wantedBy;
        afterConfig = lib.elem "chalkos-config.service" unit.after;
      };
      expected = {
        baked = false;
        configPath = "/run/containerd/certs.d";
        consumer = [ "RegistryMirror" ];
        beforeContainerd = true;
        afterConfig = true;
      };
    };
  testRegistryMirrorsRequireHTTPS = {
    expr =
      let
        # The cluster option refuses plain mirrors when the cluster is evaluated.
        mirrors =
          registries:
          (cluster [ { chalkos.cluster = { inherit registries; }; } ]).manifest.registries.mirrors;
      in
      {
        plain = fails (mirrors {
          mirrors."docker.io" = [
            "https://mirror.example.com"
            "http://10.0.2.100:5000"
          ];
        });
        allowed = fails (mirrors {
          allowPlainHTTP = true;
          mirrors."docker.io" = [ "http://10.0.2.100:5000" ];
        });
        https = fails (mirrors {
          mirrors."docker.io" = [ "https://mirror.example.com" ];
        });
      };
    expected = {
      plain = true;
      allowed = false;
      https = false;
    };
  };
  # The image's build-time facts, which chalkd reads with the node's config.
  testImageFacts =
    let
      image = builtins.fromJSON (role (cluster [ ])).environment.etc."chalkos/kubernetes/image.json".text;
      noFlannel =
        builtins.fromJSON
          (role (cluster [ { chalkos.cni.provider = "none"; } ]))
          .environment.etc."chalkos/kubernetes/image.json".text;
    in
    {
      expr = {
        inherit (image) version cni;
        inherit (image.images)
          kubeAPIServer
          kubeProxy
          coredns
          flannel
          ;
        noFlannel = {
          inherit (noFlannel) cni;
          inherit (noFlannel.images) flannel;
        };
      };
      expected = {
        version = "1.37.1";
        cni = "flannel";
        kubeAPIServer = "registry.k8s.io/kube-apiserver:v1.37.1";
        kubeProxy = "registry.k8s.io/kube-proxy:v1.37.1";
        coredns = "registry.k8s.io/coredns/coredns:v1.14.6";
        flannel = "ghcr.io/flannel-io/flannel:v0.28.9";
        noFlannel = {
          cni = "none";
          flannel = "";
        };
      };
    };
  # The kubelet reads its configuration from /run and its flags from KUBELET_ARGS, which chalkd
  # writes; protectKernelDefaults makes it fail unless the kernel has these values.
  testKubeletUnit = {
    expr =
      let
        config = role (cluster [ ]);
        inherit (config.systemd.services.kubelet.serviceConfig) ExecStart;
      in
      {
        sysctl = lib.getAttrs [
          "vm.overcommit_memory"
          "vm.panic_on_oom"
          "kernel.panic"
          "kernel.panic_on_oops"
          "kernel.keys.root_maxkeys"
          "kernel.keys.root_maxbytes"
        ] config.boot.kernel.sysctl;
        flags =
          lib.hasSuffix "$KUBELET_ARGS" ExecStart
          && lib.hasInfix "--config=/run/chalkos/kubernetes/kubelet/config.json" ExecStart
          && !(config.environment.etc ? "chalkos/kubernetes/kubelet.json");
        noFullPackage = !lib.hasInfix "-kubernetes-1.37.1/" ExecStart;
      };
    expected = {
      sysctl = {
        "vm.overcommit_memory" = 1;
        "vm.panic_on_oom" = 0;
        "kernel.panic" = 10;
        "kernel.panic_on_oops" = 1;
        "kernel.keys.root_maxkeys" = 1000000;
        "kernel.keys.root_maxbytes" = 25000000;
      };
      flags = true;
      noFullPackage = true;
    };
  };
  # The kubelet's extra flags come after chalkos's own, so they cannot override where it finds its
  # configuration, credentials and static pods, the node's name, addresses, labels and taints, or
  # who may use its API. The kubelet takes _ for -, so either spelling is refused.
  testKubeletExtraArgsCannotOverrideProtectedFlags = {
    expr =
      let
        clusterFile =
          flags: kubernetesSettings [ { chalkos.cluster.kubernetes.extraArgs.kubelet = flags; } ];
        protected = [
          "anonymous-auth"
          "authentication-token-webhook"
          "authorization-mode"
          "bootstrap-kubeconfig"
          "cert-dir"
          "client-ca-file"
          "config"
          "config-dir"
          "hostname-override"
          "kubeconfig"
          "node-ip"
          "node-labels"
          "pod-manifest-path"
          "read-only-port"
          "register-with-taints"
          "rotate-certificates"
          "tls-cert-file"
          "tls-private-key-file"
        ];
        underscored = map (builtins.replaceStrings [ "-" ] [ "_" ]) protected;
      in
      lib.filter (
        flag:
        !fails (clusterFile {
          ${flag} = "x";
        })
      ) (protected ++ underscored);
    expected = [ ];
  };
  testKubeletExtraArgsAcceptOtherFlags = {
    expr =
      let
        clusterFile =
          flags: kubernetesSettings [ { chalkos.cluster.kubernetes.extraArgs.kubelet = flags; } ];
      in
      {
        other = fails (clusterFile {
          v = "2";
          max-pods = "50";
          max_open_files = "100";
        });
        dashed = fails (clusterFile {
          "--v" = "2";
        });
      };
    expected = {
      other = false;
      dashed = true;
    };
  };
  # Flags reach the components as --name=value, so a name with its dashes, an = or whitespace
  # would set another flag than it names, such as a protected one.
  testExtraArgsRefuseOtherNames = {
    expr =
      let
        clusterFile =
          component: flags:
          kubernetesSettings [ { chalkos.cluster.kubernetes.extraArgs.${component} = flags; } ];
        components = [
          "etcd"
          "kube-apiserver"
          "kube-controller-manager"
          "kube-scheduler"
          "kubelet"
        ];
        names = [
          "--v"
          "-v"
          ""
          "register-with-taints=dedicated"
          "kubeconfig=/var/lib/x"
          "v 2"
          ".v"
        ];
      in
      lib.concatMap (
        component:
        map (name: "${component} ${name}") (
          lib.filter (name: !fails (clusterFile component { ${name} = "2"; })) names
        )
      ) components;
    expected = [ ];
  };
  testAPIServerExtraArgsCannotOverrideAuthentication = {
    expr =
      let
        clusterFile =
          flags: kubernetesSettings [ { chalkos.cluster.kubernetes.extraArgs.kube-apiserver = flags; } ];
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
        underscored = fails (clusterFile {
          anonymous_auth = "true";
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
      underscored = true;
      other = false;
    };
  };
  # The kubelet's flags reach it through an environment variable that ExecStart splits at
  # whitespace, read from an environment file that drops backslashes.
  testKubeletExtraArgsRefuseWhitespaceAndBackslashes = {
    expr =
      let
        clusterFile =
          flags: kubernetesSettings [ { chalkos.cluster.kubernetes.extraArgs.kubelet = flags; } ];
      in
      {
        space = fails (clusterFile {
          eviction-hard = "a=b c=d";
        });
        tab = fails (clusterFile {
          v = "2\t";
        });
        newline = fails (clusterFile {
          v = "2\nX=1";
        });
        backslash = fails (clusterFile {
          eviction-hard = "a=b\\c";
        });
        nameWithSpace = fails (clusterFile {
          "v 2" = "2";
        });
        other = fails (clusterFile {
          v = "2";
          eviction-hard = "a=b,c=d";
        });
      };
    expected = {
      space = true;
      tab = true;
      newline = true;
      backslash = true;
      nameWithSpace = true;
      other = false;
    };
  };
  # VXLAN is accepted only to the addresses chalkd picks: its preparation fills a table of its own
  # that marks such packets, and the firewall accepts the mark. The firewall runs on nftables and
  # its reloads replace its own table only.
  testFlannelVXLANOnlyToNodeIP = {
    expr =
      let
        vxlan =
          modules:
          let
            config = role (cluster modules);
            inherit (config.networking) firewall nftables;
            prepare = config.systemd.services.chalkos-kubernetes;
          in
          {
            nftables = nftables.enable && !nftables.flushRuleset;
            open = lib.elem 8472 firewall.allowedUDPPorts;
            acceptsMark = lib.hasInfix "udp dport 8472 meta mark & 0x01000000 == 0x01000000 accept" firewall.extraInputRules;
            # The ports the node opens, in the table of both families.
            bothFamilies =
              nftables.tables.nixos-fw.family == "inet"
              && lib.hasInfix "tcp dport { 10250, 50000, 30000-32767 } accept" nftables.tables.nixos-fw.content;
            # Emptied before the addresses are picked, filled after.
            emptied =
              let
                pre = prepare.serviceConfig.ExecStartPre or [ ];
              in
              builtins.length pre == 1 && lib.hasSuffix "/bin/chalkos-vxlan-rule" (builtins.elemAt pre 0);
            # The preparation fills the table as its last step, and nothing fills it after a failure.
            prepareFills =
              lib.hasSuffix "/bin/chalkos-vxlan-rule" prepare.serviceConfig.ExecStart
              && !(prepare.serviceConfig ? ExecStartPost);
            afterFirewall = lib.elem "nftables.service" prepare.after;
          };
      in
      {
        flannel = vxlan [ { chalkos.cni.provider = "flannel"; } ];
        none = vxlan [ { chalkos.cni.provider = "none"; } ];
        # Without a firewall there is nothing to accept the mark.
        withoutFirewall = removeAttrs (vxlan [
          { chalkos.roles.worker.nixosModules = [ { networking.firewall.enable = false; } ]; }
        ]) [ "bothFamilies" ];
      };
    expected = {
      flannel = {
        nftables = true;
        open = false;
        acceptsMark = true;
        bothFamilies = true;
        emptied = true;
        prepareFills = true;
        afterFirewall = true;
      };
      none = {
        nftables = true;
        open = false;
        acceptsMark = false;
        bothFamilies = true;
        emptied = false;
        prepareFills = false;
        afterFirewall = false;
      };
      withoutFirewall = {
        nftables = true;
        open = false;
        acceptsMark = true;
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
  # The preparation picks the node's addresses once they settled: after network-online.target by
  # default, and after the units of extensions that add addresses.
  testNodeAddressesTarget = {
    expr =
      let
        # An extension announcing addresses, as a BGP daemon would.
        bgp = {
          systemd.services.bgp = {
            before = [ "chalkos-node-addresses.target" ];
            wantedBy = [ "chalkos-node-addresses.target" ];
            serviceConfig.ExecStart = "/bin/true";
          };
        };
        config = role (cluster [ { chalkos.roles.worker.nixosModules = [ bgp ]; } ]);
        target = config.systemd.targets.chalkos-node-addresses;
        prepare = config.systemd.services.chalkos-kubernetes;
      in
      {
        targetAfter = target.after;
        targetWants = target.wants;
        prepareAfter = lib.elem "chalkos-node-addresses.target" prepare.after;
        prepareWants = lib.elem "chalkos-node-addresses.target" prepare.wants;
        extension = lib.elem "chalkos-node-addresses.target" config.systemd.units."bgp.service".wantedBy;
        # The unit is ordered before the target, so the preparation sees its addresses.
        extensionBefore =
          lib.hasInfix "\nBefore=chalkos-node-addresses.target\n"
            config.systemd.units."bgp.service".text;
      };
    expected = {
      targetAfter = [ "network-online.target" ];
      targetWants = [ "network-online.target" ];
      prepareAfter = true;
      prepareWants = true;
      extension = true;
      extensionBefore = true;
    };
  };
  # A role's images are the role's variant; the installer is no variant, and its os-release
  # names none.
  testInstallerIsNoVariant = {
    expr =
      let
        c = cluster [ ];
      in
      {
        role = (role c).chalkos.variant.name;
        installer = c.installer.nixos.config.chalkos.variant.name;
        installerOSRelease = c.installer.nixos.config.system.nixos.extraOSReleaseArgs ? CHALKOS_VARIANT;
      };
    expected = {
      role = "worker";
      installer = null;
      installerOSRelease = false;
    };
  };
  # An image describes itself in os-release and names its store partitions by its version.
  testImageDescribesItself = {
    expr =
      let
        image = modules: role (cluster [ { chalkos.roles.worker.nixosModules = modules; } ]);
        summary = c: {
          osRelease = lib.filterAttrs (k: _: lib.hasPrefix "CHALKOS_" k) c.system.nixos.extraOSReleaseArgs;
          labels = map (p: p.repartConfig.Label) [
            c.image.repart.partitions."10-store-verity"
            c.image.repart.partitions."20-store"
          ];
        };
      in
      {
        default = summary (image [ ]);
        upgraded = summary (image [
          {
            system.image.version = "0.2.0";
            chalkos.upgrade.bootTries = 1;
          }
        ]);
      };
    expected = {
      default = {
        osRelease = {
          CHALKOS_VARIANT = "worker";
          CHALKOS_PLATFORM = "metal";
          CHALKOS_BOOT_TRIES = "3";
        };
        labels = [
          "store-verity_0.1.0"
          "store_0.1.0"
        ];
      };
      upgraded = {
        osRelease = {
          CHALKOS_VARIANT = "worker";
          CHALKOS_PLATFORM = "metal";
          CHALKOS_BOOT_TRIES = "1";
        };
        labels = [
          "store-verity_0.2.0"
          "store_0.2.0"
        ];
      };
    };
  };
  # A boot the boot loader counts is blessed once the health check passes, within the image's
  # timeout.
  testHealthGate = {
    expr =
      let
        config = role (cluster [
          { chalkos.roles.worker.nixosModules = [ { chalkos.upgrade.healthTimeout = 30; } ]; }
        ]);
        health = config.systemd.services.chalkos-health;
      in
      {
        inherit (health) requiredBy before;
        wait = lib.hasSuffix "/bin/chalkd health --wait 30" health.serviceConfig.ExecStart;
        timeout = health.serviceConfig.TimeoutStartSec;
        bless = config.systemd.units ? "systemd-bless-boot.service";
      };
    expected = {
      requiredBy = [ "boot-complete.target" ];
      before = [ "boot-complete.target" ];
      wait = true;
      timeout = 90;
      bless = true;
    };
  };
  # The manifest carries the db certificate that signs the images, for chalkctl upgrade.
  testManifestNamesTheImageSigner = {
    expr = map (c: c.manifest.secureBoot.signerCertificate) [
      (cluster [ ])
      (cluster [ { chalkos.secureBoot.signerCertificate = builtins.toFile "db.crt" "PEM"; } ])
    ];
    expected = [
      null
      "PEM"
    ];
  };
  # Versions fit the store labels and systemd-boot's entry IDs unchanged.
  testImageVersions = {
    expr =
      lib.genAttrs
        [
          "0.2.0"
          "1.0~rc1"
          "2026.10.9-1"
          "0.2.0+3"
          "0.2.0_1"
          "V1"
          ""
          ".1"
          "1.0.0-abcdefghijklmnopqr"
          "1.0.0-abcdefghijklmnopq"
        ]
        (
          version:
          lib.all (a: a.assertion)
            (role (cluster [
              { chalkos.roles.worker.nixosModules = [ { system.image.version = version; } ]; }
            ])).assertions
        );
    expected = {
      "0.2.0" = true;
      "1.0~rc1" = true;
      "2026.10.9-1" = true;
      "0.2.0+3" = false;
      "0.2.0_1" = false;
      "V1" = false;
      "" = false;
      ".1" = false;
      "1.0.0-abcdefghijklmnopqr" = false;
      "1.0.0-abcdefghijklmnopq" = true;
    };
  };
  # systemd-repart formats a vfat ESP with no less than 260 MiB, so a smaller one is refused.
  testESPMinimum = {
    expr =
      lib.genAttrs
        [
          "1G"
          "260M"
          "1.5G"
          "266240K"
          "256M"
          "0.25G"
          "100M"
          "a lot"
        ]
        (
          size:
          lib.all (a: a.assertion)
            (role (cluster [
              { chalkos.roles.worker.nixosModules = [ { chalkos.disk.espSize = size; } ]; }
            ])).assertions
        );
    expected = {
      "1G" = true;
      "260M" = true;
      "1.5G" = true;
      "266240K" = true;
      "256M" = false;
      "0.25G" = false;
      "100M" = false;
      "a lot" = false;
    };
  };
  # systemd-repart makes a new LUKS2-encrypted ext4 partition no smaller than 64 MiB, so a smaller
  # STATE is refused.
  testStateMinimum = {
    expr =
      lib.genAttrs
        [
          "128M"
          "64M"
          "65536K"
          "1G"
          "63M"
          "32M"
          "a lot"
        ]
        (
          size:
          lib.all (a: a.assertion)
            (role (cluster [
              { chalkos.roles.worker.nixosModules = [ { chalkos.disk.stateSize = size; } ]; }
            ])).assertions
        );
    expected = {
      "128M" = true;
      "64M" = true;
      "65536K" = true;
      "1G" = true;
      "63M" = false;
      "32M" = false;
      "a lot" = false;
    };
  };
  # The ESP is mounted at /efi when used: upgrades write UKIs there, systemd-bless-boot renames
  # them, and systemd-boot-random-seed.service refreshes the boot loader's seed.
  testESPMount = {
    expr =
      let
        c = role (cluster [ ]);
      in
      {
        inherit (c.fileSystems."/efi") device fsType options;
        seed = c.systemd.services.systemd-boot-random-seed.environment.SYSTEMD_ESP_PATH;
        bless = c.systemd.services.systemd-bless-boot.environment.SYSTEMD_ESP_PATH;
      };
    expected = {
      device = "/dev/disk/chalk-boot/esp";
      fsType = "vfat";
      options = [
        "umask=0077"
        "nofail"
        "x-systemd.automount"
        "x-systemd.idle-timeout=1min"
      ];
      seed = "/efi";
      bless = "/efi";
    };
  };
  testStoreCompression = {
    expr = (role (cluster [ ])).image.repart.mkfsOptions.erofs;
    expected = [
      "-b 4096"
      "-zzstd,level=9"
      "-C65536"
    ];
  };
  # dm-verity refuses blocks smaller than a disk's logical block size; 4 KiB blocks also make the
  # hash tree an eighth of what 512-byte ones do.
  testStoreVerityBlocks = {
    expr =
      let
        c = role (cluster [ ]);
        inherit (c.image.repart.verityStore) partitionIds;
      in
      lib.intersectAttrs {
        VerityDataBlockSizeBytes = null;
        VerityHashBlockSizeBytes = null;
      } c.image.repart.partitions.${partitionIds.store-verity}.repartConfig;
    expected = {
      VerityDataBlockSizeBytes = 4096;
      VerityHashBlockSizeBytes = 4096;
    };
  };
  # Building an image checks that it leaves room for the next: its store in a slot, its hash tree in
  # the slot's verity partition, and on the ESP the UKIs of both slots and an upgrade's; the
  # installer, never upgraded, holds one UKI.
  testImageFitCheck = {
    expr =
      let
        c = cluster [ ];
        summary = fits: {
          inherit (fits)
            ukis
            storeSize
            storeVeritySize
            espSize
            ;
        };
      in
      {
        role = summary (role c).system.build.chalkosImage.fits;
        installer = summary c.installer.image.fits;
      };
    expected = {
      role = {
        ukis = 3;
        storeSize = "3G";
        storeVeritySize = "128M";
        espSize = "1G";
      };
      installer = {
        ukis = 1;
        storeSize = "-";
        storeVeritySize = "-";
        espSize = "260M";
      };
    };
  };
  testInitrdCompression = {
    expr = with (role (cluster [ ])).boot.initrd; [
      compressor
      compressorArgs
    ];
    expected = [
      "zstd"
      [
        "-19"
        "-T0"
      ]
    ];
  };
  # The UKI carries the kernel and the initrd; the store holds the modules alone. nixos-init reads
  # the bootspec in the initrd, so it is written without them.
  testStoreLeavesOutKernelAndInitrd = {
    expr =
      let
        c = role (cluster [ ]);
        forbidden =
          path: lib.any (r: builtins.match r "${path}" != null) c.system.forbiddenDependenciesRegexes;
      in
      {
        kernel = forbidden c.boot.kernelPackages.kernel;
        initrd = forbidden c.system.build.initialRamdisk;
        modules = forbidden c.system.modulesTree;
        bootspec = map (s: lib.hasInfix s c.boot.bootspec.writer) [
          "/boot.json"
          "del(.initrd) | .kernel = $kernel"
        ];
      };
    expected = {
      kernel = true;
      initrd = true;
      modules = false;
      bootspec = [
        true
        true
      ];
    };
  };
  # The system path holds what the image's modules and the role put there, such as the mount
  # helpers util-linux looks up there, and none of NixOS's default packages. The debug tools add a
  # shell's tools, and crictl where the role runs Kubernetes. Units name their own tools.
  testSystemPath = {
    expr =
      let
        image =
          roleConfig: modules:
          role (cluster [
            {
              chalkos.roles.worker = roleConfig // {
                nixosModules = modules;
              };
            }
          ]);
        names = c: map lib.getName c.environment.systemPackages;
        # What modules add to a role's system path.
        added =
          roleConfig: modules:
          lib.sort lib.lessThan (
            lib.subtractLists (names (image roleConfig [ ])) (names (image roleConfig modules))
          );
        debug = [ { chalkos.debug.tools = true; } ];
        c = image { } [ ];
      in
      {
        roleAdds = added { } [ ({ pkgs, ... }: { environment.systemPackages = [ pkgs.hello ]; }) ];
        nixosDefaults = lib.intersectLists [
          "nano"
          "sudo"
          "openssh"
          "coreutils-full"
          "bind"
          "host"
          "perl"
          "rsync"
          "strace"
        ] (names c);
        nfs = lib.elem "nfs-utils" (names (image { } [ { boot.supportedFilesystems.nfs = true; } ]));
        debug = added { } debug;
        debugWithoutKubernetes = added { kubernetes.enable = false; } debug;
        # D-Bus finds systemd's services and policies through the system path.
        dbus = lib.elem c.system.path c.services.dbus.packages && lib.elem "systemd" (names c);
        locales = c.i18n.supportedLocales;
        chalkd = lib.sort lib.lessThan (map lib.getName c.systemd.services.chalkd.path);
      };
    expected = {
      roleAdds = [ "hello" ];
      nixosDefaults = [ ];
      nfs = true;
      debug = [
        "coreutils"
        "cri-tools"
        "findutils"
        "gnugrep"
        "gnused"
        "iproute2"
        "procps"
        "util-linux"
      ];
      debugWithoutKubernetes = [
        "coreutils"
        "findutils"
        "gnugrep"
        "gnused"
        "iproute2"
        "procps"
        "util-linux"
      ];
      locales = [ "C.UTF-8/UTF-8" ];
      dbus = true;
      chalkd = [
        "btrfs-progs"
        "chrony"
        "coreutils"
        "cryptsetup"
        "dosfstools"
        "e2fsprogs"
        "efibootmgr"
        "findutils"
        "gnugrep"
        "gnused"
        "systemd"
        "systemd"
        "util-linux"
        "xfsprogs"
      ];
    };
  };
}
