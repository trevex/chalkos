# Evaluation tests of chalkos.lib.mkImages and the images it builds; returns lib.runTests failures
# (empty on success).
{
  lib,
  pkgs,
  mkImages,
}:
let
  fails = value: !(builtins.tryEval (builtins.deepSeq value true)).success;
  # The metal image of a variant worker, which runs Kubernetes, with the NixOS modules given.
  worker = modules: (mkImages { variants.worker.modules = modules; }).nixos.worker-metal.config;
in
lib.runTests {
  # mkImages builds each variant on each platform, metal and kvm unless the arguments name
  # others, as <variant>-<platform>, and one installer; without variants it builds node.
  testMkImagesNamesImagesByVariantAndPlatform = {
    expr =
      let
        i = mkImages {
          variants = {
            node = { };
            storage.kubernetes = false;
          };
        };
      in
      {
        images = lib.attrNames i.images;
        nixos = lib.attrNames i.nixos;
        build = lib.all lib.isDerivation (lib.attrValues i.images);
        installer = lib.isDerivation i.installer && lib.hasInfix "chalkos-installer" i.installer.name;
        default = lib.attrNames (mkImages { }).images;
        onePlatform = lib.attrNames (mkImages { platforms = [ "kvm" ]; }).images;
      };
    expected = {
      images = [
        "node-kvm"
        "node-metal"
        "storage-kvm"
        "storage-metal"
      ];
      nixos = [
        "installer"
        "node-kvm"
        "node-metal"
        "storage-kvm"
        "storage-metal"
      ];
      build = true;
      installer = true;
      default = [
        "node-kvm"
        "node-metal"
      ];
      onePlatform = [ "node-kvm" ];
    };
  };
  # Each image names its variant and platform, runs Kubernetes as its variant says and carries the
  # OS CA; the installer is no variant, belongs to no platform and runs no Kubernetes.
  testMkImagesDescribesEachImage = {
    expr =
      let
        pub = builtins.toFile "secrets.pub.json" ''{"version": 3, "osCA": {"certificate": "PEM"}}'';
        i = mkImages {
          osCA = pub;
          variants = {
            node = { };
            storage.kubernetes = false;
          };
          platforms = [ "metal" ];
        };
        summary = n: {
          variant = n.config.chalkos.variant.name;
          platform = n.config.chalkos.platform.name;
          kubernetes = n.config.chalkos.kubernetes.enable;
          osCA = n.config.environment.etc ? "chalkos/os-ca.crt";
        };
      in
      lib.mapAttrs (_: summary) i.nixos;
    expected = {
      installer = {
        variant = null;
        platform = null;
        kubernetes = false;
        osCA = true;
      };
      node-metal = {
        variant = "node";
        platform = "metal";
        kubernetes = true;
        osCA = true;
      };
      storage-metal = {
        variant = "storage";
        platform = "metal";
        kubernetes = false;
        osCA = true;
      };
    };
  };
  # A variant's images carry their platform's modules, those of every variant and the variant's
  # own; the installer carries its own modules alone.
  testMkImagesAppliesModules = {
    expr =
      let
        mark = m: { boot.kernelParams = [ m ]; };
        i = mkImages {
          platforms.metal = [ (mark "platform") ];
          modules = [ (mark "every") ];
          variants.node.modules = [ (mark "variant") ];
          installer.modules = [ (mark "installer") ];
        };
        marks =
          n:
          lib.filter (
            p:
            lib.elem p [
              "platform"
              "every"
              "variant"
              "installer"
            ]
          ) n.config.boot.kernelParams;
      in
      {
        node = lib.sort lib.lessThan (marks i.nixos.node-metal);
        installer = marks i.nixos.installer;
        # metal's modules of chalkos stay when platforms.metal gives more: console=tty0 is theirs.
        console = lib.elem "console=tty0" i.nixos.node-metal.config.boot.kernelParams;
      };
    expected = {
      node = [
        "every"
        "platform"
        "variant"
      ];
      installer = [ "installer" ];
      console = true;
    };
  };
  # Variant and platform names become os-release values and image names; two images of one name
  # are refused, as are arguments mkImages does not have.
  testMkImagesRefuses = {
    expr = {
      platform = fails (lib.attrNames (mkImages { platforms = [ "Bad" ]; }).images);
      platformModules = fails (lib.attrNames (mkImages { platforms."x.y" = [ ]; }).images);
      variant = fails (lib.attrNames (mkImages { variants.Node = { }; }).images);
      collision = fails (
        lib.attrNames
          (mkImages {
            platforms = [
              "metal"
              "b-metal"
            ];
            variants = {
              a = { };
              a-b = { };
            };
          }).images
      );
      unknown = fails (lib.attrNames (mkImages { roles.node = { }; }).images);
      noPlatform = fails (lib.attrNames (mkImages { platforms = [ ]; }).images);
      duplicatePlatform = fails (
        lib.attrNames
          (mkImages {
            platforms = [
              "kvm"
              "kvm"
            ];
          }).images
      );
      custom = (mkImages { platforms.esxi-8 = [ ]; }).nixos.node-esxi-8.config.chalkos.platform.name;
    };
    expected = {
      platform = true;
      platformModules = true;
      variant = true;
      collision = true;
      unknown = true;
      noPlatform = true;
      duplicatePlatform = true;
      custom = "esxi-8";
    };
  };
  # The OS CA is the public part of secrets of the version chalkos reads.
  testMkImagesChecksOSCA = {
    expr =
      map
        (
          version:
          fails
            (mkImages {
              osCA = builtins.toFile "secrets.pub.json" ''{"version": ${toString version}, "osCA": {"certificate": "PEM"}}'';
            }).nixos.node-metal.config.environment.etc."chalkos/os-ca.crt".source.drvPath
        )
        [
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
  # mkImages evaluates the images with the nixpkgs and the module arguments given.
  testMkImagesUsesItsNixpkgs = {
    expr =
      let
        n =
          (mkImages {
            nixpkgs.lib.nixosSystem = args: {
              marker = "custom";
              inherit (args) specialArgs;
            };
            specialArgs.answer = 42;
          }).nixos.node-metal;
      in
      {
        inherit (n) marker;
        inherit (n.specialArgs) answer;
      };
    expected = {
      marker = "custom";
      answer = 42;
    };
  };
  # A variant's modules set the image's Kubernetes release and pod network, which its image.json
  # names.
  testVariantSetsKubernetesImageOptions = {
    expr =
      let
        n =
          (mkImages {
            variants.node.modules = [
              {
                chalkos.kubernetes.images.etcd = "registry.example/etcd:1";
                chalkos.cni.provider = "none";
              }
            ];
          }).nixos.node-metal;
        facts = builtins.fromJSON n.config.environment.etc."chalkos/kubernetes/image.json".text;
      in
      {
        inherit (facts) cni;
        inherit (facts.images) etcd;
      };
    expected = {
      cni = "none";
      etcd = "registry.example/etcd:1";
    };
  };
  testImageRejectsUnknownSetting = {
    expr = fails (worker [ { chalkos.disks.espSize = "1G"; } ]).chalkos.disk;
    expected = true;
  };
  testVariantSetsDiskOptions = {
    expr = (worker [ { chalkos.disk.espSize = "256M"; } ]).chalkos.disk.espSize;
    expected = "256M";
  };
  testVariantModulesApply = {
    expr = (worker [ { networking.hostName = "shared"; } ]).networking.hostName;
    expected = "shared";
  };
  testImageRunsChalkd = {
    expr =
      let
        services = (worker [ ]).systemd.services;
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
        i = mkImages { variants.worker = { }; };
        conf = config: lib.splitString "\n" config.environment.etc."systemd/resolved.conf".text;
      in
      map
        (lines: {
          llmnr = lib.elem "LLMNR=false" lines;
          mdns = lib.elem "MulticastDNS=false" lines;
        })
        [
          (conf i.nixos.worker-metal.config)
          (conf i.nixos.installer.config)
        ];
    expected = lib.replicate 2 {
      llmnr = true;
      mdns = true;
    };
  };
  # The OS CA is an argument of mkImages, which every image and the installer carry as a file; the
  # images have no setting of it besides chalkos.maintenance.osCA.
  testImagesCarryOSCA = {
    expr =
      let
        pub = builtins.toFile "secrets.pub.json" ''{"version": 3, "osCA": {"certificate": "PEM"}}'';
        withCA = mkImages {
          osCA = pub;
          variants.worker = { };
        };
        has = config: config.environment.etc ? "chalkos/os-ca.crt";
      in
      {
        withCA = has withCA.nixos.worker-metal.config;
        installer = has withCA.nixos.installer.config;
        withoutCA = has (worker [ ]);
        setting = withCA.nixos.worker-metal.config.chalkos ? osCA;
      };
    expected = {
      withCA = true;
      installer = true;
      withoutCA = false;
      setting = false;
    };
  };
  testChrony = {
    expr =
      let
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
      # The units are always there; TimeSync.spec.dhcp decides at runtime.
      summary (worker [ ]);
    expected = {
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
  };
  # The renewal hook of chalkd exists on the test image alone.
  testRenewalHookOnTestImagesOnly = {
    expr =
      let
        hook = c: c.systemd.services.chalkd.environment ? CHALKD_TEST_RENEW_ON_APPLY_CONFIG;
      in
      {
        image = hook (worker [ ]);
        testImage = hook (worker [ ../../modules/testing/test-image.nix ]);
      };
    expected = {
      image = false;
      testImage = true;
    };
  };
  # The installer's modules reach its image, whose console is on the screen and the first serial
  # port; it is no variant and belongs to no platform, as it installs images of every one.
  testInstallerModules = {
    expr =
      let
        i = mkImages {
          installer.modules = [
            {
              systemd.network.networks."10-uplink" = {
                matchConfig.MACAddress = "52:54:00:12:34:56";
                address = [ "10.0.0.5/24" ];
              };
              chalkos.kernel.moduleGroups = [ "wireless" ];
            }
          ];
        };
        inherit (i.nixos.installer) config;
      in
      {
        uplink = config.systemd.network.networks."10-uplink".matchConfig.MACAddress;
        wireless = lib.elem "wireless" config.chalkos.kernel.moduleGroups;
        consoles = lib.filter (lib.hasPrefix "console=") config.boot.kernelParams;
        osRelease = lib.filterAttrs (
          k: _: lib.hasPrefix "CHALKOS_" k
        ) config.system.nixos.extraOSReleaseArgs;
        image = i.installer.drvPath == config.system.build.chalkosInstaller.drvPath;
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
  # A variant is built for each platform, metal and kvm unless the arguments name others; each
  # image names its platform in os-release and carries that platform's console and agents alone.
  testPlatformImages = {
    expr =
      let
        i = mkImages { variants.worker = { }; };
        summary = c: {
          platform = c.system.nixos.extraOSReleaseArgs.CHALKOS_PLATFORM;
          consoles = lib.filter (lib.hasPrefix "console=") c.boot.kernelParams;
          guestAgent = c.services.qemuGuest.enable;
          condition = c.systemd.services.qemu-guest-agent.unitConfig.ConditionVirtualization or null;
        };
      in
      {
        images = lib.attrNames i.images;
        imagesBuild = lib.all lib.isDerivation (lib.attrValues i.images);
        metal = summary i.nixos.worker-metal.config;
        kvm = summary i.nixos.worker-kvm.config;
      };
    expected = {
      images = [
        "worker-kvm"
        "worker-metal"
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
      config = (mkImages { variants.worker = { }; }).nixos.worker-kvm.config;
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
  # SCSI drivers, for the image the lab template's nodes run. A metal image leaves them out, as
  # real machines have no virtio disks.
  testKvmInitrdVirtio = {
    expr =
      let
        i = mkImages { variants.worker = { }; };
        virtio =
          platform:
          lib.sort lib.lessThan (
            lib.unique (
              lib.filter (lib.hasPrefix "virtio")
                i.nixos."worker-${platform}".config.boot.initrd.availableKernelModules
            )
          );
      in
      {
        kvm = virtio "kvm";
        metal = virtio "metal";
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
  # The arguments add a platform of their own, or modules to one of chalkos's. A variant overrides
  # a value its platform sets by priority, as module order does not: lib.mkForce for a value the
  # platform defines plainly, lib.mkOverride below 50 for the guest agent's ExecStart, which kvm
  # forces. Both add to a list.
  testCustomPlatform = {
    expr =
      let
        i = mkImages {
          platforms = {
            esxi = [
              {
                services.openssh.enable = true;
                environment.variables.PLATFORM_TEST = "esxi";
              }
            ];
            metal = [ { chalkos.kernel.moduleGroups = [ "gpu" ]; } ];
            kvm = [ ];
          };
          variants.worker.modules = [
            {
              boot.kernelParams = [ "variant" ];
              environment.variables.PLATFORM_TEST = lib.mkForce "variant";
              systemd.services.qemu-guest-agent.serviceConfig.ExecStart = lib.mkOverride 49 "variant-agent";
            }
          ];
        };
        esxi = i.nixos.worker-esxi.config;
        kvm = i.nixos.worker-kvm.config;
        metal = i.nixos.worker-metal.config;
      in
      {
        images = lib.attrNames i.images;
        esxi = esxi.services.openssh.enable;
        esxiPlatform = esxi.system.nixos.extraOSReleaseArgs.CHALKOS_PLATFORM;
        kvm = kvm.services.openssh.enable;
        metalGPU = lib.elem "gpu" metal.chalkos.kernel.moduleGroups;
        kvmGPU = lib.elem "gpu" kvm.chalkos.kernel.moduleGroups;
        kernelParams = lib.sort lib.lessThan (
          lib.filter (p: p == "variant" || p == "console=ttyS0,115200") kvm.boot.kernelParams
        );
        overridden = esxi.environment.variables.PLATFORM_TEST;
        agent = kvm.systemd.services.qemu-guest-agent.serviceConfig.ExecStart;
      };
    expected = {
      images = [
        "worker-esxi"
        "worker-kvm"
        "worker-metal"
      ];
      esxi = true;
      esxiPlatform = "esxi";
      kvm = false;
      metalGPU = true;
      kvmGPU = false;
      kernelParams = [
        "console=ttyS0,115200"
        "variant"
      ];
      overridden = "variant";
      agent = "variant-agent";
    };
  };
  # The installer runs chalkd in installer mode and belongs to no platform, whatever its modules
  # say.
  testInstallerEssentials =
    let
      installer = modules: (mkImages { installer.modules = modules; }).installer.drvPath;
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
  testStateHasItsOwnPartitionType = {
    expr = (worker [ ]).systemd.repart.partitions."50-state".Type;
    expected = "480b7842-1236-1abb-0339-e5503b43122a";
  };
  # containerd runs portmap, which needs nft.
  testContainerdFindsNft = {
    expr = lib.any (p: (p.pname or "") == "nftables") (worker [ ]).systemd.services.containerd.path;
    expected = true;
  };
  # The image fixes its Kubernetes release, the images of etcd, the pod sandbox and CoreDNS, and
  # its pod network, as options of its own; testVariantSetsKubernetesImageOptions sets them.
  testImageKubernetesOptions = {
    expr =
      let
        c = worker [ ];
      in
      {
        inherit (c.chalkos.kubernetes) enable;
        version = c.chalkos.kubernetes.package.version;
        inherit (c.chalkos.kubernetes.images) etcd pause coredns;
        inherit (c.chalkos.cni) provider;
        flannel = c.chalkos.cni.flannel.image;
      };
    expected = {
      enable = true;
      version = pkgs.kubernetes.version;
      etcd = "registry.k8s.io/etcd:3.7.0-0";
      pause = "registry.k8s.io/pause:3.10.2";
      coredns = "registry.k8s.io/coredns/coredns:v1.14.6";
      provider = "flannel";
      flannel = "ghcr.io/flannel-io/flannel:v0.28.9";
    };
  };
  testKubernetesOnlyInKubernetesVariants = {
    expr =
      let
        image = enable: (mkImages { variants.worker.kubernetes = enable; }).nixos.worker-metal.config;
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
        config = worker [ ];
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
        # chalkd renders the add-ons and reads the Addon documents from the config; the image
        # ships no objects of its own.
        addonObjects = config.environment.etc ? "chalkos/kubernetes/manifests.json";
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
            unit = (worker [ { networking.firewall.enable = false; } ]).systemd.services.chalkos-controlplane;
          in
          lib.elem "nftables.service" unit.after;
      };
    expected = {
      clusterFile = false;
      firewall = false;
      apiServerPort = false;
      markedPorts = true;
      addonObjects = false;
      etcdDir = false;
      consumer = [ "Node" ];
      beforeKubernetes = true;
      etcdMount = "/var/lib/etcd";
      kubeletEtcdMount = "/var/lib/etcd";
      afterAbsentFirewall = false;
    };
  };
  # One directory with the plugins the image names, copied out of nixpkgs's packages: those
  # flannel's network runs, plus flannel's own plugin, or with no network every reference plugin;
  # a variant adds to them or narrows them.
  testCNIPluginsFollowProvider = {
    expr =
      let
        image = settings: worker [ { chalkos.cni = settings; } ];
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
            provider: "${(worker [ { chalkos.cni.provider = provider; } ]).system.build.chalkosCNIPlugins}/bin";
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
  # An image carries the base groups of kernel modules plus what it adds; the initrd takes its
  # modules from the same tree, and a module the image loads must be in it. The build check
  # kernel-modules runs the filter and the check.
  testKernelModules = {
    expr =
      let
        image = worker;
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
        c = worker [ ];
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
      config = worker [ ];
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
  # The image's build-time facts, which chalkd reads with the node's config.
  testImageFacts =
    let
      image = builtins.fromJSON (worker [ ]).environment.etc."chalkos/kubernetes/image.json".text;
      noFlannel =
        builtins.fromJSON
          (worker [ { chalkos.cni.provider = "none"; } ])
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
        version = pkgs.kubernetes.version;
        cni = "flannel";
        kubeAPIServer = "registry.k8s.io/kube-apiserver:v${pkgs.kubernetes.version}";
        kubeProxy = "registry.k8s.io/kube-proxy:v${pkgs.kubernetes.version}";
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
        config = worker [ ];
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
        noFullPackage = !lib.hasInfix "-kubernetes-${pkgs.kubernetes.version}/" ExecStart;
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
  # VXLAN is accepted only to the addresses chalkd picks: its preparation fills a table of its own
  # that marks such packets, and the firewall accepts the mark. The firewall runs on nftables and
  # its reloads replace its own table only.
  testFlannelVXLANOnlyToNodeIP = {
    expr =
      let
        vxlan =
          modules:
          let
            config = worker modules;
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
          { networking.firewall.enable = false; }
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
        chalkd = (worker [ ]).systemd.services.chalkd;
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
        config = worker [ bgp ];
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
  # The installer is no variant, and its os-release names none.
  testInstallerIsNoVariant = {
    expr =
      let
        i = mkImages { variants.worker = { }; };
      in
      {
        variant = i.nixos.worker-metal.config.chalkos.variant.name;
        installer = i.nixos.installer.config.chalkos.variant.name;
        installerOSRelease = i.nixos.installer.config.system.nixos.extraOSReleaseArgs ? CHALKOS_VARIANT;
      };
    expected = {
      variant = "worker";
      installer = null;
      installerOSRelease = false;
    };
  };
  # An image describes itself in os-release, with an empty IMAGE_VERSION. The build labels its
  # store partitions without a version; chalkosImage relabels slot A by the root hash.
  testImageDescribesItself = {
    expr =
      let
        image = worker;
        summary = c: {
          osRelease = lib.filterAttrs (
            k: _: lib.hasPrefix "CHALKOS_" k || k == "IMAGE_VERSION"
          ) c.system.nixos.extraOSReleaseArgs;
          labels = map (p: p.repartConfig.Label) [
            c.image.repart.partitions."10-store-verity"
            c.image.repart.partitions."20-store"
          ];
        };
      in
      {
        default = summary (image [ ]);
        upgraded = summary (image [ { chalkos.upgrade.bootTries = 1; } ]);
      };
    expected = {
      default = {
        osRelease = {
          IMAGE_VERSION = "";
          CHALKOS_VARIANT = "worker";
          CHALKOS_PLATFORM = "metal";
          CHALKOS_BOOT_TRIES = "3";
        };
        labels = [
          "store-verity"
          "store"
        ];
      };
      upgraded = {
        osRelease = {
          IMAGE_VERSION = "";
          CHALKOS_VARIANT = "worker";
          CHALKOS_PLATFORM = "metal";
          CHALKOS_BOOT_TRIES = "1";
        };
        labels = [
          "store-verity"
          "store"
        ];
      };
    };
  };
  # A boot the boot loader counts is blessed once the health check passes, within the image's
  # timeout.
  testHealthGate = {
    expr =
      let
        config = worker [ { chalkos.upgrade.healthTimeout = 30; } ];
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
  # An image has no version: chalkos orders boot entries by generation, so a node image or the
  # installer that sets system.image.version fails to build. The os-release the UKI carries names
  # an empty IMAGE_VERSION, without which systemd-boot would order entries by VERSION. The UKI
  # builds as chalkos.efi and the image as chalkos.raw, whose ESP holds the UKI as the first
  # generation.
  testImageHasNoVersion = {
    expr =
      let
        message = "chalkos orders boot entries by generation; pass revision and tag to mkImages for provenance";
        refused = c: lib.any (a: !a.assertion && a.message == message) c.assertions;
        installer =
          modules:
          (mkImages {
            variants = { };
            installer = { inherit modules; };
          }).nixos.installer.config;
        node = worker [ ];
        osReleaseLines =
          c:
          lib.filter (l: lib.hasPrefix "IMAGE_" l || lib.hasPrefix "CHALKOS_" l) (
            lib.splitString "\n" c.environment.etc."os-release".text
          );
      in
      {
        node = refused (worker [ { system.image.version = "0.2.0"; } ]);
        installer = refused (installer [ { system.image.version = "0.2.0"; } ]);
        default = refused node || refused (installer [ ]);
        # The os-release file the UKI embeds, as its text; reading the built UKI's .osrel would
        # need the system built during evaluation.
        osRelease = osReleaseLines node;
        installerOSRelease = osReleaseLines (installer [ ]);
        ukiOSRelease = node.boot.uki.settings.UKI.OSRelease == "@${node.system.build.etc}/etc/os-release";
        inherit (node.system.boot.loader) ukiFile;
        inherit (node.image) fileName;
        inherit (node.image.repart.verityStore) ukiPath;
        installerFile = (installer [ ]).image.fileName;
        installerUKIPath = (installer [ ]).image.repart.verityStore.ukiPath;
      };
    expected = {
      node = true;
      installer = true;
      default = false;
      osRelease = [
        "CHALKOS_BOOT_TRIES=3"
        "CHALKOS_PLATFORM=metal"
        "CHALKOS_VARIANT=worker"
        "IMAGE_ID=chalkos"
        "IMAGE_VERSION=\"\""
      ];
      installerOSRelease = [
        "CHALKOS_BOOT_TRIES=3"
        "IMAGE_ID=\"chalkos-installer\""
        "IMAGE_VERSION=\"\""
      ];
      ukiOSRelease = true;
      ukiFile = "chalkos.efi";
      fileName = "chalkos.raw";
      ukiPath = "/EFI/Linux/chalkos_1.efi";
      installerFile = "chalkos-installer.raw";
      installerUKIPath = "/EFI/Linux/chalkos-installer_1.efi";
    };
  };
  # The image ID names the UKIs' boot entries, so a node image or the installer whose ID holds
  # other characters than lower-case letters, digits, ".", "_" and "-" fails to build: systemd-boot
  # lowercases only uncounted entry IDs, and a "+" starts the tries counter.
  testImageIDShape = {
    expr =
      let
        message = "system.image.id must consist of lower-case letters, digits, \".\", \"_\" and \"-\", as it names the image's boot entries";
        refused = c: lib.any (a: !a.assertion && a.message == message) c.assertions;
        installer =
          modules:
          (mkImages {
            variants = { };
            installer = { inherit modules; };
          }).nixos.installer.config;
        id = id: worker [ { system.image.id = lib.mkForce id; } ];
      in
      {
        default = refused (worker [ ]) || refused (installer [ ]);
        allowed = refused (id "chalkos.lab_2-x");
        upper = refused (id "Chalkos");
        plus = refused (id "chalkos+1");
        space = refused (id "chalk os");
        empty = refused (id "");
        installer = refused (installer [ { system.image.id = lib.mkForce "chalkos+installer"; } ]);
      };
    expected = {
      default = false;
      allowed = false;
      upper = true;
      plus = true;
      space = true;
      empty = true;
      installer = true;
    };
  };
  # mkImages gives its revision and tag to every image and the installer, which only metadata.json
  # reads: the store, and so the root hash, is the same without them. Both default to null.
  testMkImagesPassesProvenance = {
    expr =
      let
        given = mkImages {
          revision = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678";
          tag = "v1.5.0";
          platforms = [ "metal" ];
        };
        none = mkImages { platforms = [ "metal" ]; };
      in
      {
        given = lib.mapAttrs (_: n: n.config.chalkos.provenance) given.nixos;
        none = none.nixos.node-metal.config.chalkos.provenance;
        sameStore =
          given.nixos.node-metal.config.system.build.toplevel.drvPath
          == none.nixos.node-metal.config.system.build.toplevel.drvPath;
        mistyped = fails (mkImages { revision = 1; }).nixos.node-metal.config.chalkos.provenance;
      };
    expected = {
      given = {
        installer = {
          revision = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678";
          tag = "v1.5.0";
        };
        node-metal = {
          revision = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678";
          tag = "v1.5.0";
        };
      };
      none = {
        revision = null;
        tag = null;
      };
      sameStore = true;
      mistyped = true;
    };
  };
  # systemd-repart formats a vfat ESP with no less than 260 MiB, so a smaller one is refused.
  testESPMinimum = {
    expr = lib.genAttrs [
      "1G"
      "260M"
      "1.5G"
      "266240K"
      "256M"
      "0.25G"
      "100M"
      "a lot"
    ] (size: lib.all (a: a.assertion) (worker [ { chalkos.disk.espSize = size; } ]).assertions);
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
    expr = lib.genAttrs [
      "128M"
      "64M"
      "65536K"
      "1G"
      "63M"
      "32M"
      "a lot"
    ] (size: lib.all (a: a.assertion) (worker [ { chalkos.disk.stateSize = size; } ]).assertions);
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
        c = worker [ ];
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
    expr = (worker [ ]).image.repart.mkfsOptions.erofs;
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
        c = worker [ ];
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
        i = mkImages { variants.worker = { }; };
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
        image = summary i.images.worker-metal.fits;
        installer = summary i.installer.fits;
      };
    expected = {
      image = {
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
    expr = with (worker [ ]).boot.initrd; [
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
        c = worker [ ];
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
  # The system path holds what the image's modules and the variant put there, such as the mount
  # helpers util-linux looks up there, and none of NixOS's default packages. The debug tools add a
  # shell's tools, and crictl where the variant runs Kubernetes. Units name their own tools.
  testSystemPath = {
    expr =
      let
        image =
          variant: modules:
          (mkImages {
            variants.worker = variant // {
              inherit modules;
            };
          }).nixos.worker-metal.config;
        names = c: map lib.getName c.environment.systemPackages;
        # What modules add to a variant's system path.
        added =
          variant: modules:
          lib.sort lib.lessThan (
            lib.subtractLists (names (image variant [ ])) (names (image variant modules))
          );
        debug = [ { chalkos.debug.tools = true; } ];
        c = image { } [ ];
      in
      {
        variantAdds = added { } [ ({ pkgs, ... }: { environment.systemPackages = [ pkgs.hello ]; }) ];
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
        debugWithoutKubernetes = added { kubernetes = false; } debug;
        # D-Bus finds systemd's services and policies through the system path.
        dbus = lib.elem c.system.path c.services.dbus.packages && lib.elem "systemd" (names c);
        locales = c.i18n.supportedLocales;
        chalkd = lib.sort lib.lessThan (map lib.getName c.systemd.services.chalkd.path);
      };
    expected = {
      variantAdds = [ "hello" ];
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
