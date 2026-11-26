# Evaluation tests of chalkos.lib's image functions and the images they build; returns
# the tests in the form of lib.runTests, which the eval check runs.
{
  lib,
  pkgs,
  flakeLib,
  layers,
}:
let
  inherit (flakeLib)
    kubernetesReleases
    mkImage
    mkImages
    mkInstaller
    nixosOf
    ociOf
    selectImages
    testsOf
    ;
  inherit (import ./lib.nix { inherit lib flakeLib layers; })
    fails
    refuses
    etcJSON
    hasPackage
    worker
    installer
    router
    routerWithout
    ;
  # The newest Kubernetes minor chalkos pins, and the kubelet of a minor, as the Kubernetes layer
  # builds it.
  newestKubernetes = lib.last (lib.sort lib.versionOlder (lib.attrNames kubernetesReleases));
  kubeletOf =
    minor:
    pkgs.callPackage ../../modules/node/kubernetes/kubelet.nix {
      inherit (kubernetesReleases.${minor}) version hash;
    };
  # An image of each set of layers on metal: node has the Kubernetes layer, plain none, edge the
  # router layer.
  layered = nixosOf (mkImages {
    images = [
      {
        name = "node-metal";
        platform = "metal";
        modules = [ layers.kubernetes ];
      }
      {
        name = "plain-metal";
        platform = "metal";
      }
      {
        name = "edge-metal";
        platform = "metal";
        modules = [ layers.router ];
      }
    ];
  });
  # The images most tests read, each evaluated once: a Kubernetes worker without modules on metal
  # and kvm with the installer, the router with every feature, and the router without each
  # feature.
  workerImages = mkImages {
    images =
      map
        (platform: {
          name = "worker-${platform}";
          inherit platform;
          modules = [ layers.kubernetes ];
        })
        [
          "metal"
          "kvm"
        ];
  };
  w = workerImages.worker-metal.nixos.config;
  edge = layered.edge-metal.config;
  inherit (layered.edge-metal._module.args.routerLib) features;
  without = lib.genAttrs features (f: routerWithout [ f ]);
  # The example files of a router image's tests, chalkos.tests.router-<name>.
  examplesOf = c: lib.attrNames (builtins.readDir c.chalkos.router.examples);
  # The users and groups of an image whose IDs its build allocates from 999 down, in the order of
  # their names: those without a pinned ID.
  unpinned = c: {
    users = lib.attrNames (lib.filterAttrs (_: u: u.enable && u.uid == null) c.users.users);
    groups = lib.attrNames (lib.filterAttrs (_: g: g.gid == null) c.users.groups);
  };
in
{
  # Every image carries its checker, chalkd, on its system path, where the chalkd of a node that
  # runs another image finds it as sw/bin/chalkd.
  testImageCarriesItsChecker = {
    expr = map (c: hasPackage "chalkd" c.environment.systemPackages) [
      w
      layered.plain-metal.config
      edge
    ];
    expected = [
      true
      true
      true
    ];
  };
  # mkImages builds each image of its list by its name, each the image directory with its name,
  # platform, layer, OCI layout, SBOM, NixOS system and tests, and the installer.
  testMkImagesNames = {
    expr =
      let
        r = mkImages {
          images = [
            {
              name = "a";
              platform = "metal";
            }
            {
              name = "b";
              platform = "kvm";
              modules = [ layers.kubernetes ];
            }
          ];
        };
      in
      {
        names = lib.attrNames r;
        a = {
          inherit (r.a) name platform layer;
        };
        b = r.b.layer;
        build = lib.all lib.isDerivation [
          r.a
          r.b
          r.installer
        ];
        installer = lib.hasInfix "chalkos-installer" r.installer.name && !(r.installer ? platform);
        oci = lib.isDerivation r.a.oci && lib.hasSuffix "-oci" r.a.oci.name;
        nixos = r.b.nixos.config.chalkos.image.name;
        tests = r.a.tests.drvPath == r.a.nixos.config.system.build.chalkosTests.drvPath;
        sbom = lib.isDerivation r.a.sbom && lib.hasSuffix "-a.spdx.json.drv" r.a.sbom.drvPath;
      };
    expected = {
      names = [
        "a"
        "b"
        "installer"
      ];
      a = {
        name = "a";
        platform = "metal";
        layer = null;
      };
      b = "kubernetes";
      build = true;
      installer = true;
      oci = true;
      nixos = "b";
      tests = true;
      sbom = true;
    };
  };
  # mkImages refuses two images of one name and an image named installer.
  testMkImagesRefusesDuplicateNames = {
    expr = {
      twice = fails (mkImages {
        images = [
          {
            name = "a";
            platform = "metal";
          }
          {
            name = "a";
            platform = "kvm";
          }
        ];
      });
      installer = fails (mkImages {
        images = [
          {
            name = "installer";
            platform = "metal";
          }
        ];
      });
    };
    expected = {
      twice = true;
      installer = true;
    };
  };
  # An image's name and platform are required names of lower-case letters, digits and dashes.
  testMkImageRefusesBadNames = {
    expr = {
      name =
        fails
          (mkImage {
            name = "A_b";
            platform = "metal";
          }).drvPath;
      platform =
        fails
          (mkImage {
            name = "a";
            platform = "x.y";
          }).drvPath;
      noName = fails (mkImage { platform = "metal"; }).drvPath;
      noPlatform = fails (mkImage { name = "a"; }).drvPath;
      entry = fails (mkImages {
        images = [
          {
            name = "Bad";
            platform = "metal";
          }
        ];
      });
    };
    expected = {
      name = true;
      platform = true;
      noName = true;
      noPlatform = true;
      entry = true;
    };
  };
  # selectImages keeps the images matching every field given, without the installer, and refuses
  # a field an image does not have; ociOf, nixosOf and testsOf map the images alone.
  testSelectImages = {
    expr =
      let
        r = mkImages {
          images = [
            {
              name = "a";
              platform = "metal";
            }
            {
              name = "b";
              platform = "kvm";
              modules = [ layers.kubernetes ];
            }
          ];
        };
      in
      {
        metal = lib.attrNames (selectImages { platform = "metal"; } r);
        kubernetes = lib.attrNames (selectImages { layer = "kubernetes"; } r);
        both = lib.attrNames (
          selectImages {
            platform = "metal";
            layer = "kubernetes";
          } r
        );
        unknown = fails (selectImages { plattform = "metal"; } r);
        oci = lib.attrNames (ociOf r);
        nixos = lib.attrNames (nixosOf r);
        tests = lib.attrNames (testsOf r);
      };
    expected = {
      metal = [ "a" ];
      kubernetes = [ "b" ];
      both = [ ];
      unknown = true;
      oci = [
        "a"
        "b"
      ];
      nixos = [
        "a"
        "b"
      ];
      tests = [
        "a"
        "b"
      ];
    };
  };
  # An image without a layer module runs no Kubernetes.
  testKubernetesOffByDefault = {
    expr =
      (mkImage {
        name = "a";
        platform = "metal";
      }).nixos.config.chalkos.kubernetes.enable;
    expected = false;
  };
  # The image records its name.
  testImageNameRecorded = {
    expr =
      (mkImage {
        name = "node-metal";
        platform = "metal";
      }).nixos.config.chalkos.image.name;
    expected = "node-metal";
  };
  # Each image names itself and its platform, carries the layers its modules list and the OS CA;
  # the installer has no name, belongs to no platform and carries no layer.
  testMkImagesDescribesEachImage = {
    expr =
      let
        pub = builtins.toFile "secrets.pub.json" ''{"version": 3, "osCA": {"certificate": "PEM"}}'';
        i = mkImages {
          osCA = pub;
          images = [
            {
              name = "node-metal";
              platform = "metal";
              modules = [ layers.kubernetes ];
            }
            {
              name = "storage-metal";
              platform = "metal";
            }
            {
              name = "edge-metal";
              platform = "metal";
              modules = [ layers.router ];
            }
          ];
        };
        summary = n: {
          name = n.config.chalkos.image.name;
          platform = n.config.chalkos.platform.name;
          kubernetes = n.config.chalkos.kubernetes.enable;
          router = n.config.chalkos.router.enable;
          osCA = n.config.environment.etc ? "chalkos/os-ca.crt";
        };
      in
      lib.mapAttrs (_: i: summary i.nixos) i;
    expected = {
      installer = {
        name = null;
        platform = null;
        kubernetes = false;
        router = false;
        osCA = true;
      };
      edge-metal = {
        name = "edge-metal";
        platform = "metal";
        kubernetes = false;
        router = true;
        osCA = true;
      };
      node-metal = {
        name = "node-metal";
        platform = "metal";
        kubernetes = true;
        router = false;
        osCA = true;
      };
      storage-metal = {
        name = "storage-metal";
        platform = "metal";
        kubernetes = false;
        router = false;
        osCA = true;
      };
    };
  };
  # Each layer of an image writes an empty marker below /etc/chalkos/layers, which chalkd reads; an
  # image of the base alone and the installer have none.
  testLayerMarkers = {
    expr =
      let
        markers =
          c:
          lib.mapAttrs (_: e: e.text) (
            lib.filterAttrs (name: _: lib.hasPrefix "chalkos/layers/" name) c.environment.etc
          );
      in
      {
        node = markers layered.node-metal.config;
        plain = markers layered.plain-metal.config;
        edge = markers edge;
        installer = markers (installer [ ]);
      };
    expected = {
      node."chalkos/layers/kubernetes" = "";
      plain = { };
      edge."chalkos/layers/router" = "";
      installer = { };
    };
  };
  # An image carries one layer at most: one that lists both the Kubernetes and the router layer
  # fails to build, naming the image.
  testLayersExcludeEachOther = {
    expr =
      let
        refused = refuses "the image both-metal enables the kubernetes and router layers";
        both =
          (mkImage {
            name = "both-metal";
            platform = "metal";
            modules = [
              layers.kubernetes
              layers.router
            ];
          }).nixos.config;
      in
      {
        both = refused both;
        node = refused layered.node-metal.config;
        plain = refused layered.plain-metal.config;
        edge = refused edge;
      };
    expected = {
      both = true;
      node = false;
      plain = false;
      edge = false;
    };
  };
  # Check the prerequisites serialized for live apply, including images without optional effects.
  testBuiltinEffectPrerequisites =
    let
      plain = etcJSON "effects.json" layered.plain-metal.config;
      edgeEffects = etcJSON "effects.json" edge;
      edgeWithoutNat64 = etcJSON "effects.json" without.nat64;
      kube = etcJSON "effects.json" w;
      network = [
        "networkd"
        "links"
      ];
      routerNetwork = network ++ [ "router-nftables" ];
      # The kubelet also starts after the cluster registry it may pull from and containerd's
      # hosts.toml that lists it.
      nodePreparation = network ++ [
        "controlplane-ports"
        "cluster-registry"
        "registry-mirrors"
      ];
      after = names: effects: lib.genAttrs names (name: effects.${name}.after or [ ]);
    in
    {
      expr = {
        plain = after [ "networkd" "links" ] plain;
        router = after [
          "networkd"
          "dhcp"
          "dhcp-relay"
          "frr"
          "jool"
          "resolvers"
          "unbound"
          "unbound-start"
          "chrony-server"
        ] edgeEffects;
        resolverWithoutNat64 = edgeWithoutNat64.resolvers.after;
        kubernetes = after [
          "bgp"
          "kubernetes"
          "chalkd-cluster"
          "chalkd-share"
          "registry-credentials"
        ] kube;
      };
      expected = {
        plain = {
          networkd = [ ];
          links = [ "networkd" ];
        };
        router = {
          networkd = [ "vrf" ];
          dhcp = routerNetwork;
          dhcp-relay = routerNetwork;
          frr = routerNetwork;
          jool = [
            "networkd"
            "router-nftables"
          ];
          resolvers = [
            "vrf"
            "networkd"
            "router-nftables"
            "jool"
          ];
          unbound = [ "router-nftables" ];
          unbound-start = [ "router-nftables" ];
          chrony-server = [ "router-nftables" ];
        };
        resolverWithoutNat64 = [
          "vrf"
          "networkd"
          "router-nftables"
        ];
        kubernetes = {
          bgp = network;
          kubernetes = nodePreparation;
          chalkd-cluster = [ "kubernetes" ];
          chalkd-share = [ "kubernetes" ];
          registry-credentials = [ ];
        };
      };
    };
  # A router feature that an image disables with chalkos.router.<feature>.enable = false leaves
  # nothing of itself in the image: per feature, exactly the parts listed go and nothing comes in
  # their place, the build allocates the same IDs and networkd keeps the routes others install.
  # Images without the router layer take no router kind.
  testRouterFeaturesOptional =
    let
      names = lib.attrNames;
      parts =
        c:
        let
          networkd = c.systemd.services.systemd-networkd;
        in
        {
          kinds = names c.chalkos.kinds;
          outputs = names c.chalkos.outputs;
          effects = names c.chalkos.effects;
          services = names c.systemd.services;
          paths = names c.systemd.paths;
          users = names c.users.users;
          groups = names c.users.groups;
          diagnostics = names c.chalkos.diagnostics;
          chalkd = map lib.getName c.systemd.services.chalkd.path;
          packages = map lib.getName c.environment.systemPackages;
          modulePackages = map lib.getName c.boot.extraModulePackages;
          kernelModules = c.boot.kernelModules;
          etc = names c.environment.etc;
          # The kinds each output reads, as <output>:<kind>.
          reads = lib.concatMap (o: map (k: "${o}:${k}") c.chalkos.outputs.${o}.inputs) (
            names c.chalkos.outputs
          );
          # networkd's grant and the credentials it loads.
          networkd =
            networkd.chalkos.kinds
            ++ networkd.serviceConfig.SetCredential or [ ]
            ++ lib.optional (
              networkd.serviceConfig ? RefreshOnReload
            ) "RefreshOnReload=${networkd.serviceConfig.RefreshOnReload}";
          examples = examplesOf c;
        };
      all = parts edge;
      # What the image without feature f lacks, leaving out the reads of the outputs it lacks.
      removed =
        f:
        let
          p = parts without.${f};
        in
        lib.filterAttrs (_: v: v != [ ]) (
          lib.mapAttrs (k: v: lib.subtractLists p.${k} v) all
          // {
            reads = lib.filter (r: lib.elem (lib.head (lib.splitString ":" r)) p.outputs) (
              lib.subtractLists p.reads all.reads
            );
          }
        );
      added =
        f:
        let
          p = parts without.${f};
        in
        lib.concatLists (lib.mapAttrsToList (k: v: lib.subtractLists all.${k} v) p);
      routerKinds = c: names (lib.filterAttrs (_: k: k.group == "router.chalkos.dev") c.chalkos.kinds);
      # Whether chrony's config of an image holds the confdir and the commands of ntp-clients.
      chronyServerLines =
        c:
        map (d: lib.hasInfix d c.services.chrony.extraConfig) [
          "confdir "
          "opencommands "
        ];
    in
    {
      expr = {
        removed = lib.genAttrs features removed;
        added = lib.concatMap added features;
        ids = lib.all (f: unpinned without.${f} == unpinned edge) features;
        foreignRoutes = lib.all (
          f: without.${f}.systemd.network.config.networkConfig == edge.systemd.network.config.networkConfig
        ) features;
        otherImages = map routerKinds [
          layered.node-metal.config
          layered.plain-metal.config
        ];
        # Only an image with the time server gives chrony its confdir and the commands of
        # ntp-clients.
        chronyServer = map chronyServerLines [
          without.ntp
          edge
        ];
      };
      expected = {
        removed = {
          bgp = {
            kinds = [
              "BGP"
              "BGPPeer"
              "CommunityList"
              "PrefixList"
              "RoutingPolicy"
            ];
            outputs = [ "frr" ];
            effects = [ "frr" ];
            services = [
              "frr"
              "frr-bfdd"
              "frr-bgpd"
              "frr-mgmtd"
              "frr-staticd"
              "frr-zebra"
            ];
            users = [ "frr" ];
            groups = [
              "frr"
              "frrvty"
            ];
            diagnostics = [
              "bfd-peers"
              "bgp-neighbor"
              "bgp-route"
              "bgp-summary"
            ];
            chalkd = [ "frr" ];
            packages = [ "frr" ];
            etc = [
              "frr/frr.conf"
              "frr/vtysh.conf"
            ];
            reads = [ "router-nftables:BGPPeer" ];
            examples = [
              "edge.yaml"
              "leaf.yaml"
              "vrf.yaml"
            ];
          };
          nat64 = {
            kinds = [ "NAT64" ];
            outputs = [ "jool" ];
            effects = [ "jool" ];
            services = [ "chalkos-nat64" ];
            diagnostics = [ "nat64-sessions" ];
            chalkd = [ "jool-cli" ];
            modulePackages = [ "jool" ];
            kernelModules = [ "jool" ];
            reads = [
              "dhcp-relay:NAT64"
              "kea:NAT64"
              "networkd:NAT64"
              "router-nftables:NAT64"
            ];
            examples = [
              "edge.yaml"
              "nat64.yaml"
              "vrf.yaml"
            ];
          };
          resolver = {
            kinds = [ "Resolver" ];
            outputs = [ "unbound" ];
            effects = [
              "resolvers"
              "unbound"
              "unbound-start"
            ];
            services = [
              "chalkos-resolvers"
              "unbound"
              "unbound@"
            ];
            users = [ "unbound" ];
            groups = [ "unbound" ];
            chalkd = [ "unbound" ];
            packages = [ "unbound" ];
            etc = [ "unbound/unbound.conf" ];
            reads = [ "router-nftables:Resolver" ];
            examples = [
              "edge.yaml"
              "lan.yaml"
              "vrf.yaml"
            ];
          };
          dhcp = {
            kinds = [ "DHCPSubnet" ];
            outputs = [ "kea" ];
            effects = [ "dhcp" ];
            services = [
              "chalkos-dhcp"
              "chalkos-dhcp6-routes"
              "kea-dhcp4"
              "kea-dhcp4@"
              "kea-dhcp6"
              "kea-dhcp6@"
            ];
            paths = [ "chalkos-dhcp6-routes" ];
            users = [ "kea" ];
            groups = [ "kea" ];
            diagnostics = [ "dhcp-leases" ];
            reads = [
              "dhcp-relay:DHCPSubnet"
              "router-nftables:DHCPSubnet"
            ];
            examples = [
              "dhcp.yaml"
              "dhcp6.yaml"
            ];
          };
          dhcpRelay = {
            kinds = [ "DHCPRelay" ];
            outputs = [ "dhcp-relay" ];
            effects = [ "dhcp-relay" ];
            services = [
              "chalkos-dhcp-relays"
              "dnsmasq-relay"
              "dnsmasq-relay@"
            ];
            users = [ "dnsmasq" ];
            groups = [ "dnsmasq" ];
            reads = [ "router-nftables:DHCPRelay" ];
            examples = [ "dhcp-relay.yaml" ];
          };
          wireguard = {
            kinds = [
              "WireGuard"
              "WireGuardPeer"
            ];
            diagnostics = [ "wireguard" ];
            reads = [
              "frr:WireGuard"
              "networkd:WireGuard"
              "networkd:WireGuardPeer"
              "router-nftables:WireGuard"
            ];
            networkd = [
              "WireGuard"
              "WireGuardPeer"
              "secret:none"
              "RefreshOnReload=credentials"
            ];
            examples = [ "edge.yaml" ];
          };
          ntp = {
            kinds = [ "TimeServer" ];
            outputs = [ "chrony-server" ];
            effects = [ "chrony-server" ];
            diagnostics = [ "ntp-clients" ];
            reads = [ "router-nftables:TimeServer" ];
            examples = [ "time-server.yaml" ];
          };
        };
        added = [ ];
        ids = true;
        foreignRoutes = true;
        otherImages = [
          [ ]
          [ ]
        ];
        chronyServer = [
          [
            false
            false
          ]
          [
            true
            true
          ]
        ];
      };
    };
  # Router images run dnsmasq-relay for the default VRF and dnsmasq-relay@<vrf> bound to each VRF's
  # device, as the user dnsmasq with a relay's capabilities; chalkos-dhcp-relays starts, restarts
  # and stops them without capabilities.
  testRouterDHCPRelay = {
    expr =
      let
        relay = edge.systemd.services.dnsmasq-relay;
        vrf = edge.systemd.services."dnsmasq-relay@";
        relays = edge.systemd.services.chalkos-dhcp-relays;
      in
      {
        check = lib.hasInfix "/bin/chalkos-render check each *.conf /nix/store/" edge.chalkos.outputs.dhcp-relay.check;
        relay = {
          inherit (relay.serviceConfig)
            User
            AmbientCapabilities
            RestrictAddressFamilies
            ;
          inherit (relay.unitConfig) ConditionPathExists;
          wanted = relay.wantedBy;
        };
        vrfRelay = {
          inherit (vrf.serviceConfig) BindNetworkInterface;
          inherit (vrf.unitConfig) Requires;
          records =
            builtins.match "/nix/store/[^ ]*/bin/chalkd router record-device dnsmasq-relay %I" vrf.serviceConfig.ExecStartPre
            != null;
        };
        reconciler = {
          inherit (relays) wantedBy;
          inherit (relays.serviceConfig) Type CapabilityBoundingSet;
          runs = lib.hasSuffix "/bin/chalkd router services relay" relays.serviceConfig.ExecStart;
          reloads = relays.serviceConfig.ExecReload == relays.serviceConfig.ExecStart;
          stops = lib.hasSuffix " stop" relays.serviceConfig.ExecStop;
        };
      };
    expected = {
      check = true;
      relay = {
        User = "dnsmasq";
        AmbientCapabilities = [
          "CAP_NET_BIND_SERVICE"
          "CAP_NET_RAW"
          "CAP_NET_ADMIN"
        ];
        RestrictAddressFamilies = [
          "AF_UNIX"
          "AF_NETLINK"
          "AF_INET"
          "AF_INET6"
        ];
        ConditionPathExists = "/run/chalkos/outputs/dhcp-relay/dnsmasq-relay.conf";
        wanted = [ ];
      };
      vrfRelay = {
        BindNetworkInterface = "%I";
        Requires = "sys-subsystem-net-devices-%i.device";
        records = true;
      };
      reconciler = {
        wantedBy = [ "multi-user.target" ];
        Type = "oneshot";
        CapabilityBoundingSet = [ "" ];
        runs = true;
        reloads = true;
        stops = true;
      };
    };
  };
  # A router image with every feature runs a test of every router example; one without a feature
  # runs those whose documents it accepts.
  testRouterExamples = {
    expr = examplesOf edge;
    expected = [
      "dhcp-relay.yaml"
      "dhcp.yaml"
      "dhcp6.yaml"
      "edge.yaml"
      "lan.yaml"
      "leaf.yaml"
      "nat64.yaml"
      "none.yaml"
      "policy.yaml"
      "time-server.yaml"
      "vrf.yaml"
    ];
  };
  # On a router image with every feature, the test of each router example compares all nine
  # router outputs with golden files, and router-vrf-strict the ruleset; on one without a feature,
  # no router test compares any.
  testRouterGoldens =
    let

      # The tests of the router's examples of an image.
      exampleTests = c: map (file: "router-${lib.removeSuffix ".yaml" file}") (examplesOf c);
      # The outputs a test of an image compares.
      outputsOf = c: name: lib.attrNames c.chalkos.tests.${name}.outputs;
      # The outputs each test of the router's examples of an image compares, by test.
      compared = c: lib.genAttrs (exampleTests c) (outputsOf c);
      routerOutputs = [
        "chrony-server"
        "dhcp-relay"
        "frr"
        "jool"
        "kea"
        "networkd"
        "router-nftables"
        "router-sysctl"
        "unbound"
      ];
    in
    {
      expr = {
        compared = compared edge;
        vrfStrict = lib.attrNames edge.chalkos.tests.router-vrf-strict.outputs;
        goldensWithoutDHCP = lib.attrNames (
          lib.filterAttrs (
            name: t: lib.hasPrefix "router-" name && t.outputs != { }
          ) without.dhcp.chalkos.tests
        );
      };
      expected = {
        compared = lib.genAttrs (exampleTests edge) (_: routerOutputs);
        vrfStrict = [ "router-nftables" ];
        goldensWithoutDHCP = [ ];
      };
    };
  # The users and groups of the router's features have pinned IDs, free in NixOS's ids.nix, so an
  # image that adds or drops a feature moves no other user's or group's ID; the build allocates
  # IDs to the router image's other users and groups alone.
  testRouterUserIDs = {
    expr =
      let
        c = edge;
      in
      {
        users = lib.genAttrs [ "dnsmasq" "frr" "kea" "unbound" ] (n: c.users.users.${n}.uid);
        groups = lib.genAttrs [ "dnsmasq" "frr" "frrvty" "kea" "unbound" ] (n: c.users.groups.${n}.gid);
        unpinned = unpinned c;
      };
    expected = {
      users = {
        dnsmasq = 332;
        frr = 329;
        kea = 328;
        unbound = 331;
      };
      groups = {
        dnsmasq = 332;
        frr = 329;
        frrvty = 330;
        kea = 328;
        unbound = 331;
      };
      unpinned = {
        users = [
          "chalkos-render"
          "nscd"
          "systemd-oom"
        ];
        groups = [
          "chalkos-render"
          "nscd"
          "systemd-coredump"
          "systemd-oom"
        ];
      };
    };
  };
  # A router image without BGP, NAT64, the resolver, DHCP, the DHCP relay, WireGuard and NTP keeps
  # the router's core: the router kinds of the firewall, VRFs and policy routing, the outputs and
  # units of the firewall, the kernel settings and the VRFs, the console and the copy of chalkd's
  # lines to the serial port every metal image runs, and the diagnostics of every image and
  # traceroute. Its tests run the examples of none of the seven, and its build allocates the IDs
  # of the same users and groups as an image with every feature.
  testRouterWithoutFeatures = {
    expr =
      let
        c = routerWithout [
          "bgp"
          "nat64"
          "resolver"
          "dhcp"
          "dhcpRelay"
          "wireguard"
          "ntp"
        ];
      in
      {
        kinds = lib.attrNames (lib.filterAttrs (_: k: k.group == "router.chalkos.dev") c.chalkos.kinds);
        outputs = lib.attrNames c.chalkos.outputs;
        effects = lib.attrNames c.chalkos.effects;
        units = lib.filter (lib.hasPrefix "chalkos-") (lib.attrNames c.systemd.services);
        diagnostics = lib.attrNames c.chalkos.diagnostics;
        examples = examplesOf c;
        unpinned = unpinned c;
        same = unpinned c == unpinned edge;
      };
    expected = {
      kinds = [
        "AddressSet"
        "FirewallPolicy"
        "FirewallZone"
        "NATRule"
        "NextHop"
        "PolicyRoute"
        "Router"
        "VRF"
      ];
      outputs = [
        "chrony"
        "networkd"
        "router-nftables"
        "router-sysctl"
      ];
      effects = [
        "chrony"
        "chrony-dhcp"
        "links"
        "networkd"
        "router-nftables"
        "router-sysctl"
        "vrf"
      ];
      units = [
        "chalkos-chrony-dhcp"
        "chalkos-chrony-dhcp-links"
        "chalkos-config"
        "chalkos-console"
        "chalkos-firewall"
        "chalkos-health"
        "chalkos-links"
        "chalkos-serial-log@"
        "chalkos-vrf"
      ];
      diagnostics = [
        "addresses"
        "dns-query"
        "ip-rules"
        "links"
        "neighbours"
        "nft-ruleset"
        "ping"
        "route-get"
        "routes"
        "tcp-connect"
        "traceroute"
      ];
      examples = [
        "none.yaml"
        "policy.yaml"
      ];
      unpinned = {
        users = [
          "chalkos-render"
          "nscd"
          "systemd-oom"
        ];
        groups = [
          "chalkos-render"
          "nscd"
          "systemd-coredump"
          "systemd-oom"
        ];
      };
      same = true;
    };
  };
  # On a router image the router layer owns the firewall: NixOS's firewall and nftables service are
  # off, chalkos-firewall loads the table after chalkos-config renders it and before the network
  # comes up, systemd-sysctl waits for the rendered file, and the image loads its rulesets' kernel
  # modules. Other images keep NixOS's firewall.
  testRouterFirewall = {
    expr =
      let
        of = c: {
          firewall = c.networking.firewall.enable;
          nftables = c.networking.nftables.enable;
          unit = c.systemd.services ? chalkos-firewall;
          outputs = lib.filter (lib.hasPrefix "router-") (lib.attrNames c.chalkos.outputs);
        };
        unit = edge.systemd.services.chalkos-firewall;
      in
      {
        edge = of edge;
        plain = of layered.plain-metal.config;
        node = of layered.node-metal.config;
        after = lib.elem "chalkos-config.service" unit.after;
        before = lib.elem "network-pre.target" unit.before;
        sysctlAfter = edge.systemd.services.systemd-sysctl.after;
        # The modules the rulesets need, which the kernel-modules-check finds in the tree.
        missingModules = lib.subtractLists edge.boot.kernelModules [
          "nf_conntrack"
          "nf_tables"
          "nft_chain_nat"
          "nft_ct"
          "nft_fib_inet"
          "nft_fib_ipv4"
          "nft_fib_ipv6"
          "nft_log"
          "nft_masq"
          "nft_nat"
          "nft_reject_inet"
        ];
      };
    expected = {
      edge = {
        firewall = false;
        nftables = false;
        unit = true;
        outputs = [
          "router-nftables"
          "router-sysctl"
        ];
      };
      plain = {
        firewall = true;
        nftables = true;
        unit = false;
        outputs = [ ];
      };
      node = {
        firewall = true;
        nftables = true;
        unit = false;
        outputs = [ ];
      };
      after = true;
      before = true;
      sysctlAfter = [ "chalkos-config.service" ];
      missingModules = [ ];
    };
  };
  # A router image ignores NixOS's firewall, so it warns of each of its options a module set
  # besides chalkd's port, which every image opens.
  testRouterWarnsOfNixosFirewall = {
    expr =
      let
        warned = modules: lib.filter (lib.hasInfix "networking.firewall") (router modules).warnings;
      in
      {
        edge = warned [ ];
        set = warned [
          {
            networking.firewall.allowedTCPPorts = [ 22 ];
            networking.firewall.interfaces.eth0.allowedUDPPorts = [ 53 ];
            networking.firewall.extraInputRules = "tcp dport 80 accept";
          }
        ];
        plain = lib.filter (lib.hasInfix "networking.firewall") layered.plain-metal.config.warnings;
      };
    expected = {
      edge = [ ];
      set = [
        "the router layer filters with the table its router documents render and ignores networking.firewall.allowedTCPPorts, networking.firewall.extraInputRules, networking.firewall.interfaces.eth0; accept a port with a FirewallPolicy to local"
      ];
      plain = [ ];
    };
  };
  # Router images run FRR's mgmtd, zebra, staticd, bfdd and bgpd as units of their own, without
  # watchfrr or FRR's scripts. frr.service starts after the firewall and before the node's
  # addresses settle, fills the BGPPeers' passwords in from its credentials before the daemons
  # start and on reload, which chalkos-frr-reload applies, and upholds the daemons. Each daemon
  # starts after frr.service and stops or restarts with it, loads its config with vtysh once it
  # runs and restarts when it fails; zebra's clients stop with zebra. networkd keeps zebra's
  # routes. They run chalkos's FRR. Other images run no FRR.
  testRouterBGP = {
    expr =
      let
        services = edge.systemd.services;
        inherit (services) frr;
        daemons = lib.filterAttrs (n: _: lib.hasPrefix "frr-" n) services;
        frrPackage = "${layered.edge-metal.pkgs.chalkos.frr}";
      in
      {
        daemons = lib.attrNames daemons;
        inherit (frr) upholds wants;
        type = frr.serviceConfig.Type;
        check = lib.hasInfix "/bin/chalkos-render check run /nix/store/" edge.chalkos.outputs.frr.check;
        grant = frr.chalkos.kinds;
        after = lib.subtractLists frr.after [
          "chalkos-config.service"
          "chalkos-firewall.service"
          "systemd-networkd.service"
        ];
        before = lib.elem "chalkos-node-addresses.target" frr.before;
        wantedBy = lib.sort lib.lessThan frr.wantedBy;
        filled = edge.environment.etc."frr/frr.conf".source;
        condition = frr.unitConfig.ConditionPathIsDirectory;
        reload = lib.length frr.serviceConfig.ExecReload;
        # FRR starts on every router with config, without a marker of BGP.
        marker = lib.any (s: s.unitConfig ? ConditionPathExists) ([ frr ] ++ lib.attrValues daemons);
        fillsAlike = frr.serviceConfig.ExecStart == lib.head frr.serviceConfig.ExecReload;
        fills = lib.hasInfix "/bin/chalkd frr-credentials " frr.serviceConfig.ExecStart;
        reloadsFilled =
          let
            reload = lib.last frr.serviceConfig.ExecReload;
          in
          lib.hasInfix "/bin/chalkos-frr-reload " reload && lib.hasSuffix " /run/frr/frr.conf" reload;
        # Each daemon: its unit's ties to frr.service and zebra, and how it starts and loads its
        # config, from chalkos's FRR, which has no Python, SNMP, Lua or shell in its closure.
        units = lib.mapAttrs (
          n: s:
          let
            name = lib.removePrefix "frr-" n;
          in
          {
            inherit (s) bindsTo partOf requires;
            before = lib.elem "chalkos-node-addresses.target" s.before;
            condition = s.unitConfig.ConditionPathIsDirectory;
            inherit (s.serviceConfig) Type PIDFile Restart;
            start =
              s.serviceConfig.ExecStart == "${frrPackage}/libexec/frr/${name} -d ${
                if name == "zebra" then "-A 127.0.0.1 -s 90000000" else "-A 127.0.0.1"
              }";
            load = s.serviceConfig.ExecStartPost == "${frrPackage}/bin/vtysh -b -d ${name}";
          }
        ) daemons;
        refresh = frr.serviceConfig.RefreshOnReload;
        networkd = edge.systemd.network.config.networkConfig;
        groups = edge.chalkos.diagnostics.bgp-summary.groups;
        plain = layered.plain-metal.config.systemd.services ? frr;
        node = layered.node-metal.config.chalkos.outputs ? frr;
      };
    expected =
      let
        daemons = [
          "frr-bfdd"
          "frr-bgpd"
          "frr-mgmtd"
          "frr-staticd"
          "frr-zebra"
        ];
        unit = name: bindsTo: {
          inherit bindsTo;
          partOf = [ "frr.service" ];
          requires = [ "frr.service" ];
          before = true;
          condition = "/run/chalkos/credentials/frr";
          Type = "forking";
          PIDFile = "/run/frr/${name}.pid";
          Restart = "on-failure";
          start = true;
          load = true;
        };
      in
      {
        inherit daemons;
        upholds = map (d: "${d}.service") daemons;
        wants = map (d: "${d}.service") daemons;
        type = "oneshot";
        check = true;
        grant = [ "BGPPeer" ];
        after = [ ];
        before = true;
        wantedBy = [
          "chalkos-node-addresses.target"
          "multi-user.target"
        ];
        filled = "/run/frr/frr.conf";
        condition = "/run/chalkos/credentials/frr";
        reload = 2;
        marker = false;
        fillsAlike = true;
        fills = true;
        reloadsFilled = true;
        units = {
          frr-mgmtd = unit "mgmtd" [ ];
          frr-zebra = unit "zebra" [ ];
          frr-bfdd = unit "bfdd" [ "frr-zebra.service" ];
          frr-bgpd = unit "bgpd" [ "frr-zebra.service" ];
          frr-staticd = unit "staticd" [ "frr-zebra.service" ];
        };
        refresh = "credentials";
        networkd = {
          ManageForeignNextHops = false;
          ManageForeignRoutes = false;
        };
        groups = [ "frrvty" ];
        plain = false;
        node = false;
      };
  };
  # Router images accept VRF documents; other images do not. chalkos-vrf runs before networkd at
  # boot with CAP_NET_ADMIN alone, and FRR orders after networkd, which creates a VRF's device
  # before FRR's reload configures its instance.
  testRouterVRF = {
    expr =
      let
        vrf = edge.systemd.services.chalkos-vrf;
      in
      {
        kind = edge.chalkos.kinds ? VRF;
        plainKind = layered.plain-metal.config.chalkos.kinds ? VRF;
        inherit (vrf) after before wantedBy;
        defaults = vrf.unitConfig.DefaultDependencies;
        reloads = vrf.serviceConfig.ExecReload == vrf.serviceConfig.ExecStart;
        capabilities = vrf.serviceConfig.CapabilityBoundingSet;
        frrAfter = lib.elem "systemd-networkd.service" edge.systemd.services.frr.after;
      };
    expected = {
      kind = true;
      plainKind = false;
      after = [ "chalkos-config.service" ];
      before = [ "systemd-networkd.service" ];
      defaults = false;
      wantedBy = [ "multi-user.target" ];
      reloads = true;
      capabilities = [ "CAP_NET_ADMIN" ];
      frrAfter = true;
    };
  };
  # Router images run Kea without its database backends, with the lease commands' and run_script
  # hooks alone, as the user kea: kea-dhcp4 and kea-dhcp6 for the default VRF and
  # kea-dhcp4@<vrf> and kea-dhcp6@<vrf> bound to each VRF's device, restarted when they stop.
  # chalkos-dhcp6-routes routes the prefixes chalkos-dhcp6-pd records, and chalkos-dhcp starts,
  # reloads and stops the servers without capabilities, in the group kea.
  testRouterDHCP = {
    expr =
      let
        server = edge.systemd.services.kea-dhcp4;
        vrf = edge.systemd.services."kea-dhcp4@";
        server6 = edge.systemd.services.kea-dhcp6;
        vrf6 = edge.systemd.services."kea-dhcp6@";
        routes = edge.systemd.services.chalkos-dhcp6-routes;
        dhcp = edge.systemd.services.chalkos-dhcp;
        kea = lib.head (lib.splitString "/bin/" server.serviceConfig.ExecStart);
      in
      {
        kind = edge.chalkos.kinds ? DHCPSubnet;
        plainKind = layered.plain-metal.config.chalkos.kinds ? DHCPSubnet;
        check = lib.hasInfix "/bin/chalkos-render check kea /nix/store/" edge.chalkos.outputs.kea.check;
        package = lib.hasSuffix "-kea-dhcp-${pkgs.kea.version}" kea;
        hooks = builtins.attrNames (builtins.readDir "${kea}/lib/kea/hooks");
        user = edge.users.users.kea.isSystemUser;
        server = {
          start = lib.hasSuffix "/bin/kea-dhcp4 -c /run/chalkos/outputs/kea/kea-dhcp4.json" server.serviceConfig.ExecStart;
          inherit (server.serviceConfig)
            User
            AmbientCapabilities
            CapabilityBoundingSet
            RestrictAddressFamilies
            RuntimeDirectory
            RuntimeDirectoryMode
            RuntimeDirectoryPreserve
            StateDirectory
            Restart
            ;
          reload = lib.hasSuffix "kill -HUP $MAINPID" server.serviceConfig.ExecReload;
          inherit (server.unitConfig) ConditionPathExists;
          wanted = server.wantedBy;
        };
        vrfServer = {
          start = lib.hasSuffix "/bin/kea-dhcp4 -c /run/chalkos/outputs/kea/kea-dhcp4-%I.json" vrf.serviceConfig.ExecStart;
          inherit (vrf.serviceConfig) BindNetworkInterface User;
          inherit (vrf.unitConfig) Requires ConditionPathExists;
          bound = vrf.unitConfig ? BindsTo;
          records =
            builtins.match "/nix/store/[^ ]*/bin/chalkd router record-device kea-dhcp4 %I" vrf.serviceConfig.ExecStartPre
            != null;
        };
        server6 = {
          start = lib.hasSuffix "/bin/kea-dhcp6 -c /run/chalkos/outputs/kea/kea-dhcp6.json" server6.serviceConfig.ExecStart;
          inherit (server6.serviceConfig)
            User
            AmbientCapabilities
            RestrictAddressFamilies
            ReadWritePaths
            ;
          hookPath =
            builtins.match "KEA_HOOK_SCRIPTS_PATH=/nix/store/[^/]*-chalkos-dhcp6-pd/bin" (
              lib.last server6.serviceConfig.Environment
            ) != null;
          inherit (server6.unitConfig) ConditionPathExists;
          vrf = {
            inherit (vrf6.serviceConfig) BindNetworkInterface;
            records =
              builtins.match "/nix/store/[^ ]*/bin/chalkd router record-device kea-dhcp6 %I" vrf6.serviceConfig.ExecStartPre
              != null;
            inherit (vrf6.unitConfig) ConditionPathExists;
          };
        };
        routes = {
          inherit (routes) after wantedBy;
          inherit (routes.unitConfig) StartLimitIntervalSec;
          inherit (routes.serviceConfig) Type RestrictAddressFamilies;
          ip = hasPackage "iproute2-ip" routes.path;
          path = {
            inherit (edge.systemd.paths.chalkos-dhcp6-routes) wantedBy;
            inherit (edge.systemd.paths.chalkos-dhcp6-routes.pathConfig) PathChanged TriggerLimitIntervalSec;
          };
          timer = {
            inherit (edge.systemd.timers.chalkos-dhcp6-routes) wantedBy;
            inherit (edge.systemd.timers.chalkos-dhcp6-routes.timerConfig) OnBootSec OnUnitActiveSec;
          };
          records = lib.all (r: lib.elem r edge.systemd.tmpfiles.rules) [
            "d /var/lib/chalkos-dhcp6-pd 0755 root root - -"
            "d /var/lib/chalkos-dhcp6-pd/records 0755 kea kea - -"
            "d /var/lib/chalkos-dhcp6-pd/records.new 0700 kea kea - -"
          ];
        };
        reconciler = {
          after = lib.all (x: lib.elem x dhcp.after) [
            "chalkos-vrf.service"
            "systemd-networkd.service"
          ];
          inherit (dhcp) wantedBy;
          inherit (dhcp.serviceConfig) Type CapabilityBoundingSet SupplementaryGroups;
          runs = lib.hasSuffix "/bin/chalkd router services kea" dhcp.serviceConfig.ExecStart;
          reloads = dhcp.serviceConfig.ExecReload == dhcp.serviceConfig.ExecStart;
          stops = lib.hasSuffix " stop" dhcp.serviceConfig.ExecStop;
        };
        diagnosticGroups = edge.chalkos.diagnostics.dhcp-leases.groups;
        plain = layered.plain-metal.config.chalkos.outputs ? kea;
      };
    expected = {
      kind = true;
      plainKind = false;
      check = true;
      package = true;
      hooks = [
        "libdhcp_lease_cmds.so"
        "libdhcp_run_script.so"
      ];
      user = true;
      server = {
        start = true;
        User = "kea";
        AmbientCapabilities = [
          "CAP_NET_BIND_SERVICE"
          "CAP_NET_RAW"
        ];
        CapabilityBoundingSet = [
          "CAP_NET_BIND_SERVICE"
          "CAP_NET_RAW"
        ];
        RestrictAddressFamilies = [
          "AF_UNIX"
          "AF_NETLINK"
          "AF_INET"
          "AF_INET6"
          "AF_PACKET"
        ];
        RuntimeDirectory = "kea";
        RuntimeDirectoryMode = "0750";
        RuntimeDirectoryPreserve = true;
        StateDirectory = "kea";
        Restart = "on-failure";
        reload = true;
        ConditionPathExists = "/run/chalkos/outputs/kea/kea-dhcp4.json";
        wanted = [ ];
      };
      vrfServer = {
        start = true;
        BindNetworkInterface = "%I";
        User = "kea";
        Requires = "sys-subsystem-net-devices-%i.device";
        ConditionPathExists = "/run/chalkos/outputs/kea/kea-dhcp4-%I.json";
        bound = false;
        records = true;
      };
      server6 = {
        start = true;
        User = "kea";
        AmbientCapabilities = [ "CAP_NET_BIND_SERVICE" ];
        RestrictAddressFamilies = [
          "AF_UNIX"
          "AF_NETLINK"
          "AF_INET"
          "AF_INET6"
        ];
        # One parent of the records and their staging directory: two entries would be two mounts,
        # across which the hook's rename fails with EXDEV.
        ReadWritePaths = [ "-/var/lib/chalkos-dhcp6-pd" ];
        hookPath = true;
        ConditionPathExists = "/run/chalkos/outputs/kea/kea-dhcp6.json";
        vrf = {
          BindNetworkInterface = "%I";
          records = true;
          ConditionPathExists = "/run/chalkos/outputs/kea/kea-dhcp6-%I.json";
        };
      };
      routes = {
        StartLimitIntervalSec = 0;
        after = [ "chalkos-dhcp.service" ];
        wantedBy = [ "multi-user.target" ];
        Type = "oneshot";
        RestrictAddressFamilies = [
          "AF_UNIX"
          "AF_NETLINK"
        ];
        ip = true;
        path = {
          wantedBy = [ "multi-user.target" ];
          PathChanged = "/var/lib/chalkos-dhcp6-pd/records";
          TriggerLimitIntervalSec = 0;
        };
        timer = {
          wantedBy = [ "timers.target" ];
          OnBootSec = "1min";
          OnUnitActiveSec = "1min";
        };
        records = true;
      };
      reconciler = {
        after = true;
        wantedBy = [ "multi-user.target" ];
        Type = "oneshot";
        CapabilityBoundingSet = [ "" ];
        SupplementaryGroups = [ "kea" ];
        runs = true;
        reloads = true;
        stops = true;
      };
      diagnosticGroups = [ "kea" ];
      plain = false;
    };
  };
  # Router images run Jool (chalkos-nat64) and Unbound per VRF (reconciled by chalkos-resolvers):
  # their outputs' checks, ordering and hardening. chalkos-nat64 has no mount namespace of its own,
  # so the host sees the instances' namespaces. Other images run neither.
  testRouterNAT64AndResolver = {
    expr =
      let
        nat64 = edge.systemd.services.chalkos-nat64;
        unbound = edge.systemd.services.unbound;
      in
      {
        checks = {
          jool = lib.hasInfix "/bin/jool file check $FILE" edge.chalkos.outputs.jool.check;
          unbound = lib.hasInfix "/bin/unbound-checkconf $FILE" edge.chalkos.outputs.unbound.check;
        };
        nat64After = lib.subtractLists nat64.after [
          "chalkos-config.service"
          "chalkos-firewall.service"
          "systemd-networkd.service"
        ];
        nat64Before = nat64.before;
        nat64Capabilities = nat64.serviceConfig.CapabilityBoundingSet;
        nat64Namespaces = {
          inherit (nat64.serviceConfig) RestrictNamespaces;
          mountNamespace = lib.any (o: nat64.serviceConfig ? ${o}) [
            "ProtectSystem"
            "PrivateTmp"
            "PrivateDevices"
            "ProtectHome"
            "ProtectKernelLogs"
            "ProtectKernelModules"
            "ProtectKernelTunables"
            "ProtectControlGroups"
            "PrivateMounts"
            "ReadOnlyPaths"
            "ReadWritePaths"
            "InaccessiblePaths"
            "ProtectProc"
            "ProcSubset"
            "BindPaths"
            "BindReadOnlyPaths"
            "TemporaryFileSystem"
            "RootDirectory"
            "MountAPIVFS"
            "PrivateUsers"
          ];
        };
        nat64Reloads = nat64.serviceConfig.ExecReload == nat64.serviceConfig.ExecStart;
        nat64Stops = lib.hasSuffix " stop" nat64.serviceConfig.ExecStop;
        unboundAfter = lib.elem "chalkos-nat64.service" unbound.after;
        unboundCapabilities = {
          inherit (unbound.serviceConfig) AmbientCapabilities CapabilityBoundingSet RuntimeDirectoryMode;
        };
        unboundCondition = unbound.unitConfig.ConditionPathExists;
        unboundConfig = edge.environment.etc."unbound/unbound.conf".source;
        unboundReload = lib.hasSuffix "/bin/unbound-control reload_keep_cache" unbound.serviceConfig.ExecReload;
        localQueries = edge.services.unbound.resolveLocalQueries;
        chalkd = lib.subtractLists (map lib.getName edge.systemd.services.chalkd.path) [
          "jool-cli"
          "unbound"
          "util-linux"
        ];
        diagnosticNamespace = edge.chalkos.diagnostics.nat64-sessions.networkNamespace;
        vrfResolver =
          let
            u = edge.systemd.services."unbound@";
          in
          {
            inherit (u.serviceConfig)
              BindNetworkInterface
              RuntimeDirectory
              RuntimeDirectoryMode
              AmbientCapabilities
              CapabilityBoundingSet
              User
              Type
              ProtectSystem
              ;
            start = lib.hasSuffix "/bin/unbound -p -d -c /run/chalkos/outputs/unbound/unbound-%I.conf" u.serviceConfig.ExecStart;
            reload = lib.hasSuffix "/bin/unbound-control -c /run/chalkos/outputs/unbound/unbound-%I.conf reload_keep_cache" u.serviceConfig.ExecReload;
            inherit (u.unitConfig) Requires ConditionPathExists;
            bound = u.unitConfig ? BindsTo;
            records =
              builtins.match "/nix/store/[^ ]*/bin/chalkd router record-device unbound %I" u.serviceConfig.ExecStartPre
              != null;
            after = lib.all (x: lib.elem x u.after) [
              "sys-subsystem-net-devices-%i.device"
              "chalkos-firewall.service"
              "chalkos-nat64.service"
            ];
          };
        resolvers =
          let
            r = edge.systemd.services.chalkos-resolvers;
          in
          {
            after = lib.all (x: lib.elem x r.after) [
              "chalkos-vrf.service"
              "systemd-networkd.service"
              "chalkos-nat64.service"
            ];
            inherit (r.serviceConfig)
              Type
              RemainAfterExit
              CapabilityBoundingSet
              SupplementaryGroups
              PrivateDevices
              ProtectControlGroups
              ;
            runs = lib.hasSuffix "/bin/chalkd router services unbound" r.serviceConfig.ExecStart;
            reloads = r.serviceConfig.ExecReload == r.serviceConfig.ExecStart;
            stops = lib.hasSuffix " stop" r.serviceConfig.ExecStop;
          };
        unboundCheck = lib.hasInfix "/bin/chalkos-render check each *.conf /nix/store/" edge.chalkos.outputs.unbound.check;
        plain = {
          unbound = layered.plain-metal.config.services.unbound.enable;
          jool = layered.plain-metal.config.chalkos.outputs ? jool;
        };
      };
    expected = {
      checks = {
        jool = true;
        unbound = true;
      };
      nat64After = [ ];
      nat64Before = [ "frr.service" ];
      nat64Capabilities = [
        "CAP_NET_ADMIN"
        "CAP_SYS_ADMIN"
      ];
      nat64Namespaces = {
        RestrictNamespaces = "net";
        mountNamespace = false;
      };
      nat64Reloads = true;
      nat64Stops = true;
      unboundAfter = true;
      unboundCapabilities = {
        AmbientCapabilities = [ "CAP_NET_BIND_SERVICE" ];
        CapabilityBoundingSet = [ "CAP_NET_BIND_SERVICE" ];
        RuntimeDirectoryMode = "0750";
      };
      unboundCondition = "/run/chalkos/outputs/unbound/unbound.conf";
      unboundConfig = "/run/chalkos/outputs/unbound/unbound.conf";
      unboundReload = true;
      localQueries = false;
      chalkd = [ ];
      diagnosticNamespace = "nat64-{instance}";
      vrfResolver = {
        BindNetworkInterface = "%I";
        RuntimeDirectory = "unbound-%I";
        RuntimeDirectoryMode = "0750";
        AmbientCapabilities = [ "CAP_NET_BIND_SERVICE" ];
        CapabilityBoundingSet = [ "CAP_NET_BIND_SERVICE" ];
        User = "unbound";
        Type = "notify";
        ProtectSystem = "strict";
        start = true;
        reload = true;
        Requires = "sys-subsystem-net-devices-%i.device";
        bound = false;
        records = true;
        ConditionPathExists = "/run/chalkos/outputs/unbound/unbound-%I.conf";
        after = true;
      };
      resolvers = {
        after = true;
        Type = "oneshot";
        RemainAfterExit = true;
        CapabilityBoundingSet = [ "" ];
        SupplementaryGroups = [ "unbound" ];
        PrivateDevices = true;
        ProtectControlGroups = true;
        runs = true;
        reloads = true;
        stops = true;
      };
      unboundCheck = true;
      plain = {
        unbound = false;
        jool = false;
      };
    };
  };
  # Forwarding belongs to the base: both layers turn it on, an image of the base alone forwards
  # only when its modules ask for it.
  testForwarding = {
    expr =
      let
        forwarding =
          c:
          lib.filterAttrs (
            k: _:
            lib.elem k [
              "net.ipv4.ip_forward"
              "net.ipv6.conf.all.forwarding"
            ]
          ) c.boot.kernel.sysctl;
        plainForwarding =
          (mkImage {
            name = "plain-metal";
            platform = "metal";
            modules = [ { chalkos.forwarding.enable = true; } ];
          }).nixos.config;
        on = {
          "net.ipv4.ip_forward" = 1;
          "net.ipv6.conf.all.forwarding" = 1;
        };
      in
      {
        node = forwarding layered.node-metal.config == on;
        edge = forwarding edge == on;
        plain = forwarding layered.plain-metal.config;
        asked = forwarding plainForwarding == on;
      };
    expected = {
      node = true;
      edge = true;
      plain = { };
      asked = true;
    };
  };
  # An image carries its platform's modules, those every image carries and its own; the installer
  # carries its own modules alone. In a list both define, the image's own entries come
  # first.
  testMkImagesAppliesModules = {
    expr =
      let
        mark = m: { boot.kernelParams = [ m ]; };
        i = mkImages {
          modules = [ (mark "every") ];
          images = [
            {
              name = "node-metal";
              platform = "metal";
              modules = [ (mark "image") ];
            }
          ];
          installer.modules = [ (mark "installer") ];
        };
        marks =
          n:
          lib.filter (
            p:
            lib.elem p [
              "every"
              "image"
              "installer"
            ]
          ) n.config.boot.kernelParams;
      in
      {
        node = marks i.node-metal.nixos;
        installer = marks i.installer.nixos;
        # chalkos's own metal module is among them: it sets the consoles.
        console = lib.any (
          d: lib.hasSuffix "modules/platforms/metal.nix" d.file
        ) i.node-metal.nixos.options.chalkos.consoles.definitionsWithLocations;
      };
    expected = {
      node = [
        "image"
        "every"
      ];
      installer = [ "installer" ];
      console = true;
    };
  };
  # mkImages and mkImage refuse arguments they do not have, as an image of mkImages's list does;
  # a platform other than metal and kvm is one of the image's modules alone.
  testMkImagesRefuses = {
    expr = {
      unknown = fails (mkImages {
        variants.node = { };
      });
      unknownEntry = fails (mkImages {
        images = [
          {
            name = "a";
            platform = "metal";
            layer = "router";
          }
        ];
      });
      unknownImage =
        fails
          (mkImage {
            name = "a";
            platform = "metal";
            platforms = [ "kvm" ];
          }).drvPath;
      custom =
        (mkImage {
          name = "node-esxi-8";
          platform = "esxi-8";
        }).nixos.config.chalkos.platform.name;
    };
    expected = {
      unknown = true;
      unknownEntry = true;
      unknownImage = true;
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
            (mkImage {
              name = "node-metal";
              platform = "metal";
              osCA = builtins.toFile "secrets.pub.json" ''{"version": ${toString version}, "osCA": {"certificate": "PEM"}}'';
            }).nixos.config.environment.etc."chalkos/os-ca.crt".source.drvPath
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
  # mkImage evaluates the image with the nixpkgs and the module arguments given, adds chalkosLib
  # to those arguments and leaves nixpkgs's lib as it is.
  testMkImageUsesItsNixpkgs = {
    expr =
      let
        n =
          (mkImage {
            name = "node-metal";
            platform = "metal";
            nixpkgs.lib = lib // {
              nixosSystem = args: {
                marker = "custom";
                config.system.build.chalkosImage = { };
                inherit (args) specialArgs;
                passesLib = args ? lib;
              };
            };
            specialArgs.answer = 42;
          }).nixos;
      in
      {
        inherit (n) marker passesLib;
        inherit (n.specialArgs) answer;
        chalkosLib = n.specialArgs.chalkosLib ? mkOption;
      };
    expected = {
      marker = "custom";
      passesLib = false;
      answer = 42;
      chalkosLib = true;
    };
  };
  # An image's modules get chalkosLib, without the functions that build images, and nixpkgs's lib,
  # without a chalkos attribute.
  testImageModulesGetChalkosLib = {
    expr =
      builtins.fromJSON
        (worker [
          (
            { chalkosLib, lib, ... }:
            {
              environment.etc."chalkos-test/lib.json".text = builtins.toJSON {
                libChalkos = lib ? chalkos;
                mkOption = chalkosLib ? mkOption;
                mkImage = chalkosLib ? mkImage;
              };
            }
          )
        ]).environment.etc."chalkos-test/lib.json".text;
    expected = {
      libChalkos = false;
      mkOption = true;
      mkImage = false;
    };
  };
  # mkImage refuses a module argument of the user's named chalkosLib.
  testMkImageRefusesChalkosLibArgument = {
    expr =
      fails
        (mkImage {
          name = "node-metal";
          platform = "metal";
          specialArgs.chalkosLib = flakeLib;
        }).nixos.config.chalkos.image.name;
    expected = true;
  };
  # The flake's lib holds chalkosLib's helpers besides the functions that build images, usable
  # outside an image.
  testFlakeLibOutsideAnImage = {
    expr = {
      images = lib.all (n: flakeLib ? ${n}) [
        "mkImage"
        "mkImages"
        "mkInstaller"
        "selectImages"
        "ociOf"
        "nixosOf"
        "testsOf"
      ];
      rules =
        (flakeLib.mkOption {
          type = lib.types.str;
          rules = [
            {
              rule = "self == oldSelf";
              message = "immutable";
            }
          ];
        })."x-chalkos-rules";
      template = flakeLib.render.template ../../modules/testing/greeting.tmpl;
    };
    expected = {
      images = true;
      rules = [
        {
          rule = "self == oldSelf";
          message = "immutable";
        }
      ];
      template.template = ../../modules/testing/greeting.tmpl;
    };
  };
  testImageRunsChalkd = {
    expr =
      let
        services = w.systemd.services;
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
        i = workerImages;
        conf = config: lib.splitString "\n" config.environment.etc."systemd/resolved.conf".text;
      in
      map
        (lines: {
          llmnr = lib.elem "LLMNR=false" lines;
          mdns = lib.elem "MulticastDNS=false" lines;
        })
        [
          (conf i.worker-metal.nixos.config)
          (conf i.installer.nixos.config)
        ];
    expected = lib.replicate 2 {
      llmnr = true;
      mdns = true;
    };
  };
  # The OS CA is an argument of mkImage, which every image and the installer carry as a file; the
  # images have no setting of it besides chalkos.maintenance.osCA.
  testImagesCarryOSCA = {
    expr =
      let
        pub = builtins.toFile "secrets.pub.json" ''{"version": 3, "osCA": {"certificate": "PEM"}}'';
        withCA = mkImage {
          name = "worker-metal";
          platform = "metal";
          osCA = pub;
        };
      in
      {
        withoutCA = w.environment.etc ? "chalkos/os-ca.crt";
        setting = withCA.nixos.config.chalkos ? osCA;
      };
    expected = {
      withoutCA = false;
      setting = false;
    };
  };
  # chalkd pulls images over HTTPS with Go's crypto/x509, which reads the system trust store at
  # /etc/ssl/certs/ca-certificates.crt, on the nodes for upgrades and on the installer for installs.
  testImageHasTheCABundle = {
    expr =
      let
        i = workerImages;
        has = config: config.environment.etc ? "ssl/certs/ca-certificates.crt";
      in
      {
        node = has i.worker-metal.nixos.config;
        installer = has i.installer.nixos.config;
      };
    expected = {
      node = true;
      installer = true;
    };
  };
  testChrony = {
    expr =
      let
        summary = c: {
          chrony = c.services.chrony.enable;
          timesyncd = c.services.timesyncd.enable;
          servers = c.services.chrony.servers;
          sourcedirs = lib.filter (lib.hasPrefix "sourcedir") (
            lib.splitString "\n" c.services.chrony.extraConfig
          );
          dhcpPath = c.systemd.paths ? chalkos-chrony-dhcp;
          # The servers DHCP announced at boot are read once networkd is up, not only on a change.
          dhcpAtBoot =
            c.systemd.services ? chalkos-chrony-dhcp
            && lib.elem "multi-user.target" c.systemd.services.chalkos-chrony-dhcp.wantedBy
            && lib.elem "systemd-networkd.service" c.systemd.services.chalkos-chrony-dhcp.after;
          dhcpDir = lib.elem "d /run/chalkos/chrony-dhcp 0755 root root - -" c.systemd.tmpfiles.rules;
          dhcpGrant = (etcJSON "services.json" c).chalkos-chrony-dhcp.kinds;
          dhcpReadsConfig = lib.hasSuffix "/bin/chalkd chrony-dhcp /run/chalkos/chrony-dhcp/dhcp.sources" c.systemd.services.chalkos-chrony-dhcp.serviceConfig.ExecStart;
          # Active after it ran, so chalkd's try-restart runs it again; a link's change restarts it.
          dhcpRerunsOnChange =
            c.systemd.services.chalkos-chrony-dhcp.serviceConfig.RemainAfterExit
            && c.systemd.paths.chalkos-chrony-dhcp.pathConfig.Unit == "chalkos-chrony-dhcp-links.service"
            && lib.hasSuffix "systemctl restart chalkos-chrony-dhcp.service" c.systemd.services.chalkos-chrony-dhcp-links.serviceConfig.ExecStart;
          # A burst of link changes, as a CNI's interfaces bring at boot, starts the units more
          # often than systemd's default limits allow; none of the three may fail for it.
          dhcpUnlimited =
            c.systemd.paths.chalkos-chrony-dhcp.pathConfig.TriggerLimitIntervalSec == 0
            && c.systemd.services.chalkos-chrony-dhcp-links.startLimitIntervalSec == 0
            && c.systemd.services.chalkos-chrony-dhcp.startLimitIntervalSec == 0;
          chalkdHasChronyc = lib.elem c.services.chrony.package c.systemd.services.chalkd.path;
          # chrony's effect, on its output, runs chalkd from chalkd's PATH, which leaves a chronyd
          # that does not run.
          reloadSources =
            (etcJSON "effects.json" c).chrony == {
              outputs = [ "chrony" ];
              kinds = [ ];
              run = [
                "chalkd"
                "chrony-reload-sources"
              ];
              reload = [ ];
              restart = [ ];
              start = false;
              when = "";
              chalkd = "";
              hint = null;
            }
            && hasPackage "chalkd" c.systemd.services.chalkd.path;
          # chronyd checks the output chrony, whose examples are tests of the image.
          output = {
            check = lib.hasInfix "/bin/chalkos-render check chrony /nix/store/" c.chalkos.outputs.chrony.check;
            tests = lib.filter (lib.hasPrefix "chrony-") (lib.attrNames c.chalkos.tests);
          };
          chalkdProtectsClock = c.systemd.services.chalkd.serviceConfig.ProtectClock;
        };
      in
      # The units are always there; TimeSync.spec.dhcp decides at runtime.
      summary w;
    expected = {
      chrony = true;
      timesyncd = false;
      servers = [ ];
      sourcedirs = [
        "sourcedir /run/chalkos/outputs/chrony"
        "sourcedir /run/chalkos/chrony-dhcp"
      ];
      dhcpPath = true;
      dhcpAtBoot = true;
      dhcpDir = true;
      dhcpGrant = [ "TimeSync" ];
      dhcpReadsConfig = true;
      dhcpRerunsOnChange = true;
      dhcpUnlimited = true;
      chalkdHasChronyc = true;
      reloadSources = true;
      output = {
        check = true;
        tests = [
          "chrony-empty"
          "chrony-servers"
        ];
      };
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
        image = hook w;
        testImage = hook (worker [ ../../modules/testing/test-image.nix ]);
      };
    expected = {
      image = false;
      testImage = true;
    };
  };
  # The installer's modules reach its image.
  testInstallerModules = {
    expr =
      let
        i = mkInstaller {
          modules = [
            {
              systemd.network.networks."10-uplink" = {
                matchConfig.MACAddress = "52:54:00:12:34:56";
                address = [ "10.0.0.5/24" ];
              };
              chalkos.kernel.moduleGroups = [ "wireless" ];
            }
          ];
        };
        inherit (i.nixos) config;
      in
      {
        uplink = config.systemd.network.networks."10-uplink".matchConfig.MACAddress;
        wireless = lib.elem "wireless" config.chalkos.kernel.moduleGroups;
        image = i.drvPath == config.system.build.chalkosInstaller.drvPath;
      };
    expected = {
      uplink = "52:54:00:12:34:56";
      wireless = true;
      image = true;
    };
  };
  # Booted from a CD-ROM, the installer's partitions come from the one loop device systemd's udev
  # rules attach to it, systemd-loop@.service of systemd 261 and later: a loop device of the
  # installer's own rules would be a second one, and the ESP would not mount when dm-verity took
  # the store from one and its hash tree from the other.
  testInstallerAttachesNoLoopDevice = {
    expr =
      let
        config = installer [ ];
      in
      {
        initrdRules = lib.hasInfix "losetup" config.boot.initrd.services.udev.rules;
        rules = lib.hasInfix "losetup" config.services.udev.extraRules;
        systemdLoop = lib.versionAtLeast config.boot.initrd.systemd.package.version "261";
      };
    expected = {
      initrdRules = false;
      rules = false;
      systemdLoop = true;
    };
  };
  # Only the kvm image runs the guest agent, and only under KVM.
  testPlatformImages = {
    expr =
      let
        i = workerImages;
        summary = c: {
          guestAgent = c.services.qemuGuest.enable;
          condition = c.systemd.services.qemu-guest-agent.unitConfig.ConditionVirtualization or null;
        };
      in
      {
        metal = summary i.worker-metal.nixos.config;
        kvm = summary i.worker-kvm.nixos.config;
      };
    expected = {
      metal = {
        guestAgent = false;
        condition = null;
      };
      kvm = {
        guestAgent = true;
        condition = "kvm";
      };
    };
  };
  # The kernel's consoles: metal, a platform of the arguments' own and the installer put the
  # screen last, kvm has the first serial port alone, and an image or the installer may choose
  # another order. Two plain lists conflict, and lib.mkForce overrides one. A console= of
  # boot.kernelParams's own is refused, as are an empty list and an entry that is no device and
  # options; an entry may have several options, as uart8250's takes them.
  testConsoles = {
    expr =
      let
        i = workerImages;
        esxi =
          (mkImage {
            name = "worker-esxi";
            platform = "esxi";
            modules = [ layers.kubernetes ];
          }).nixos.config;
        serialLast = [
          "tty0"
          "ttyS0,115200"
        ];
        consoles = c: lib.filter (lib.hasPrefix "console=") c.boot.kernelParams;
        refused = refuses "boot.kernelParams holds console= entries of its own";
        # The worker of a platform r650 whose own module sets the consoles plainly, with the
        # image's module given.
        onR650 =
          module:
          (mkImage {
            name = "worker-r650";
            platform = "r650";
            modules = [
              layers.kubernetes
              {
                chalkos.consoles = [
                  "ttyS1,115200"
                  "tty0"
                ];
              }
              module
            ];
          }).nixos.config;
      in
      {
        metal = consoles i.worker-metal.nixos.config;
        kvm = consoles i.worker-kvm.nixos.config;
        esxi = consoles esxi;
        installer = consoles i.installer.nixos.config;
        serialLast = consoles (worker [ { chalkos.consoles = serialLast; } ]);
        installerSerialLast = consoles (installer [ { chalkos.consoles = serialLast; } ]);
        stray = refused (worker [ { boot.kernelParams = [ "console=ttyS1,115200" ]; } ]);
        plain = refused w;
        empty = fails (worker [ { chalkos.consoles = [ ]; } ]).chalkos.consoles;
        notADevice = fails (worker [ { chalkos.consoles = [ "ttyS0 quiet" ]; } ]).chalkos.consoles;
        # Two modules' plain lists conflict; lib.mkForce overrides the other's.
        twice = fails (onR650 { chalkos.consoles = serialLast; }).chalkos.consoles;
        forced = consoles (onR650 {
          chalkos.consoles = lib.mkForce serialLast;
        });
        uart = consoles (worker [
          {
            chalkos.consoles = [
              "uart8250,mmio32,0xfe215040"
              "tty0"
            ];
          }
        ]);
      };
    expected = {
      metal = [
        "console=ttyS0,115200"
        "console=tty0"
      ];
      kvm = [ "console=ttyS0,115200" ];
      esxi = [
        "console=ttyS0,115200"
        "console=tty0"
      ];
      installer = [
        "console=ttyS0,115200"
        "console=tty0"
      ];
      serialLast = [
        "console=tty0"
        "console=ttyS0,115200"
      ];
      installerSerialLast = [
        "console=tty0"
        "console=ttyS0,115200"
      ];
      stray = true;
      plain = false;
      empty = true;
      notADevice = true;
      twice = true;
      forced = [
        "console=tty0"
        "console=ttyS0,115200"
      ];
      uart = [
        "console=uart8250,mmio32,0xfe215040"
        "console=tty0"
      ];
    };
  };
  # /dev/console is the last kernel console. While that is tty0 or tty1 and the console runs on
  # tty1, chalkd's unit leaves /dev/console out, so its lines do not run over the console, which
  # shows the fingerprint and the addresses. Otherwise /dev/console gets them.
  testChalkdOutput = {
    expr =
      let
        i = workerImages;
        output =
          c:
          lib.unique [
            c.systemd.services.chalkd.serviceConfig.StandardOutput
            c.systemd.services.chalkd.serviceConfig.StandardError
          ];
        on = consoles: output (worker [ { chalkos.consoles = consoles; } ]);
      in
      {
        metal = output i.worker-metal.nixos.config;
        kvm = output i.worker-kvm.nixos.config;
        installer = output i.installer.nixos.config;
        serialLast = on [
          "tty0"
          "ttyS0,115200"
        ];
        screenAlone = on [ "tty1" ];
        otherTerminal = on [
          "ttyS0,115200"
          "tty2"
        ];
        noConsole = output (worker [ { chalkos.console.enable = false; } ]);
      };
    expected = {
      metal = [ "journal" ];
      kvm = [ "journal+console" ];
      installer = [ "journal" ];
      serialLast = [ "journal+console" ];
      screenAlone = [ "journal" ];
      otherTerminal = [ "journal+console" ];
      noConsole = [ "journal+console" ];
    };
  };
  # While the last kernel console is a virtual terminal, chalkos-serial-log@ copies chalkd's lines
  # from the journal to each serial port of the list, whether the console runs or not: ttyS*,
  # ttyAMA* and hvc* among them; a uart8250 entry names no device file. A serial port last gets
  # them through /dev/console, and a port without a UART starts no copy.
  testSerialLog = {
    expr =
      let
        i = workerImages;
        copies = c: lib.filter (lib.hasPrefix "chalkos-serial-log@") c.systemd.targets.multi-user.wants;
        on = consoles: copies (worker [ { chalkos.consoles = consoles; } ]);
        unit = i.worker-metal.nixos.config.systemd.services."chalkos-serial-log@";
      in
      {
        metal = copies i.worker-metal.nixos.config;
        kvm = copies i.worker-kvm.nixos.config;
        installer = copies i.installer.nixos.config;
        serialLast = on [
          "tty0"
          "ttyS0,115200"
        ];
        twoPorts = on [
          "ttyS1,115200"
          "ttyS0"
          "tty0"
        ];
        screenAlone = on [ "tty0" ];
        noConsole = copies (worker [ { chalkos.console.enable = false; } ]);
        otherPorts = on [
          "ttyAMA0,115200"
          "hvc0"
          "uart8250,mmio32,0xfe215040"
          "tty1"
        ];
        unit = {
          condition = unit.unitConfig.ConditionPathExists;
          typeZero = lib.hasSuffix "/bin/grep -qsvx 0 /sys/class/tty/%i/type" unit.serviceConfig.ExecCondition;
          follows = lib.tail (lib.splitString " " unit.serviceConfig.ExecStart);
          inherit (unit.serviceConfig)
            StandardInput
            StandardOutput
            TTYPath
            TTYVHangup
            Restart
            CapabilityBoundingSet
            ;
        };
        noTemplate =
          (worker [ { chalkos.consoles = [ "ttyS0,115200" ]; } ]).systemd.services ? "chalkos-serial-log@";
      };
    expected = {
      metal = [ "chalkos-serial-log@ttyS0.service" ];
      kvm = [ ];
      installer = [ "chalkos-serial-log@ttyS0.service" ];
      serialLast = [ ];
      twoPorts = [
        "chalkos-serial-log@ttyS1.service"
        "chalkos-serial-log@ttyS0.service"
      ];
      screenAlone = [ ];
      noConsole = [ "chalkos-serial-log@ttyS0.service" ];
      otherPorts = [
        "chalkos-serial-log@ttyAMA0.service"
        "chalkos-serial-log@hvc0.service"
      ];
      unit = {
        condition = "/sys/class/tty/%i";
        typeZero = true;
        follows = [
          "--follow"
          "--boot"
          "--lines=all"
          "--no-pager"
          "--output=cat"
          "--cursor-file=/run/chalkos-serial-log/%i.cursor"
          "_SYSTEMD_UNIT=chalkd.service"
        ];
        StandardInput = "null";
        StandardOutput = "tty";
        TTYPath = "/dev/%i";
        TTYVHangup = false;
        Restart = "always";
        CapabilityBoundingSet = "";
      };
      noTemplate = false;
    };
  };
  # The guest agent answers what the host asks about the node and shuts it down; it runs no
  # commands and touches no files. It shuts the node down with systemd's commands, which it runs
  # from /sbin, a read-only tmpfs of its own: when an image has links below /usr, /sbin leads to
  # /usr/sbin.
  testGuestAgentRestricted =
    let
      config = workerImages.worker-kvm.nixos.config;
      service = config.systemd.services.qemu-guest-agent.serviceConfig;
    in
    {
      expr = {
        rpcs = lib.splitString "," (lib.head (builtins.match ".*--allow-rpcs=([^ ]*).*" service.ExecStart));
        sbin = service.BindReadOnlyPaths or [ ];
        sbinTmpfs = service.TemporaryFileSystem or null;
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
        sbinTmpfs = "/sbin:ro";
      };
    };
  # A kvm image finds its disks at boot: the initrd loads virtio's PCI transport and its block and
  # SCSI drivers, for the image the lab template's nodes run. A metal image leaves them out, as
  # real machines have no virtio disks.
  testKvmInitrdVirtio = {
    expr =
      let
        i = workerImages;
        virtio =
          platform:
          lib.sort lib.lessThan (
            lib.unique (
              lib.filter (lib.hasPrefix "virtio")
                i."worker-${platform}".nixos.config.boot.initrd.availableKernelModules
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
  # A platform other than metal and kvm is one of the image's modules alone. An image overrides a
  # value its platform's modules set by priority, as module order does not: lib.mkForce for a value
  # the platform defines plainly, lib.mkOverride below 50 for the guest agent's ExecStart, which
  # kvm forces. Both add to a list.
  testCustomPlatform = {
    expr =
      let
        esxi =
          (mkImage {
            name = "worker-esxi";
            platform = "esxi";
            modules = [
              layers.kubernetes
              { services.openssh.enable = true; }
            ];
          }).nixos.config;
        kvm =
          (mkImage {
            name = "worker-kvm";
            platform = "kvm";
            modules = [
              layers.kubernetes
              {
                boot.kernelParams = [ "image" ];
                systemd.services.qemu-guest-agent.serviceConfig.ExecStart = lib.mkOverride 49 "image-agent";
              }
            ];
          }).nixos.config;
      in
      {
        esxi = esxi.services.openssh.enable;
        esxiPlatform = esxi.system.nixos.extraOSReleaseArgs.CHALKOS_PLATFORM;
        kvm = kvm.services.openssh.enable;
        kernelParams = lib.sort lib.lessThan (
          lib.filter (p: p == "image" || p == "console=ttyS0,115200") kvm.boot.kernelParams
        );
        agent = kvm.systemd.services.qemu-guest-agent.serviceConfig.ExecStart;
      };
    expected = {
      esxi = true;
      esxiPlatform = "esxi";
      kvm = false;
      kernelParams = [
        "console=ttyS0,115200"
        "image"
      ];
      agent = "image-agent";
    };
  };
  # The installer runs chalkd in installer mode and belongs to no platform, whatever its modules
  # say.
  testInstallerEssentials =
    let
      build = modules: (installer modules).system.build.chalkosInstaller.drvPath;
    in
    {
      expr = {
        chalkd = fails (build [ { systemd.services.chalkd.enable = false; } ]);
        mode = fails (build [
          { systemd.services.chalkd.environment.CHALKD_INSTALLER = lib.mkForce "0"; }
        ]);
        platform = fails (build [ { chalkos.platform.name = "kvm"; } ]);
      };
      expected = {
        chalkd = true;
        mode = true;
        platform = true;
      };
    };
  testStateHasItsOwnPartitionType = {
    expr = w.systemd.repart.partitions."50-state".Type;
    expected = "480b7842-1236-1abb-0339-e5503b43122a";
  };
  # containerd runs portmap, which needs nft.
  testContainerdFindsNft = {
    expr = lib.any (p: (p.pname or "") == "nftables") w.systemd.services.containerd.path;
    expected = true;
  };
  # CNI DaemonSets install their plugins in /opt/cni/bin, a tmpfs of its own whose plugins can
  # run, and write their configuration to /etc/cni/net.d, which links to containerd's conf_dir on
  # VAR.
  testCNIDaemonSetPaths = {
    expr = {
      inherit (w.fileSystems."/opt/cni/bin") fsType options;
      confDir = w.virtualisation.containerd.settings.plugins."io.containerd.cri.v1.runtime".cni.conf_dir;
      etc = w.environment.etc."cni/net.d".source;
    };
    expected = {
      fsType = "tmpfs";
      options = [
        "mode=0755"
        "nosuid"
        "nodev"
      ];
      confDir = "/var/lib/cni/net.d";
      etc = "/var/lib/cni/net.d";
    };
  };
  # The image fixes its Kubernetes release, whose kubelet it runs, and its pod network, as options
  # of its own; the Cluster document names the other images, which no option sets.
  testImageKubernetesOptions = {
    expr =
      let
        c = w;
      in
      {
        inherit (c.chalkos.kubernetes) enable;
        images =
          fails
            (worker [ { chalkos.kubernetes.images.pause = "registry.k8s.io/pause:3.10.2"; } ])
            .chalkos.kubernetes;
        flannel =
          fails
            (worker [ { chalkos.cni.flannel.image = "ghcr.io/flannel-io/flannel:v0.28.9"; } ]).chalkos.cni;
      };
    expected = {
      enable = true;
      images = true;
      flannel = true;
    };
  };
  # chalkos.kubernetes.version picks the kubelet by its minor, the newest of releases.nix by
  # default, built from the patch release and source that releases.nix pins for it, whatever
  # nixpkgs's own release; image.json names the release's version. Compared by derivation, so no
  # kubelet builds. Each minor of releases.nix names its release's minor.
  testKubeletRelease =
    let
      image =
        modules:
        let
          c = worker modules;
        in
        {
          inherit (c.chalkos.kubernetes) version;
          package = c.chalkos.kubernetes.package.drvPath;
          source = c.chalkos.kubernetes.package.src.outputHash;
          imageVersion =
            (builtins.fromJSON (builtins.readFile c.chalkos.imageDirectory.facts.kubernetes)).version;
          kubelet = lib.hasInfix "-kubelet-${c.chalkos.kubernetes.package.version}/bin/kubelet " c.systemd.services.kubelet.serviceConfig.ExecStart;
        };
      expect = minor: {
        version = minor;
        package = (kubeletOf minor).drvPath;
        source = kubernetesReleases.${minor}.hash;
        imageVersion = kubernetesReleases.${minor}.version;
        kubelet = true;
      };
    in
    {
      expr = {
        default = image [ ];
        older = image [ { chalkos.kubernetes.version = "1.36"; } ];
        minors = lib.mapAttrsToList (
          minor: release: lib.versions.majorMinor release.version == minor
        ) kubernetesReleases;
        unknown = fails (worker [ { chalkos.kubernetes.version = "1.30"; } ]).chalkos.kubernetes.version;
      };
      expected = {
        default = expect newestKubernetes;
        older = expect "1.36";
        minors = map (_: true) (lib.attrNames kubernetesReleases);
        unknown = true;
      };
    };
  # A package of its own stands for a release the table lacks; an image that sets both the
  # version and the package names one minor in both.
  testKubeletPackageAndVersionAgree =
    let
      refused = modules: refuses "and chalkos.kubernetes.version is 1.36" (worker modules);
    in
    {
      expr = {
        disagree = refused [
          {
            chalkos.kubernetes.version = "1.36";
            chalkos.kubernetes.package = kubeletOf newestKubernetes;
          }
        ];
        packageAlone =
          lib.all (a: a.assertion)
            (worker [ { chalkos.kubernetes.package = kubeletOf "1.36"; } ]).assertions;
        agree =
          lib.all (a: a.assertion)
            (worker [
              {
                chalkos.kubernetes.version = newestKubernetes;
                chalkos.kubernetes.package = kubeletOf newestKubernetes;
              }
            ]).assertions;
      };
      expected = {
        disagree = true;
        packageAlone = true;
        agree = true;
      };
    };
  # Only an image with the Kubernetes layer runs Kubernetes.
  testKubernetesOnlyWithItsLayer = {
    expr =
      let
        image = enable: if enable then w else layered.plain-metal.config;
        services = config: {
          kubelet = config.systemd.services ? kubelet;
          containerd = config.virtualisation.containerd.enable;
          prepare = config.systemd.services ? chalkos-kubernetes;
          credentials = config.systemd.services ? chalkos-registry-credentials;
        };
      in
      {
        none = services (image false);
        worker = services (image true);
        prepare = lib.hasInfix "/bin/chalkd kubernetes prepare" (image true)
        .systemd.services.chalkos-kubernetes.serviceConfig.ExecStart;
      };
    expected = {
      none = {
        kubelet = false;
        containerd = false;
        prepare = false;
        credentials = false;
      };
      worker = {
        kubelet = true;
        containerd = true;
        prepare = true;
        credentials = true;
      };
      prepare = true;
    };
  };
  testKubernetesImage = {
    expr =
      let
        config = w;
      in
      {
        # chalkd renders the add-ons and reads the Addon documents from the config; the image
        # ships no objects of its own.
        addonObjects = config.environment.etc ? "chalkos/kubernetes/manifests.json";
        # chalkos-controlplane makes etcd's directory by the Node's type, not the image.
        etcdDir = lib.elem "d /var/lib/etcd 0700 root root -" config.systemd.tmpfiles.rules;
        grant = (etcJSON "services.json" config).chalkos-controlplane.kinds;
        beforeKubernetes = lib.all (u: lib.elem u config.systemd.services.chalkos-controlplane.before) [
          "chalkos-kubernetes.service"
          "kubelet.service"
        ];
        etcdMount = config.systemd.services.chalkos-controlplane.unitConfig.RequiresMountsFor;
        # The kubelet waits for the mount too: its etcd pod would otherwise start on an empty
        # directory. It waits for its own root, an exec path, as well.
        kubeletMounts = config.systemd.services.kubelet.unitConfig.RequiresMountsFor;
        # Without the firewall the unit makes etcd's directory alone, and does not wait for it.
        afterAbsentFirewall =
          let
            unit = (worker [ { networking.firewall.enable = false; } ]).systemd.services.chalkos-controlplane;
          in
          lib.elem "nftables.service" unit.after;
      };
    expected = {
      addonObjects = false;
      etcdDir = false;
      grant = [ "Node" ];
      beforeKubernetes = true;
      etcdMount = "/var/lib/etcd";
      kubeletMounts = [
        "/var/lib/etcd"
        "/var/lib/kubelet"
      ];
      afterAbsentFirewall = false;
    };
  };
  # One directory with the plugins the image names, copied out of nixpkgs's packages: those
  # flannel's network runs, plus flannel's own plugin, or with no network every reference plugin;
  # an image adds to them or narrows them. containerd searches /opt/cni/bin, where DaemonSets
  # install theirs, before it.
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
          [
            "/opt/cni/bin"
            (dir "flannel")
          ]
          [
            "/opt/cni/bin"
            (dir "none")
          ]
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
  # modules from the same tree, and a module the image loads must be in it. The tree is the
  # image's only one: it holds the modules of boot.extraModulePackages too, such as the router's
  # jool, and every module of the kernel with allModules. The build check kernel-modules runs the
  # filter and the check.
  testKernelModules = {
    expr =
      let
        c = w;
        gpu = worker [
          {
            chalkos.kernel.moduleGroups = [ "gpu" ];
            chalkos.kernel.extraModules = [ "kvm_amd" ];
          }
        ];
        all = worker [ { chalkos.kernel.allModules = true; } ];
        tree = c: c.system.build.chalkosKernelModules;
        check = c.system.build.chalkosKernelModulesCheck;
        loaded = lib.splitString "\n" check.loaded;
      in
      {
        groups = lib.sort lib.lessThan (lib.unique c.chalkos.kernel.moduleGroups);
        gpuDirectory = map (c: lib.elem "drivers/gpu" (tree c).directories) [
          c
          gpu
        ];
        extra = lib.elem "kvm_amd" (tree gpu).names;
        # The image's module tree is made of the filtered tree alone, not of the kernel's or of
        # the out-of-tree module packages.
        filtered =
          map (c: (tree c) ? directories && map toString c.system.modulesTree.paths == [ "${tree c}" ])
            [
              c
              edge
              all
            ];
        extras = map toString (tree edge).extras == [ "${edge.boot.kernelPackages.jool}" ];
        all = (tree all).directories;
        loadsChecked = lib.all (m: lib.elem m loaded) (
          c.boot.kernelModules ++ c.boot.initrd.kernelModules ++ c.boot.initrd.availableKernelModules
        );
      };
    expected = {
      groups = [
        "filesystems"
        "firewall"
        "kubernetes"
        "kubevirt"
        "network"
        "platform"
        "storage"
        "virtualisation"
      ];
      gpuDirectory = [
        false
        true
      ];
      extra = true;
      filtered = [
        true
        true
        true
      ];
      extras = true;
      all = [ "." ];
      loadsChecked = true;
    };
  };
  # The groups of kernel modules are declared by the modules that need them: every image carries
  # the base firewall's, a router too, whose rulesets use its modules with the nftables service
  # off, and the Kubernetes layer adds its own. Every image declares the Kubernetes group, so an
  # image may add it; a group no module declares is refused.
  testKernelModuleGroupsByLayer = {
    expr =
      let
        groupsOf = c: lib.sort lib.lessThan (lib.unique c.chalkos.kernel.moduleGroups);
      in
      {
        node = groupsOf layered.node-metal.config;
        plain = groupsOf layered.plain-metal.config;
        edge = groupsOf edge;
        declared = layered.plain-metal.config.chalkos.kernel.groups ? kubernetes;
        firewall = layered.plain-metal.config.chalkos.kernel.groups.firewall.directories;
        unknownTree =
          (builtins.tryEval
            (worker [ { chalkos.kernel.moduleGroups = [ "chalkos-nothing" ]; } ])
            .system.build.chalkosKernelModules.drvPath
          ).success;
      };
    expected =
      let
        base = [
          "filesystems"
          "firewall"
          "network"
          "platform"
          "storage"
          "virtualisation"
        ];
      in
      {
        node = lib.sort lib.lessThan (
          base
          ++ [
            "kubernetes"
            "kubevirt"
          ]
        );
        plain = base;
        edge = base;
        declared = true;
        firewall = [
          "net/netfilter"
          "net/ipv4/netfilter"
          "net/ipv6/netfilter"
        ];
        unknownTree = false;
      };
  };
  # The tools nodes run, without what they never do: xfs_scrub, ctr and containerd-stress.
  testTrimmedTools = {
    expr =
      let
        c = w;
        named = name: lib.findFirst (p: lib.getName p == name) null;
        xfsprogs = named "xfsprogs" c.systemd.services.chalkd.path;
        containerd = named "containerd" c.systemd.services.containerd.path;
      in
      {
        scrub = lib.elem "--enable-scrub=no" xfsprogs.configureFlags;
        python = hasPackage "python3" xfsprogs.buildInputs;
        icu = hasPackage "icu4c" (
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
  # Mirrors come from the Mirror, Registry and ClusterRegistry documents and the node's static
  # addresses, written to /run before containerd starts.
  testRegistryMirrorsFromConfig =
    let
      config = w;
      unit = config.systemd.services.chalkos-registry-mirrors;
      services = etcJSON "services.json" config;
    in
    {
      expr = {
        baked = lib.any (lib.hasPrefix "containerd/certs.d") (lib.attrNames config.environment.etc);
        configPath =
          config.virtualisation.containerd.settings.plugins."io.containerd.cri.v1.images".registry.config_path;
        grant = services.chalkos-registry-mirrors.kinds;
        beforeContainerd =
          lib.elem "containerd.service" unit.before && lib.elem "containerd.service" unit.wantedBy;
        afterConfig = lib.elem "chalkos-config.service" unit.after;
      };
      expected = {
        baked = false;
        configPath = "/run/containerd/certs.d";
        grant = [
          "Mirror"
          "Registry"
          "ClusterRegistry"
          "Network"
        ];
        beforeContainerd = true;
        afterConfig = true;
      };
    };
  # containerd's settings of the Cluster document, the pod sandbox's image, come from a file in /run
  # that chalkos-containerd-config writes before containerd starts; the image bakes in none.
  testContainerdImportsClusterSettings =
    let
      config = w;
      unit = config.systemd.services.chalkos-containerd-config;
      services = etcJSON "services.json" config;
    in
    {
      expr = {
        inherit (config.virtualisation.containerd.settings) imports;
        pinned =
          config.virtualisation.containerd.settings.plugins."io.containerd.cri.v1.images" ? pinned_images;
        grant = services.chalkos-containerd-config.kinds;
        inherit (unit) wantedBy requiredBy;
        beforeContainerd = lib.elem "containerd.service" unit.before;
        execStart = lib.hasSuffix "kubernetes containerd-config /run/containerd/conf.d/chalkos.toml" unit.serviceConfig.ExecStart;
      };
      expected = {
        imports = [ "/run/containerd/conf.d/*.toml" ];
        pinned = false;
        grant = [ "Cluster" ];
        wantedBy = [ "containerd.service" ];
        requiredBy = [ ];
        beforeContainerd = true;
        execStart = true;
      };
    };
  # The Kubernetes layer's units run chalkd itself, with no script around it: the control plane's
  # ports with the firewall's mark and the nft that loads them, and containerd's registry hosts.
  testKubernetesUnitsRunChalkd =
    let
      units = w.systemd.services;
      chalkd = builtins.match "/nix/store/[^ /]*-chalkd-[^ /]*/bin/chalkd (.*)";
    in
    {
      expr = {
        controlplane = chalkd units.chalkos-controlplane.serviceConfig.ExecStart;
        nft = lib.elem "nftables" (map lib.getName units.chalkos-controlplane.path);
        mirrors = chalkd units.chalkos-registry-mirrors.serviceConfig.ExecStart;
      };
      expected = {
        controlplane = [ "kubernetes controlplane --firewall-mark 0x02000000" ];
        nft = true;
        mirrors = [ "kubernetes registry-mirrors" ];
      };
    };
  # The image's build-time facts, which chalkd reads with the node's config: the kubelet's release
  # and the pod network. The node's file is the image directory's facts/kubernetes.json.
  testImageFacts =
    let
      factsOf = c: builtins.fromJSON (builtins.readFile c.chalkos.imageDirectory.facts.kubernetes);
      image = factsOf w;
      noFlannel = factsOf (worker [ { chalkos.cni.provider = "none"; } ]);
    in
    {
      expr = {
        inherit image noFlannel;
        sameFile =
          w.environment.etc."chalkos/kubernetes/image.json".source
          == w.chalkos.imageDirectory.facts.kubernetes;
      };
      expected = {
        sameFile = true;
        image = {
          version = kubernetesReleases.${newestKubernetes}.version;
          cni = "flannel";
        };
        noFlannel = {
          version = kubernetesReleases.${newestKubernetes}.version;
          cni = "none";
        };
      };
    };
  # The kubelet reads its configuration from /run and its flags from KUBELET_ARGS, which chalkd
  # writes; protectKernelDefaults makes it fail unless the kernel has these values.
  testKubeletUnit = {
    expr =
      let
        config = w;
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
        noFullPackage =
          !lib.hasInfix "-kubernetes-${kubernetesReleases.${newestKubernetes}.version}/" ExecStart;
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
  # The kubelet's credential provider: a unit granted the Registry kind writes its files before the
  # kubelet, which reads its flags, and a Registry change starts both again. The kubelet only wants
  # the unit and its flags file may be missing, so a failure to write them leaves anonymous pulls.
  testRegistryCredentialsUnit =
    let
      config = w;
      unit = config.systemd.services.chalkos-registry-credentials;
      kubelet = config.systemd.services.kubelet;
      services = etcJSON "services.json" config;
      effects = etcJSON "effects.json" config;
    in
    {
      expr = {
        grant = services.chalkos-registry-credentials.kinds;
        credentials = unit.serviceConfig.LoadCredential;
        command = lib.hasSuffix "/bin/chalkd kubernetes registry-credentials" unit.serviceConfig.ExecStart;
        beforeKubelet =
          lib.elem "kubelet.service" unit.before
          && lib.elem "chalkos-registry-credentials.service" kubelet.wants
          && !(lib.elem "chalkos-registry-credentials.service" kubelet.requires)
          && lib.elem "chalkos-registry-credentials.service" kubelet.after;
        afterConfig = lib.elem "chalkos-config.service" unit.after;
        flags =
          lib.elem "-/run/chalkos/kubernetes/credential-provider/flags" kubelet.serviceConfig.EnvironmentFile
          && lib.hasInfix "$KUBELET_CREDENTIAL_PROVIDER_ARGS $KUBELET_ARGS" kubelet.serviceConfig.ExecStart;
        effect = {
          inherit (effects.registry-credentials) kinds restart;
        };
      };
      expected = {
        grant = [ "Registry" ];
        credentials = [ "secret:/run/chalkos/credentials/chalkos-registry-credentials" ];
        command = true;
        beforeKubelet = true;
        afterConfig = true;
        flags = true;
        effect = {
          kinds = [ "Registry" ];
          restart = [
            "chalkos-registry-credentials.service"
            "kubelet.service"
          ];
        };
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
            inherit (config.networking) nftables;
            prepare = config.systemd.services.chalkos-kubernetes;
          in
          {
            nftables = nftables.enable && !nftables.flushRuleset;
            # The ports the node opens, in the table of both families.
            bothFamilies =
              nftables.tables.nixos-fw.family == "inet"
              && lib.hasInfix "tcp dport { 10250, 50000, 30000-32767 } accept" nftables.tables.nixos-fw.content;
            # Emptied before the addresses are picked, filled after.
            emptied =
              let
                pre = prepare.serviceConfig.ExecStartPre or [ ];
              in
              builtins.length pre == 1
              && lib.hasSuffix "/bin/chalkd kubernetes vxlan-rule" (builtins.elemAt pre 0);
            # The preparation fills the table as its last step, and nothing fills it after a
            # failure.
            prepareFills =
              lib.hasSuffix "/bin/chalkd kubernetes prepare --vxlan-rule" prepare.serviceConfig.ExecStart
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
        bothFamilies = true;
        emptied = true;
        prepareFills = true;
        afterFirewall = true;
      };
      none = {
        nftables = true;
        bothFamilies = true;
        emptied = false;
        prepareFills = false;
        afterFirewall = false;
      };
      withoutFirewall = {
        nftables = true;
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
        chalkd = w.systemd.services.chalkd;
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
  # default, and after the units of extensions that add addresses. Every image defines the target,
  # also one without Kubernetes.
  testNodeAddressesTarget = {
    expr =
      let
        plainTarget = layered.plain-metal.config.systemd.targets.chalkos-node-addresses;
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
        plainAfter = plainTarget.after;
      };
    expected = {
      targetAfter = [ "network-online.target" ];
      targetWants = [ "network-online.target" ];
      prepareAfter = true;
      prepareWants = true;
      extension = true;
      extensionBefore = true;
      plainAfter = [ "network-online.target" ];
    };
  };
  # An image describes itself in os-release, with an empty IMAGE_VERSION. The build labels its
  # store partitions without a version; chalkosImage relabels slot A by the root hash.
  testImageDescribesItself = {
    expr =
      let
        c = worker [ { chalkos.upgrade.bootTries = 1; } ];
      in
      {
        osRelease = lib.filterAttrs (
          k: _: lib.hasPrefix "CHALKOS_" k || k == "IMAGE_VERSION"
        ) c.system.nixos.extraOSReleaseArgs;
        labels = map (p: p.repartConfig.Label) [
          c.image.repart.partitions."10-store-verity"
          c.image.repart.partitions."20-store"
        ];
      };
    expected = {
      osRelease = {
        IMAGE_VERSION = "";
        CHALKOS_IMAGE = "worker-metal";
        CHALKOS_PLATFORM = "metal";
        CHALKOS_BOOT_TRIES = "1";
      };
      labels = [
        "store-verity"
        "store"
      ];
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
  # chalkd confirms a config's trial with the boot's health check, so it ignores the units the
  # image's check ignores; an image that ignores none sets nothing.
  testHealthIgnoreUnitsReachChalkd = {
    expr =
      let
        environment = c: c.systemd.services.chalkd.environment;
        ignoring = worker [
          {
            chalkos.upgrade.healthIgnoreUnits = [
              "a.service"
              "b.service"
            ];
          }
        ];
      in
      {
        units = (environment ignoring).CHALKD_HEALTH_IGNORE_UNITS or null;
        none = (environment w) ? CHALKD_HEALTH_IGNORE_UNITS;
      };
    expected = {
      units = "a.service b.service";
      none = false;
    };
  };
  # chalkd's status reports a boot settled once the image's health timeout passed after the boot
  # started.
  testHealthTimeoutReachesChalkd = {
    expr =
      let
        timeout = c: c.systemd.services.chalkd.environment.CHALKD_HEALTH_TIMEOUT or null;
      in
      {
        default = timeout w;
        set = timeout (worker [ { chalkos.upgrade.healthTimeout = 30; } ]);
      };
    expected = {
      default = "300";
      set = "30";
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
        refused = refuses "chalkos orders boot entries by generation";
        node = w;
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
        "CHALKOS_IMAGE=\"worker-metal\""
        "CHALKOS_PLATFORM=metal"
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
        refused = refuses "system.image.id must consist of lower-case letters";
        id = id: worker [ { system.image.id = lib.mkForce id; } ];
      in
      {
        default = refused w || refused (installer [ ]);
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
  # The Kubernetes layer annotates its image's manifest with its Kubernetes release; an image
  # without the layer has no such annotation.
  testKubernetesAnnotation = {
    expr = lib.mapAttrs (_: image: image.config.chalkos.imageDirectory.annotations) {
      inherit (layered) node-metal plain-metal;
    };
    expected = {
      node-metal."dev.chalkos.kubernetes.version" = kubernetesReleases.${newestKubernetes}.version;
      plain-metal = { };
    };
  };
  # mkImages gives its revision and tag to every image and the installer, which only metadata.json
  # reads and annotates the image's manifest with: the store, and so the root hash, is the same
  # without them. Both default to null.
  testMkImagesPassesProvenance = {
    expr =
      let
        node = {
          name = "node-metal";
          platform = "metal";
        };
        given = mkImages {
          revision = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678";
          tag = "v1.5.0";
          images = [ node ];
        };
        none = mkImage node;
      in
      {
        given = lib.mapAttrs (_: i: i.nixos.config.chalkos.provenance) given;
        none = none.nixos.config.chalkos.provenance;
        sameStore =
          given.node-metal.nixos.config.system.build.toplevel.drvPath
          == none.nixos.config.system.build.toplevel.drvPath;
        mistyped = fails (mkImage (node // { revision = 1; })).nixos.config.chalkos.provenance;
        annotations = given.node-metal.nixos.config.chalkos.imageDirectory.annotations;
        noAnnotations = none.nixos.config.chalkos.imageDirectory.annotations;
      };
    expected = {
      annotations = {
        "org.opencontainers.image.revision" = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678";
        "org.opencontainers.image.version" = "v1.5.0";
      };
      noAnnotations = { };
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
      "260M"
      "266240K"
      "0.25G"
      "a lot"
    ] (size: lib.all (a: a.assertion) (worker [ { chalkos.disk.espSize = size; } ]).assertions);
    expected = {
      "260M" = true;
      "266240K" = true;
      "0.25G" = false;
      "a lot" = false;
    };
  };
  # systemd-repart makes a new LUKS2-encrypted ext4 partition no smaller than 64 MiB, so a smaller
  # STATE is refused.
  testStateMinimum = {
    expr = lib.genAttrs [
      "64M"
      "65536K"
      "63M"
      "a lot"
    ] (size: lib.all (a: a.assertion) (worker [ { chalkos.disk.stateSize = size; } ]).assertions);
    expected = {
      "64M" = true;
      "65536K" = true;
      "63M" = false;
      "a lot" = false;
    };
  };
  # The ESP is mounted at /efi when used: upgrades write UKIs there, systemd-bless-boot renames
  # them, and systemd-boot-random-seed.service refreshes the boot loader's seed.
  testESPMount = {
    expr =
      let
        c = w;
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
        "fmask=0177"
        "dmask=0077"
        "noexec"
        "nosuid"
        "nodev"
        "nofail"
        "x-systemd.automount"
        "x-systemd.idle-timeout=1min"
      ];
      seed = "/efi";
      bless = "/efi";
    };
  };
  # Building an image checks that it leaves room for the next: its store in a slot, its hash tree in
  # the slot's verity partition, and on the ESP the UKIs of both slots and an upgrade's; the
  # installer, never upgraded, holds one UKI.
  testImageFitCheck = {
    expr =
      let
        i = workerImages;
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
        image = summary i.worker-metal.fits;
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
  # The UKI carries the kernel and the initrd; the store holds the modules alone. nixos-init reads
  # the bootspec in the initrd, so it is written without them.
  testStoreLeavesOutKernelAndInitrd = {
    expr =
      let
        c = w;
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
  # Images run nixos-init in place of NixOS's bash activation and link no /bin/sh or /usr/bin/env,
  # with the debug tools too, and neither the initrd nor the system path holds a shell. The build
  # refuses a shell's store path, but not bash-completion's, unless the debug tools or
  # chalkos.shell.allow let one in. The NixOS firewall, which nixpkgs's bashless profile would
  # turn off, stays on.
  testNoShell = {
    expr =
      let
        forbidden =
          c: path: lib.any (r: builtins.match "(${r}).*" path != null) c.system.forbiddenDependenciesRegexes;
        shell = c: {
          nixosInit = c.system.nixos-init.enable;
          activatable = c.system.activatable;
          binsh = c.environment.binsh;
          usrbinenv = c.environment.usrbinenv;
          initrdShell = c.boot.initrd.systemd.shell.enable;
          shutdownShell = c.systemd.shutdownRamfs.shell.enable;
          bash = lib.elem "bash-interactive" (map lib.getName c.environment.systemPackages);
          forbidden = map (forbidden c) [
            "${builtins.storeDir}/0123456789abcdfghijklmnpqrsvwxyz-bash-5.3p15"
            "${builtins.storeDir}/0123456789abcdfghijklmnpqrsvwxyz-bash-interactive-5.3p15-man"
            "${builtins.storeDir}/0123456789abcdfghijklmnpqrsvwxyz-busybox-1.37.0"
            "${builtins.storeDir}/0123456789abcdfghijklmnpqrsvwxyz-busybox-static-1.0"
            "${builtins.storeDir}/0123456789abcdfghijklmnpqrsvwxyz-toybox-0.8.13"
            "${builtins.storeDir}/0123456789abcdfghijklmnpqrsvwxyz-bash-completion-2.16.0"
          ];
        };
        c = worker [ ];
      in
      {
        worker = shell c;
        debug = shell (worker [ { chalkos.debug.tools = true; } ]);
        allowed = (shell (worker [ { chalkos.shell.allow = true; } ])).forbidden;
        router = shell (router [ ]);
        firewall = c.networking.firewall.enable && c.networking.nftables.tables ? nixos-fw;
      };
    expected =
      let
        none = {
          nixosInit = true;
          activatable = false;
          binsh = null;
          usrbinenv = null;
          initrdShell = false;
          shutdownShell = false;
          bash = false;
          forbidden = shellsForbidden;
        };
        shellsForbidden = [
          true
          true
          true
          true
          true
          false
        ];
        noneForbidden = [
          false
          false
          false
          false
          false
          false
        ];
      in
      {
        worker = none;
        debug = none // {
          bash = true;
          forbidden = noneForbidden;
        };
        allowed = noneForbidden;
        router = none;
        firewall = true;
      };
  };
  # btrfs support takes a copy of btrfs-progs, which the no-shell check finds without fsck.btrfs,
  # in the system, the initrd and chalkd's path, while containerd stays nixpkgs's build against the
  # original.
  testBtrfsProgs = {
    expr =
      let
        c = worker [ ];
        copy = lib.findFirst (p: lib.getName p == "btrfs-progs") null c.system.fsPackages;
      in
      {
        copied = copy != null && copy.drvPath != pkgs.btrfs-progs.drvPath;
        initrd = lib.elem copy c.boot.initrd.systemd.initrdBin;
        chalkd = lib.elem copy c.systemd.services.chalkd.path;
        containerd = lib.hasInfix (builtins.unsafeDiscardStringContext pkgs.containerd.outPath) (
          builtins.unsafeDiscardStringContext
            (lib.findFirst (p: lib.getName p == "containerd") null c.systemd.services.containerd.path)
            .buildCommand
        );
      };
    expected = {
      copied = true;
      initrd = true;
      chalkd = true;
      containerd = true;
    };
  };
  # The system path holds what the image's modules put there, such as the mount helpers util-linux
  # looks up there, and none of NixOS's default packages. The debug tools add a shell's tools, and
  # crictl where the image runs Kubernetes. Units name their own tools.
  testSystemPath = {
    expr =
      let
        image =
          layer: modules:
          (mkImage {
            name = "worker-metal";
            platform = "metal";
            modules = layer ++ modules;
          }).nixos.config;
        names = c: map lib.getName c.environment.systemPackages;
        # What modules add to the system path of an image with the layer given, or none.
        added =
          layer: modules:
          lib.sort lib.lessThan (lib.subtractLists (names (image layer [ ])) (names (image layer modules)));
        kubernetes = [ layers.kubernetes ];
        debug = [ { chalkos.debug.tools = true; } ];
        c = image kubernetes [ ];
      in
      {
        imageAdds = added kubernetes [ ({ pkgs, ... }: { environment.systemPackages = [ pkgs.hello ]; }) ];
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
        nfs = lib.elem "nfs-utils" (names (image kubernetes [ { boot.supportedFilesystems.nfs = true; } ]));
        debug = added kubernetes debug;
        debugWithoutKubernetes = added [ ] debug;
        # D-Bus finds systemd's services and policies through the system path.
        dbus = lib.elem c.system.path c.services.dbus.packages && lib.elem "systemd" (names c);
        locales = c.i18n.supportedLocales;
        chalkd = lib.sort lib.lessThan (map lib.getName c.systemd.services.chalkd.path);
      };
    expected = {
      imageAdds = [ "hello" ];
      nixosDefaults = [ ];
      nfs = true;
      debug = [
        "bash-interactive"
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
        "bash-interactive"
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
        "chalkd"
        "chrony"
        "coreutils"
        "cryptsetup"
        "dosfstools"
        "e2fsprogs"
        "efibootmgr"
        "findutils"
        "frr"
        "gnugrep"
        "gnused"
        "systemd"
        "systemd"
        "util-linux"
        "xfsprogs"
      ];
    };
  };

  # A diagnostic chalkd could not run fails the image's build, with a message that names its
  # option.
  testDiagnosticsRefuseWhatChalkdCannotRun =
    let
      ip = "${pkgs.iproute2}/bin/ip";
      # The messages of the failed assertions about chalkos.diagnostics of an image with d.
      refusals =
        d:
        lib.filter (lib.hasPrefix "chalkos.diagnostics.") (
          map (a: a.message) (
            lib.filter (a: !a.assertion) (worker [ { chalkos.diagnostics = d; } ]).assertions
          )
        );
      plain = {
        description = "A test.";
        role = "reader";
        command = [ ip ];
      };
      # The refusals of the diagnostic route, or in-namespace in the namespace ns-{name}, with the
      # attributes given.
      route = attrs: refusals { route = plain // attrs; };
      inNamespace =
        attrs: refusals { in-namespace = plain // { networkNamespace = "ns-{name}"; } // attrs; };
      table = {
        type = "pattern";
        regex = "^[a-z]+$";
        description = "A table.";
      };
    in
    {
      expr = {
        accepted = route {
          command = [
            ip
            "route"
            "get"
            "{address}"
          ];
          parameters.address = {
            type = "address";
            description = "An address.";
          };
        };
        name = refusals { Route_Get = plain; };
        program = route {
          command = [
            "ip"
            "route"
          ];
        };
        placeholder = route {
          command = [
            ip
            "{address}"
          ];
        };
        parameter = route { parameters.table = table; };
        parameterName = route {
          command = [
            ip
            "{Table}"
          ];
          parameters.Table = table;
        };
        optionalDefault = route {
          command = [
            ip
            "{table}"
          ];
          parameters.table = table // {
            optional = true;
            default = "main";
          };
        };
        group = route { groups = [ "chalkos-no-such-group" ]; };
        groupForm = route { groups = [ "Wheel" ]; };
        longName = refusals { ${lib.strings.replicate 65 "a"} = plain; };
        longTimeout = route { timeout = "601s"; };
        longestTimeout = route { timeout = "10m"; };
        namespace = inNamespace { parameters.name = table; };
        namespaceOptional = inNamespace {
          parameters.name = table // {
            optional = true;
          };
        };
        namespaceParameter = inNamespace { };
        # A parameter fills a namespace's name with one character at least.
        namespaceLongest = inNamespace {
          networkNamespace = "${lib.strings.replicate 254 "a"}{name}";
          parameters.name = table;
        };
        namespaceLong = inNamespace {
          networkNamespace = "${lib.strings.replicate 255 "a"}{name}";
          parameters.name = table;
        };
      };
      expected = {
        accepted = [ ];
        name = [ "chalkos.diagnostics.Route_Get: the name is not of the form [a-z][a-z0-9-]*" ];
        program = [
          "chalkos.diagnostics.route.command: its first argument, ip, is no program of the image's store"
        ];
        placeholder = [
          "chalkos.diagnostics.route: the command or networkNamespace names {address}, which is no parameter"
        ];
        parameter = [ "chalkos.diagnostics.route.parameters.table: the command names no {table}" ];
        parameterName = [
          "chalkos.diagnostics.route.parameters.Table: the command names no {Table}"
          "chalkos.diagnostics.route.parameters.Table: the name is not of the form [a-z][a-z0-9-]*"
        ];
        optionalDefault = [
          "chalkos.diagnostics.route.parameters.table: it is optional and has a default; a default fills the arguments that name it, optional drops them"
        ];
        group = [ "chalkos.diagnostics.route.groups: the image has no group chalkos-no-such-group" ];
        groupForm = [
          "chalkos.diagnostics.route.groups: Wheel is not of the form [a-z_][a-z0-9_-]*"
          "chalkos.diagnostics.route.groups: the image has no group Wheel"
        ];
        longName = [
          "chalkos.diagnostics.${lib.strings.replicate 65 "a"}: the name is longer than 64 characters"
        ];
        longTimeout = [ "chalkos.diagnostics.route.timeout: 601s is longer than 10m" ];
        longestTimeout = [ ];
        namespace = [ ];
        namespaceOptional = [
          "chalkos.diagnostics.in-namespace.parameters.name: networkNamespace names it, so it is not optional"
        ];
        namespaceParameter = [
          "chalkos.diagnostics.in-namespace: the command or networkNamespace names {name}, which is no parameter"
        ];
        namespaceLongest = [ ];
        namespaceLong = [
          "chalkos.diagnostics.in-namespace.networkNamespace: it is longer than 255 characters, each parameter counted as one"
        ];
      };
    };
  # The image writes its diagnostics to /etc/chalkos/diagnostics.json and system.build, as chalkd
  # and chalkos-rules read them, with a default and a regex as empty strings when there is none.
  testDiagnosticsReachChalkd =
    let
      c = worker [
        (
          { pkgs, ... }:
          {
            chalkos.diagnostics.mine = {
              description = "A test.";
              command = [
                "${pkgs.iproute2}/bin/ip"
                "-json"
                "route"
                "show"
                "table"
                "{table}"
              ];
              parameters.table = {
                type = "int";
                min = 1;
                max = 254;
                default = "254";
                description = "A table.";
              };
              role = "reader";
              timeout = "5s";
            };
          }
        )
      ];
      etc = etcJSON "diagnostics.json" c;
      # writeText keeps its text as an attribute, so no build runs during evaluation.
      build = builtins.fromJSON (
        builtins.unsafeDiscardStringContext c.system.build.chalkosDiagnostics.text
      );
      mine = etc.mine // {
        command = lib.tail etc.mine.command;
      };
    in
    {
      expr = {
        inherit mine;
        same = etc == build;
      };
      expected = {
        mine = {
          description = "A test.";
          command = [
            "-json"
            "route"
            "show"
            "table"
            "{table}"
          ];
          parameters.table = {
            type = "int";
            description = "A table.";
            optional = false;
            default = "254";
            min = 1;
            max = 254;
            values = [ ];
            regex = "";
          };
          role = "reader";
          timeout = "5s";
          capabilities = [ ];
          groups = [ ];
          networkNamespace = "";
        };
        same = true;
      };
    };
  # A diagnostic's network namespace is a name below /run/netns: the type refuses a slash, a space
  # and a name that starts with . or -, so no value leaves the directory.
  testDiagnosticNamespaceStaysBelowTheDirectory =
    let
      accepts =
        ns:
        (builtins.tryEval (
          builtins.unsafeDiscardStringContext
            (worker [
              (
                { pkgs, ... }:
                {
                  chalkos.diagnostics.in-namespace = {
                    description = "A test.";
                    command = [ "${pkgs.iproute2}/bin/ip" ];
                    networkNamespace = ns;
                  };
                }
              )
            ]).environment.etc."chalkos/diagnostics.json".text
        )).success;
    in
    {
      expr = map accepts [
        "nat64-a.b_c"
        "_x"
        ".."
        "."
        ".x"
        "-x"
        "a/b"
        "../x"
        "a b"
        ""
      ];
      expected = [
        true
        true
        false
        false
        false
        false
        false
        false
        false
        false
      ];
    };
  # Every image declares the base diagnostics, the installer among them, each running a program of
  # the store. ip is the ip-only build, which leaves iproute2's other tools and arpd's
  # Berkeley DB out of the images.
  testBaseDiagnostics =
    let
      node = etcJSON "diagnostics.json" w;
      inInstaller = etcJSON "diagnostics.json" (installer [ ]);
      names = [
        "addresses"
        "dns-query"
        "ip-rules"
        "links"
        "neighbours"
        "nft-ruleset"
        "ping"
        "route-get"
        "routes"
        "tcp-connect"
      ];
    in
    {
      expr = {
        # The worker adds the diagnostics of FRR and the cluster registry, which
        # testKubernetesBGPOptional and testKubernetesRegistryOptional cover.
        names = lib.subtractLists [
          "bfd-peers"
          "bgp-neighbor"
          "bgp-route"
          "bgp-summary"
          "cluster-registry"
        ] (lib.attrNames node);
        installer = lib.attrNames inInstaller;
        inStore = lib.all (d: lib.hasPrefix "/nix/store/" (lib.head d.command)) (lib.attrValues node);
        ip = lib.hasSuffix "-iproute2-ip-${pkgs.iproute2.version}/bin/ip" (lib.head node.route-get.command);
        tcpConnect = {
          program = lib.hasSuffix "/bin/chalkd" (lib.head node.tcp-connect.command);
          arguments = lib.tail node.tcp-connect.command;
        };
      };
      expected = {
        inherit names;
        installer = names;
        inStore = true;
        ip = true;
        tcpConnect = {
          program = true;
          arguments = [
            "tcp-connect"
            "{address}"
            "{port}"
          ];
        };
      };
    };
  # Every image and the installer run the console on tty1 in place of the getty, with its actions;
  # an image turns the actions off, or the console with them, which brings the getty back and the
  # kernel's own console log level. chalkd and the console learn both from their units'
  # environments.
  testConsole = {
    expr =
      let
        env = c: {
          console = c.systemd.services.chalkd.environment.CHALKD_CONSOLE or null;
          actions = c.systemd.services.chalkd.environment.CHALKD_CONSOLE_ACTIONS or null;
          unit = c.systemd.services.chalkos-console.serviceConfig.TTYPath or null;
          consoleActions = c.systemd.services.chalkos-console.environment.CHALKOS_CONSOLE_ACTIONS or null;
          getty = c.systemd.services."getty@tty1".enable or true;
          autovt = c.systemd.services."autovt@tty1".enable or true;
          printk = c.boot.kernel.sysctl."kernel.printk" or null;
        };
      in
      {
        default = env w;
        installer = env (installer [ ]);
        noActions = env (worker [ { chalkos.console.actions.enable = false; } ]);
        off = env (worker [ { chalkos.console.enable = false; } ]);
      };
    expected = {
      default = {
        console = "1";
        actions = "1";
        unit = "/dev/tty1";
        consoleActions = "1";
        getty = false;
        autovt = false;
        printk = "1 4 1 7";
      };
      installer = {
        console = "1";
        actions = "1";
        unit = "/dev/tty1";
        consoleActions = "1";
        getty = false;
        autovt = false;
        printk = "1 4 1 7";
      };
      noActions = {
        console = "1";
        actions = "0";
        unit = "/dev/tty1";
        consoleActions = "0";
        getty = false;
        autovt = false;
        printk = "1 4 1 7";
      };
      off = {
        console = null;
        actions = null;
        unit = null;
        consoleActions = null;
        getty = true;
        autovt = true;
        printk = 4;
      };
    };
  };
  # No serial port or hypervisor console gets a getty: systemd's getty generator, which starts
  # them from the kernel's console= entries, is masked, and no image adds an instance itself.
  # nixpkgs's template stays, without a condition of chalkos's.
  testNoSerialGetty = {
    expr =
      let
        c = w;
      in
      {
        generator = c.systemd.generators.systemd-getty-generator;
        condition = c.systemd.services."serial-getty@".serviceConfig ? ExecCondition;
        units = lib.filter (lib.hasPrefix "serial-getty@") (lib.attrNames c.systemd.units);
      };
    expected = {
      generator = "/dev/null";
      condition = false;
      units = [ "serial-getty@.service" ];
    };
  };
  # The nftables unit runs touch, cp, rm and nft in place of nixpkgs's three bash scripts, and
  # keeps nixpkgs's ruleset, whose deletions are the file the unit saves.
  testNftablesUnitWithoutScripts = {
    expr =
      let
        s = w.systemd.services.nftables.serviceConfig;
        commands = map toString (s.ExecStart ++ [ s.ExecStartPost ] ++ s.ExecReload ++ s.ExecStop);
        # Each command's program by its name, without a store path's hash.
        programs = lib.unique (
          map (
            c: lib.last (builtins.match "([a-z0-9]{32}-)?(.*)" (baseNameOf (lib.head (lib.splitString " " c))))
          ) commands
        );
      in
      {
        inherit programs;
        rules = lib.hasSuffix "-nftables-rules" (toString (lib.elemAt s.ExecStart 1));
      };
    expected = {
      programs = [
        "touch"
        "nftables-rules"
        "cp"
        "nft"
        "rm"
      ];
      rules = true;
    };
  };
  # An image's own overlay can override chalkos's packages with overrideScope: mkImage adds the
  # chalkos scope before the image's overlays, so an image's chalkd replaces the one on the system
  # path, which is otherwise the scope's.
  testImageOverlayOverridesTheChalkosScope = {
    expr =
      let
        onPath = name: c: lib.any (p: lib.getName p == name) c.environment.systemPackages;
        custom =
          (mkImage {
            name = "custom-metal";
            platform = "metal";
            modules = [
              {
                nixpkgs.overlays = [
                  (_: prev: {
                    chalkos = prev.chalkos.overrideScope (
                      _: sp: { chalkd = sp.chalkd.overrideAttrs { pname = "chalkd-custom"; }; }
                    );
                  })
                ];
              }
            ];
          }).nixos.config;
      in
      {
        scope = lib.elem layered.plain-metal.pkgs.chalkos.chalkd layered.plain-metal.config.environment.systemPackages;
        custom = onPath "chalkd-custom" custom;
      };
    expected = {
      scope = true;
      custom = true;
    };
  };
  # The router modules get routerLib, the helpers and types the router's parts share, as a module
  # argument. Images without the router layer get it as well, as they declare the parts' options.
  testRouterModulesGetRouterLib = {
    expr =
      let
        # What a module of the image c reads of routerLib.
        probe =
          c:
          builtins.fromJSON
            (c [
              (
                { routerLib, ... }:
                {
                  environment.etc."chalkos-test/router-lib.json".text = builtins.toJSON {
                    kinds = routerLib ? kinds;
                    types = routerLib.types ? interface;
                    feature = routerLib ? feature;
                    declared = routerLib.declared [
                      "NAT64"
                      "Nope"
                    ];
                  };
                }
              )
            ]).environment.etc."chalkos-test/router-lib.json".text;
      in
      {
        router = probe router;
        worker = (probe worker).feature;
      };
    expected = {
      router = {
        kinds = true;
        types = true;
        feature = true;
        declared = [ "NAT64" ];
      };
      worker = true;
    };
  };
  # A Kubernetes image carries BGP unless it leaves the feature out. Without it, the image lacks
  # BGP's kinds, outputs, effects and tests, FRR's units, user, groups, package, files and
  # diagnostics, the firewall's rule for BFD's mark and networkd's leave of zebra's routes;
  # nothing else changes, and its build allocates the IDs of the same users and groups.
  testKubernetesBGPOptional =
    let
      names = lib.attrNames;
      parts = c: {
        kinds = names c.chalkos.kinds;
        outputs = names c.chalkos.outputs;
        effects = names c.chalkos.effects;
        tests = names c.chalkos.tests;
        services = names c.systemd.services;
        users = names c.users.users;
        groups = names c.users.groups;
        diagnostics = names c.chalkos.diagnostics;
        chalkd = map lib.getName c.systemd.services.chalkd.path;
        packages = map lib.getName c.environment.systemPackages;
        etc = names c.environment.etc;
        firewall = lib.splitString "\n" c.networking.firewall.extraInputRules;
        networkd = names (
          lib.filterAttrs (n: _: lib.hasPrefix "ManageForeign" n) c.systemd.network.config.networkConfig
        );
      };
      without = worker [ { chalkos.kubernetes.bgp.enable = false; } ];
      diff = a: b: lib.filterAttrs (_: v: v != [ ]) (lib.mapAttrs (k: v: lib.subtractLists b.${k} v) a);
    in
    {
      expr = {
        removed = diff (parts w) (parts without);
        added = diff (parts without) (parts w);
        ids = unpinned without == unpinned w;
      };
      expected = {
        removed = {
          chalkd = [ "frr" ];
          kinds = [
            "BGPPeer"
            "BGPSpeaker"
          ];
          outputs = [
            "bfd-firewall"
            "bgp"
          ];
          effects = [
            "bgp"
            "bgp-speaker"
          ];
          tests = [
            "kubernetes-bgp-add-speaker"
            "kubernetes-bgp-address"
            "kubernetes-bgp-address-interface"
            "kubernetes-bgp-address-two-interfaces"
            "kubernetes-bgp-bad-fields"
            "kubernetes-bgp-duplicate-address"
            "kubernetes-bgp-match-glob"
            "kubernetes-bgp-mixed"
            "kubernetes-bgp-new-asn"
            "kubernetes-bgp-new-password"
            "kubernetes-bgp-new-peer-asn"
            "kubernetes-bgp-no-router-id"
            "kubernetes-bgp-none"
            "kubernetes-bgp-peer-without-speaker"
            "kubernetes-bgp-remove-speaker"
            "kubernetes-bgp-shared-unnumbered"
            "kubernetes-bgp-unconfigured-export"
            "kubernetes-bgp-unconfigured-peer-interface"
            "kubernetes-bgp-unnumbered"
          ];
          services = [
            "frr"
            "frr-bfdd"
            "frr-bgpd"
            "frr-mgmtd"
            "frr-staticd"
            "frr-zebra"
          ];
          users = [ "frr" ];
          groups = [
            "frr"
            "frrvty"
          ];
          diagnostics = [
            "bfd-peers"
            "bgp-neighbor"
            "bgp-route"
            "bgp-summary"
          ];
          packages = [ "frr" ];
          etc = [
            "frr/frr.conf"
            "frr/vtysh.conf"
          ];
          firewall = [ "udp dport 3784 meta mark & 0x08000000 == 0x08000000 accept" ];
          networkd = [
            "ManageForeignNextHops"
            "ManageForeignRoutes"
          ];
        };
        added = { };
        ids = true;
      };
    };
  # The cluster registry that an image disables with chalkos.kubernetes.registry.enable = false
  # leaves nothing of itself in the image, and nothing comes in its place.
  testKubernetesRegistryOptional =
    let
      names = lib.attrNames;
      parts = c: {
        kinds = names c.chalkos.kinds;
        outputs = names c.chalkos.outputs;
        effects = names c.chalkos.effects;
        tests = names c.chalkos.tests;
        services = names c.systemd.services;
        paths = names c.systemd.paths;
        users = names c.users.users;
        groups = names c.users.groups;
        diagnostics = names c.chalkos.diagnostics;
        mirrors = c.systemd.services.chalkos-registry-mirrors.chalkos.kinds;
        kubernetesAfter = c.chalkos.effects.kubernetes.after;
        firewall = lib.filter (l: l != "") (lib.splitString "\n" c.networking.firewall.extraInputRules);
      };
      without = worker [ { chalkos.kubernetes.registry.enable = false; } ];
      diff = a: b: lib.filterAttrs (_: v: v != [ ]) (lib.mapAttrs (k: v: lib.subtractLists b.${k} v) a);
    in
    {
      expr = {
        removed = diff (parts w) (parts without);
        added = diff (parts without) (parts w);
        ids = unpinned without == unpinned w;
      };
      expected = {
        removed = {
          kinds = [ "ClusterRegistry" ];
          outputs = [ "cluster-registry" ];
          effects = [ "cluster-registry" ];
          tests = [
            "kubernetes-registry-bad-fields"
            "kubernetes-registry-elsewhere"
            "kubernetes-registry-moved"
            "kubernetes-registry-moved-unregistered"
            "kubernetes-registry-port"
            "kubernetes-registry-spelled"
            "kubernetes-registry-volume"
            "kubernetes-registry-volume-elsewhere"
            "kubernetes-registry-worker"
          ];
          services = [
            "chalkos-registry"
            "chalkos-registry-credentials-changed"
          ];
          paths = [ "chalkos-registry-credentials-changed" ];
          users = [ "chalkos-registry" ];
          groups = [ "chalkos-registry" ];
          diagnostics = [ "cluster-registry" ];
          mirrors = [
            "ClusterRegistry"
            "Network"
          ];
          kubernetesAfter = [
            "cluster-registry"
            "registry-mirrors"
          ];
          firewall = [ "meta mark & 0x04000000 == 0x04000000 accept" ];
        };
        added = { };
        ids = true;
      };
    };
  # On a Kubernetes image FRR runs the output bgp's config, which vtysh checks, reloads when it
  # changes, loads the BGPPeers' passwords, and announces the node's addresses before the
  # Kubernetes preparation picks them. FRR's units start only on a node whose output bgp holds the
  # marker of a BGPSpeaker, and a delivery that adds or removes the speaker restarts frr.service,
  # which starts or stops FRR. frr.service loads the table that marks BFD from the BGPPeers, which
  # nft checks, before it starts and on each reload, and deletes it when it stops; the firewall
  # accepts BFD with the mark alone.
  testKubernetesBGP = {
    expr =
      let
        frr = w.systemd.services.frr;
        bgp = w.chalkos.outputs.bgp;
      in
      {
        option = w.chalkos.kubernetes.bgp.enable;
        check = lib.hasInfix "/bin/vtysh --dryrun" bgp.check;
        render = lib.hasSuffix " kubernetes-frr" bgp.render;
        inherit (bgp) inputs;
        reload = w.chalkos.effects.bgp.reload;
        grant = frr.chalkos.kinds;
        before = lib.elem "chalkos-node-addresses.target" frr.before;
        wantedBy = lib.sort lib.lessThan frr.wantedBy;
        prepareAfter = lib.elem "chalkos-node-addresses.target" w.systemd.services.chalkos-kubernetes.after;
        filled = w.environment.etc."frr/frr.conf".source;
        conditions = lib.mapAttrs (_: s: lib.filterAttrs (n: _: lib.hasPrefix "Condition" n) s.unitConfig) (
          lib.filterAttrs (n: _: n == "frr" || lib.hasPrefix "frr-" n) w.systemd.services
        );
        start = {
          inherit (w.chalkos.effects.bgp-speaker) restart start after;
          when = lib.hasInfix "BGPSpeaker" w.chalkos.effects.bgp-speaker.when;
        };
        reloadOn = w.chalkos.effects.bgp.outputs;
        bfd =
          let
            load = "/bin/nft -f /run/chalkos/outputs/bfd-firewall/bfd.nft";
            reload = lib.head frr.serviceConfig.ExecReload;
          in
          {
            inherit (w.chalkos.outputs.bfd-firewall) inputs;
            render = lib.hasSuffix " kubernetes-bfd 0x08000000" w.chalkos.outputs.bfd-firewall.render;
            check = lib.hasInfix " check each bfd.nft /nix/store/" w.chalkos.outputs.bfd-firewall.check;
            after = lib.elem "nftables.service" frr.after;
            load =
              lib.hasPrefix "+" frr.serviceConfig.ExecStartPre
              && lib.hasSuffix load frr.serviceConfig.ExecStartPre;
            reload = lib.hasPrefix "+" reload && lib.hasSuffix load reload;
            reloads = lib.length frr.serviceConfig.ExecReload;
            delete = lib.hasSuffix "/bin/nft delete table inet chalkos-bfd" frr.serviceConfig.ExecStopPost;
            firewall = lib.filter (lib.hasInfix "3784") (
              lib.splitString "\n" w.networking.firewall.extraInputRules
            );
            ports = w.networking.firewall.allowedUDPPorts;
          };
      };
    expected = {
      option = true;
      check = true;
      render = true;
      inputs = [
        "BGPPeer"
        "BGPSpeaker"
        "Node"
      ];
      reload = [ "frr.service" ];
      grant = [ "BGPPeer" ];
      before = true;
      wantedBy = [
        "chalkos-node-addresses.target"
        "multi-user.target"
      ];
      prepareAfter = true;
      filled = "/run/frr/frr.conf";
      conditions =
        lib.genAttrs
          [
            "frr"
            "frr-bfdd"
            "frr-bgpd"
            "frr-mgmtd"
            "frr-staticd"
            "frr-zebra"
          ]
          (_: {
            ConditionPathIsDirectory = "/run/chalkos/credentials/frr";
            ConditionPathExists = "/run/chalkos/outputs/bgp/speaker";
          });
      start = {
        restart = [ "frr.service" ];
        start = true;
        after = [
          "networkd"
          "links"
        ];
        when = true;
      };
      reloadOn = [
        "bgp"
        "bfd-firewall"
      ];
      bfd = {
        inputs = [
          "BGPPeer"
          "BGPSpeaker"
        ];
        render = true;
        check = true;
        after = true;
        load = true;
        reload = true;
        reloads = 3;
        delete = true;
        firewall = [ "udp dport 3784 meta mark & 0x08000000 == 0x08000000 accept" ];
        ports = [ ];
      };
    };
  };
  # The cluster registry runs on the nodes with a rendered port, as its own user with pinned IDs
  # and without capabilities, on copies of chalkd's credentials, which a change of either file
  # makes it read again. It is ready once it listens and comes before the kubelet's effect.
  testKubernetesRegistry = {
    expr =
      let
        unit = w.systemd.services.chalkos-registry;
        effect = w.chalkos.effects.cluster-registry;
      in
      {
        option = w.chalkos.kubernetes.registry.enable;
        condition = unit.unitConfig.ConditionPathExists;
        inherit (unit.serviceConfig)
          User
          CapabilityBoundingSet
          Type
          LoadCredential
          ;
        ids = [
          w.users.users.chalkos-registry.uid
          w.users.groups.chalkos-registry.gid
        ];
        changed = w.systemd.paths.chalkos-registry-credentials-changed.pathConfig.PathChanged;
        restartOnChange = lib.hasSuffix "systemctl --no-block try-restart chalkos-registry.service" w.systemd.services.chalkos-registry-credentials-changed.serviceConfig.ExecStart;
        inherit (effect) restart start;
        kubelet = lib.elem "cluster-registry" w.chalkos.effects.kubernetes.after;
      };
    expected = {
      option = true;
      condition = "/run/chalkos/outputs/cluster-registry/registry.env";
      User = "chalkos-registry";
      CapabilityBoundingSet = [ "" ];
      Type = "notify";
      LoadCredential = [
        "ca.crt:/state/chalkd/ca.crt"
        "node.pem:/state/chalkd/node.pem"
      ];
      ids = [
        333
        333
      ];
      changed = [
        "/state/chalkd/ca.crt"
        "/state/chalkd/node.pem"
      ];
      restartOnChange = true;
      restart = [ "chalkos-registry.service" ];
      start = true;
      kubelet = true;
    };
  };
  # Every image loads its IPE policy: ipe in the one lsm= the kernel reads, audit for the
  # records chalkd reads, the policy text with the store's root hash filled in at build, and
  # the initrd unit that loads it before switch-root. The UKI's config names no initrd, as the
  # UKI build passes the policy's archive first and the image's initrd after it.
  testModulePolicy =
    let
      version = import ../../version.nix;
      unit = w.boot.initrd.systemd.services.chalkos-module-policy;
    in
    {
      expr = {
        lsm = lib.filter (lib.hasPrefix "lsm=") w.boot.kernelParams;
        audit = lib.elem "audit=1" w.boot.kernelParams;
        policy = w.system.build.chalkosModulePolicy;
        ukiInitrd = w.boot.uki.settings.UKI.Initrd or null;
        unit = {
          inherit (unit) wantedBy before;
          inherit (unit.unitConfig) DefaultDependencies;
          inherit (unit.serviceConfig) Type RemainAfterExit;
          load = lib.hasSuffix "/bin/chalkos-module-policy load" unit.serviceConfig.ExecStart;
        };
        stored = lib.any (
          p: lib.hasInfix "chalkos-module-policy" (toString (p.source or p))
        ) w.boot.initrd.systemd.storePaths;
      };
      expected = {
        lsm = [ "lsm=ipe,landlock,yama,bpf" ];
        audit = true;
        policy = ''
          policy_name=chalkos policy_version=${version}
          DEFAULT action=ALLOW
          DEFAULT op=KMODULE action=DENY
          op=KMODULE boot_verified=TRUE action=ALLOW
          op=KMODULE dmverity_roothash=sha256:@ROOT_HASH@ action=ALLOW
        '';
        ukiInitrd = null;
        unit = {
          wantedBy = [ "sysinit.target" ];
          before = [ "sysinit.target" ];
          DefaultDependencies = false;
          Type = "oneshot";
          RemainAfterExit = true;
          load = true;
        };
        stored = true;
      };
    };
  # An image may leave the module policy out: no ipe, no unit, nixpkgs's own UKI.
  testModulePolicyOptional =
    let
      c = worker [ { chalkos.kernel.modulePolicy.enable = false; } ];
    in
    {
      expr = {
        lsm = lib.any (lib.hasInfix "ipe") c.boot.kernelParams;
        unit = c.boot.initrd.systemd.services ? chalkos-module-policy;
        ukiInitrd = c.boot.uki.settings.UKI.Initrd != null;
      };
      expected = {
        lsm = false;
        unit = false;
        ukiInitrd = true;
      };
    };
  # The installer and the router load the policy too.
  testModulePolicyEveryImage = {
    expr = map (c: c.boot.initrd.systemd.services ? chalkos-module-policy) [
      (installer [ ])
      edge
    ];
    expected = [
      true
      true
    ];
  };
  # No container, privileged or not, gets CAP_SYS_MODULE or CAP_MAC_ADMIN: containerd's bounding
  # set, which its shims and runc inherit, leaves both out. allowMacAdmin gives CAP_MAC_ADMIN back.
  testContainerdCapabilities = {
    expr = map (c: c.systemd.services.containerd.serviceConfig.CapabilityBoundingSet) [
      w
      (worker [ { chalkos.kubernetes.containerd.allowMacAdmin = true; } ])
    ];
    expected = [
      "~CAP_SYS_MODULE CAP_MAC_ADMIN"
      "~CAP_SYS_MODULE"
    ];
  };
  # A layer's feature is on by default and names its kinds; one may be off by default and carry
  # no kinds. The descriptions of the existing features stay as they were.
  testFeatureOption =
    let
      f = (import ../../lib/chalkos.nix { inherit lib; }).internal.feature;
      opt = args: (f "Kubernetes" "x" args).x.enable;
    in
    {
      expr = {
        onDefault =
          (opt {
            carries = "X";
            kinds = [ "A" ];
          }).default;
        onText =
          (opt {
            carries = "X";
            kinds = [ "A" ];
          }).description;
        offDefault =
          (opt {
            carries = "X";
            default = false;
          }).default;
        offText =
          (opt {
            carries = "X";
            default = false;
          }).description;
      };
      expected = {
        onDefault = true;
        onText = "Whether Kubernetes images carry X and accept the kind `A`. Only with the Kubernetes layer; set it false in an image's modules to leave the feature out of it.";
        offDefault = false;
        offText = "Whether Kubernetes images carry X. Only with the Kubernetes layer; set it true in an image's modules to add the feature to it.";
      };
    };
  # Kubernetes images link /lib/modules to the image's module tree, whose files are its own, so
  # the hostPath mounts of ceph-csi, Cilium, Longhorn and the GPU operator read the modules
  # without the store; a router image has no such link. They put the host tools into /usr/bin and
  # /usr/sbin of the store's file system, which is /usr on a node, and the system references their
  # links, so the store partition and the shell checks hold them.
  testKubernetesHostBase =
    let
      tools = worker [
        (
          { pkgs, ... }:
          {
            chalkos.kubernetes.hostTools.fstrim = "${pkgs.util-linux}/bin/fstrim";
          }
        )
      ];
      store = c: c.image.repart.partitions.${c.image.repart.verityStore.partitionIds.store};
      entries = lib.mapAttrs (_: lib.mapAttrs (_: e: { inherit (e) mode argument; }));
    in
    {
      expr = {
        libModules = entries w.systemd.tmpfiles.settings.chalkos-lib-modules;
        router = edge.systemd.tmpfiles.settings ? chalkos-lib-modules;
        noTools = (store w).contents;
        tools = lib.attrNames (store tools).contents;
        usrIsStore = w.fileSystems."/nix/store".device;
        stored = lib.any (p: lib.hasInfix "chalkos-usr-links" (toString p)) tools.system.extraDependencies;
      };
      expected = {
        libModules = {
          "/lib".d = {
            mode = "0755";
            argument = "";
          };
          "/lib/modules"."L+" = {
            mode = "-";
            argument = "${w.system.build.chalkosKernelModules}/lib/modules";
          };
        };
        router = false;
        noTools = { };
        tools = [
          "/bin"
          "/sbin"
        ];
        usrIsStore = "/usr/nix/store";
        stored = true;
      };
    };
  # containerd's runtime handlers: runc, and those an image adds, in the read-only config; CDI
  # specifications, which containerd 2.4 always reads, from the image (/etc/cdi) and from units
  # at boot (/var/run/cdi).
  testContainerdRuntimes =
    let
      c = worker [
        {
          chalkos.kubernetes.containerd.runtimes.kata.options.ConfigPath = "/etc/kata/configuration.toml";
        }
      ];
      runtime = x: x.virtualisation.containerd.settings.plugins."io.containerd.cri.v1.runtime";
    in
    {
      expr = {
        runtimes = (runtime c).containerd.runtimes;
        cdi = (runtime w).cdi_spec_dirs;
        runcRefused = refuses "names runc, which is containerd's own handler" (worker [
          { chalkos.kubernetes.containerd.runtimes.runc = { }; }
        ]);
      };
      expected = {
        runtimes = {
          runc.options.SystemdCgroup = true;
          kata = {
            runtime_type = "io.containerd.runc.v2";
            options.ConfigPath = "/etc/kata/configuration.toml";
          };
        };
        cdi = [
          "/etc/cdi"
          "/var/run/cdi"
        ];
        runcRefused = true;
      };
    };
  # ceph and kubevirt are on by default: rbd, ceph and nbd load at boot, LVM stays for Rook's
  # OSDs, and the kubevirt group carries KVM, vhost, VFIO and UIO for on-demand loading. The
  # IOMMU option adds the kernel parameters and loads vfio_pci. Off, each leaves nothing behind.
  testKubernetesCephKubevirt =
    let
      off = worker [
        {
          chalkos.kubernetes.ceph.enable = false;
          chalkos.kubernetes.kubevirt.enable = false;
        }
      ];
      iommu = worker [ { chalkos.kubernetes.kubevirt.iommu = true; } ];
      ours = [
        "rbd"
        "ceph"
        "nbd"
        "vfio_pci"
      ];
      loaded = c: lib.filter (m: lib.elem m ours) c.boot.kernelModules;
      iommuParams = c: lib.filter (lib.hasInfix "iommu") c.boot.kernelParams;
    in
    {
      expr = {
        defaults = {
          inherit (w.chalkos.kubernetes.ceph) enable;
          kubevirt = w.chalkos.kubernetes.kubevirt.enable;
          inherit (w.chalkos.kubernetes.kubevirt) iommu;
        };
        loaded = loaded w;
        lvm = w.services.lvm.enable;
        group = lib.elem "kubevirt" w.chalkos.kernel.moduleGroups;
        inherit (w.chalkos.kernel.groups.kubevirt) directories;
        params = iommuParams w;
        offLoaded = loaded off;
        offGroup = lib.elem "kubevirt" off.chalkos.kernel.moduleGroups;
        iommuParams = iommuParams iommu;
        iommuLoaded = loaded iommu;
        routerGroup = lib.elem "kubevirt" edge.chalkos.kernel.moduleGroups;
      };
      expected = {
        defaults = {
          enable = true;
          kubevirt = true;
          iommu = false;
        };
        loaded = [
          "ceph"
          "nbd"
          "rbd"
        ];
        lvm = true;
        group = true;
        directories = [
          "arch/x86/kvm"
          "drivers/vhost"
          "drivers/vfio"
          "drivers/uio"
        ];
        params = [ ];
        offLoaded = [ ];
        offGroup = false;
        iommuParams = [
          "intel_iommu=on"
          "iommu=pt"
        ];
        iommuLoaded = [
          "ceph"
          "nbd"
          "rbd"
          "vfio_pci"
        ];
        routerGroup = false;
      };
    };
  # longhorn is off by default. On, the image runs iscsid from boot, with its socket, carries
  # iscsiadm, the NFS client and host-tool links, and loads iscsi_tcp and dm_crypt; iscsiadm's
  # database and the node's initiator name live on VAR.
  testKubernetesLonghorn =
    let
      on = worker [ { chalkos.kubernetes.longhorn.enable = true; } ];
      iscsid = on.systemd.services.iscsid;
    in
    {
      expr = {
        default = w.chalkos.kubernetes.longhorn.enable;
        offSocket = w.systemd.sockets ? iscsid;
        offService = w.systemd.services ? iscsid;
        socket = on.systemd.sockets.iscsid.listenStreams;
        socketWanted = on.systemd.sockets.iscsid.wantedBy;
        serviceWanted = iscsid.wantedBy;
        type = iscsid.serviceConfig.Type;
        # Longhorn enters iscsid's mount namespace, which must be the host's.
        noMountNamespace =
          !lib.any (o: iscsid.serviceConfig ? ${o}) [
            "NoExecPaths"
            "ProtectSystem"
            "PrivateTmp"
            "PrivateNetwork"
            "ReadOnlyPaths"
          ];
        hostTools = lib.attrNames on.chalkos.kubernetes.hostTools;
        etc = lib.filter (lib.hasPrefix "iscsi/") (lib.attrNames on.environment.etc);
        initiator = on.environment.etc."iscsi/initiatorname.iscsi".source;
        initiatorName =
          on.systemd.tmpfiles.settings.chalkos-iscsi."/var/lib/iscsi/initiatorname.iscsi".f.argument;
        nodes = on.environment.etc."iscsi/nodes".source;
        mountNfs = lib.any (p: lib.getName p == "nfs-utils-chalkos") on.environment.systemPackages;
        loaded = lib.filter (
          m:
          lib.elem m [
            "iscsi_tcp"
            "dm_crypt"
          ]
        ) on.boot.kernelModules;
      };
      expected = {
        default = false;
        offSocket = false;
        offService = false;
        socket = [ "@ISCSIADM_ABSTRACT_NAMESPACE" ];
        socketWanted = [ "sockets.target" ];
        serviceWanted = [ "multi-user.target" ];
        type = "notify";
        noMountNamespace = true;
        hostTools = [
          "cryptsetup"
          "dmsetup"
          "fstrim"
          "iscsiadm"
          "mount"
        ];
        etc = [
          "iscsi/ifaces"
          "iscsi/initiatorname.iscsi"
          "iscsi/iscsid.conf"
          "iscsi/isns"
          "iscsi/nodes"
          "iscsi/send_targets"
          "iscsi/static"
        ];
        initiator = "/var/lib/iscsi/initiatorname.iscsi";
        initiatorName = "InitiatorName=iqn.2026-11.dev.chalkos:%m";
        nodes = "/var/lib/iscsi/nodes";
        mountNfs = true;
        loaded = [
          "dm_crypt"
          "iscsi_tcp"
        ];
      };
    };
  # nvidia is off by default. On, with NVIDIA's unfree packages allowed: NVIDIA's open or
  # proprietary modules from boot.extraModulePackages, loaded at boot with nouveau kept out, the
  # gpu group and the GSP firmware, containerd's nvidia handler, the unit that writes the CDI
  # specification without running programs from outside the store, and the driver root at
  # /usr/local/nvidia. Without the allowance, the image names the packages to allow: the open
  # modules are free, the proprietary ones are not.
  testKubernetesNvidia =
    let
      on =
        type: allow:
        worker [
          {
            nixpkgs.config.allowUnfreePredicate = p: lib.elem (lib.getName p) allow;
            chalkos.kubernetes.nvidia = {
              enable = true;
              kernelModuleType = type;
            };
          }
        ];
      open = on "open" [ "nvidia-x11" ];
      proprietary = on "proprietary" [
        "nvidia-x11"
        "nvidia-kernel-modules"
      ];
      names = c: map lib.getName c.boot.extraModulePackages;
      cdi = open.systemd.services.chalkos-nvidia-cdi;
    in
    {
      expr = {
        default = w.chalkos.kubernetes.nvidia.enable;
        defaultType = w.chalkos.kubernetes.nvidia.kernelModuleType;
        offUnit = w.systemd.services ? chalkos-nvidia-cdi;
        offLink = w.chalkos.usr.links ? "local/nvidia";
        openModules = names open;
        proprietaryModules = names proprietary;
        loaded = lib.filter (lib.hasPrefix "nvidia") open.boot.kernelModules;
        nouveau = lib.elem "nouveau" open.boot.blacklistedKernelModules;
        gpu = lib.elem "gpu" open.chalkos.kernel.moduleGroups;
        # hardware.firmware is the environment of the packages, compressed where they allow it.
        firmware = lib.any (
          p:
          lib.hasSuffix "-nvidia-x11-${open.chalkos.kubernetes.nvidia.package.version}-firmware" (toString p)
        ) open.hardware.firmware.paths;
        runtime = lib.hasSuffix "/bin/nvidia-container-runtime" open.chalkos.kubernetes.containerd.runtimes.nvidia.options.BinaryName;
        cdi = lib.hasInfix "/bin/nvidia-ctk cdi generate --output=/var/run/cdi/nvidia.yaml --driver-root=/usr/local/nvidia " cdi.serviceConfig.ExecStart;
        cdiWanted = cdi.wantedBy;
        cdiNoExec =
          cdi.serviceConfig.NoExecPaths == [ "/" ] && cdi.serviceConfig.ExecPaths == [ "/nix/store" ];
        driverRoot = lib.hasPrefix "/nix/store/" (toString open.chalkos.usr.links."local/nvidia");
        refusedOpen = refuses ''[ "nvidia-x11" ]'' (worker [
          { chalkos.kubernetes.nvidia.enable = true; }
        ]);
        refusedProprietary = refuses ''[ "nvidia-kernel-modules" ]'' (worker [
          {
            nixpkgs.config.allowUnfreePredicate = p: lib.getName p == "nvidia-x11";
            chalkos.kubernetes.nvidia = {
              enable = true;
              kernelModuleType = "proprietary";
            };
          }
        ]);
        allowed = refuses "NVIDIA's unfree" open || refuses "NVIDIA's unfree" proprietary;
      };
      expected = {
        default = false;
        defaultType = "open";
        offUnit = false;
        offLink = false;
        openModules = [ "nvidia-open" ];
        proprietaryModules = [ "nvidia-kernel-modules" ];
        loaded = [
          "nvidia"
          "nvidia_drm"
          "nvidia_modeset"
          "nvidia_uvm"
        ];
        nouveau = true;
        gpu = true;
        firmware = true;
        runtime = true;
        cdi = true;
        cdiWanted = [ "multi-user.target" ];
        cdiNoExec = true;
        driverRoot = true;
        refusedOpen = true;
        refusedProprietary = true;
        allowed = false;
      };
    };
  # The kernel's modprobe helper, which loads the modules pods need on demand, is kmod's ELF
  # modprobe, set by nixos-init, and no modprobe.d install or remove command makes it run a shell.
  testModprobeWithoutShell =
    let
      binary = w.boot.bootspec.extensions."org.nixos.nixos-init.v1".modprobe_binary;
    in
    {
      expr = {
        modprobe = lib.hasSuffix "/bin/modprobe" binary;
        kmod = lib.hasInfix "-kmod-" binary;
        commands = builtins.match "(.*\n)?[[:space:]]*(install|remove)[[:space:]].*" w.boot.extraModprobeConfig;
      };
      expected = {
        modprobe = true;
        kmod = true;
        commands = null;
      };
    };
}
