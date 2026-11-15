# Evaluation tests of chalkos.lib.mkImages and the images it builds; returns lib.runTests failures
# (empty on success).
{
  lib,
  pkgs,
  mkImages,
  flakeLib,
}:
let
  inherit (import ./lib.nix { inherit lib mkImages; })
    fails
    refuses
    etcJSON
    hasPackage
    worker
    installer
    router
    routerWithout
    ;
  # A variant of each set of layers on metal: node has the Kubernetes layer, plain none, edge the
  # router layer.
  layered =
    (mkImages {
      platforms = [ "metal" ];
      variants = {
        node = { };
        plain.kubernetes = false;
        edge = {
          kubernetes = false;
          router = true;
        };
      };
    }).nixos;
  # The images most tests read, each evaluated once: a variant worker without modules on each
  # platform with the installer, the router with every feature, and the router without each
  # feature.
  workerImages = mkImages { variants.worker = { }; };
  w = workerImages.nixos.worker-metal.config;
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
lib.runTests {
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
  # mkImages builds each variant on each platform, metal and kvm unless the arguments name
  # others, as <variant>-<platform>, each with its OCI layout as oci.<variant>-<platform>, and
  # one installer; without variants it builds node.
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
        oci = lib.attrNames i.oci;
        ociBuild = lib.all (d: lib.isDerivation d && lib.hasSuffix "-oci" d.name) (lib.attrValues i.oci);
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
      oci = [
        "node-kvm"
        "node-metal"
        "storage-kvm"
        "storage-metal"
      ];
      ociBuild = true;
    };
  };
  # Each image names its variant and platform, carries the layers its variant asks for and the OS
  # CA; the installer is no variant, belongs to no platform and carries no layer.
  testMkImagesDescribesEachImage = {
    expr =
      let
        pub = builtins.toFile "secrets.pub.json" ''{"version": 3, "osCA": {"certificate": "PEM"}}'';
        i = mkImages {
          osCA = pub;
          variants = {
            node = { };
            storage.kubernetes = false;
            edge = {
              kubernetes = false;
              router = true;
            };
          };
          platforms = [ "metal" ];
        };
        summary = n: {
          variant = n.config.chalkos.variant.name;
          platform = n.config.chalkos.platform.name;
          kubernetes = n.config.chalkos.kubernetes.enable;
          router = n.config.chalkos.router.enable;
          osCA = n.config.environment.etc ? "chalkos/os-ca.crt";
        };
      in
      lib.mapAttrs (_: summary) i.nixos;
    expected = {
      installer = {
        variant = null;
        platform = null;
        kubernetes = false;
        router = false;
        osCA = true;
      };
      edge-metal = {
        variant = "edge";
        platform = "metal";
        kubernetes = false;
        router = true;
        osCA = true;
      };
      node-metal = {
        variant = "node";
        platform = "metal";
        kubernetes = true;
        router = false;
        osCA = true;
      };
      storage-metal = {
        variant = "storage";
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
  # An image carries one layer at most: a variant with both the Kubernetes and the router layer
  # fails to build, naming the variant.
  testLayersExcludeEachOther = {
    expr =
      let
        refused = refuses "the variant both enables the kubernetes and router layers";
        both = (mkImages { variants.both.router = true; }).nixos.both-metal.config;
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
  # A router feature that a variant disables with chalkos.router.<feature>.enable = false leaves
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
            services = [ "frr" ];
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
              "frr/daemons"
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
        };
        added = [ ];
        ids = true;
        foreignRoutes = true;
        otherImages = [
          [ ]
          [ ]
        ];
      };
    };
  # The output dhcp-relay renders a dnsmasq config per VRF from the DHCPRelay documents, the VRFs,
  # the Networks and, to refuse a family relayed beside Kea's servers, the DHCPSubnets, and checks
  # each with dnsmasq --test. dnsmasq-relay runs the default VRF's relay and dnsmasq-relay@<vrf>
  # each VRF's, bound to the VRF's device, whose index it records, as the user dnsmasq with the
  # capabilities dnsmasq demands of a relay. chalkos-dhcp-relays starts, restarts and stops them
  # without capabilities whenever the output dhcp-relay or networkd changes.
  testRouterDHCPRelay = {
    expr =
      let
        relay = edge.systemd.services.dnsmasq-relay;
        vrf = edge.systemd.services."dnsmasq-relay@";
        relays = edge.systemd.services.chalkos-dhcp-relays;
      in
      {
        check = lib.hasInfix "/bin/dnsmasq --test" edge.chalkos.outputs.dhcp-relay.check;
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
            builtins.match "/nix/store/[^ ]*-chalkos-vrf-device dnsmasq-relay %I" vrf.serviceConfig.ExecStartPre
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
      "vrf.yaml"
    ];
  };
  # On a router image with every feature, the test of each router example compares all eight router outputs with golden files,
  # and router-vrf-strict the ruleset; on one without a feature, no router test compares any.
  testRouterGoldens =
    let

      # The tests of the router's examples of an image.
      exampleTests = c: map (file: "router-${lib.removeSuffix ".yaml" file}") (examplesOf c);
      # The outputs a test of an image compares.
      outputsOf = c: name: lib.attrNames c.chalkos.tests.${name}.outputs;
      # The outputs each test of the router's examples of an image compares, by test.
      compared = c: lib.genAttrs (exampleTests c) (outputsOf c);
      routerOutputs = [
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
  # A router image without BGP, NAT64, the resolver, DHCP, the DHCP relay and WireGuard keeps the
  # router's core: the router kinds of the firewall, VRFs and policy routing, the outputs and units
  # of the firewall, the kernel settings and the VRFs, the console and the copy of chalkd's lines
  # to the serial port every metal image runs, and the diagnostics of every image and traceroute.
  # Its tests run the examples of none of the six, and its build allocates the IDs of the
  # same users and groups as an image with every feature.
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
  # comes up, the outputs router-sysctl and router-nftables render the router kinds, and their
  # effects restart systemd-sysctl, which waits for the rendered file, and reload the firewall. The
  # image loads the kernel modules of its rulesets.
  # Other images keep NixOS's firewall.
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
  # Router images run FRR with bgpd and bfdd, whose config the output frr renders from the BGP
  # kinds and the Network documents' names, checked by vtysh, and whose reload the effect asks
  # for. frr.service reads the BGPPeers' passwords as credentials, starts after the firewall and
  # before the node's addresses settle, on a node with config alone, and fills its config in
  # before it starts, a restart of a failed unit included, and on reload; the firewall opens the
  # peers' ports. chalkd asks vtysh for FRR's state, networkd keeps zebra's routes, and the image
  # adds the BGP diagnostics. Other images run no FRR.
  testRouterBGP = {
    expr =
      let
        frr = edge.systemd.services.frr;
      in
      {
        daemons = {
          inherit (edge.services.frr.bgpd) enable;
          bfdd = edge.services.frr.bfdd.enable;
        };
        check = lib.hasInfix "vtysh --dryrun" edge.chalkos.outputs.frr.check;
        grant = frr.chalkos.kinds;
        after = lib.subtractLists frr.after [
          "chalkos-config.service"
          "chalkos-firewall.service"
          "systemd-networkd.service"
        ];
        before = lib.elem "chalkos-node-addresses.target" frr.before;
        wantedBy = lib.elem "chalkos-node-addresses.target" frr.wantedBy;
        filled = edge.environment.etc."frr/frr.conf".source;
        condition = frr.unitConfig.ConditionPathIsDirectory;
        reload = lib.length frr.serviceConfig.ExecReload;
        fillsAlike = frr.serviceConfig.ExecStartPre == lib.head frr.serviceConfig.ExecReload;
        # frr-reload.py writes the running config over any other file than confdir's frr.conf.
        reloadsLink = lib.hasSuffix " --confdir /etc/frr --rundir /run/frr /etc/frr/frr.conf" (
          lib.last frr.serviceConfig.ExecReload
        );
        refresh = frr.serviceConfig.RefreshOnReload;
        networkd = edge.systemd.network.config.networkConfig;
        groups = edge.chalkos.diagnostics.bgp-summary.groups;
        plain = layered.plain-metal.config.services.frr.bgpd.enable;
        node = layered.node-metal.config.chalkos.outputs ? frr;
      };
    expected = {
      daemons = {
        enable = true;
        bfdd = true;
      };
      check = true;
      grant = [ "BGPPeer" ];
      after = [ ];
      before = true;
      wantedBy = true;
      filled = "/run/frr/frr.conf";
      condition = "/run/chalkos/credentials/frr";
      reload = 2;
      fillsAlike = true;
      reloadsLink = true;
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
  # Router images accept VRF documents and render them with networkd's documents and the NAT64s,
  # whose links networkd leaves alone, through chalkos-render router-networkd; other images render
  # networkd's documents alone. chalkos-vrf, which a change of the output networkd reloads, runs
  # before networkd at boot and removes the VRFs the rendered units no longer declare. FRR orders
  # after networkd, which creates a VRF's device before FRR's reload configures its instance, and
  # the BGP and BFD diagnostics take a VRF.
  # The firewall reads the VRFs and the Network documents.
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
  # Router images accept DHCPSubnet documents, which the output kea renders into a config per VRF
  # and family and checks with kea-dhcp4 -t or kea-dhcp6 -t, and run the servers of Kea without its
  # database backends, with the lease commands' and the run_script hooks alone, as the user kea:
  # kea-dhcp4 with raw sockets and port 67, kea-dhcp6 with port 547 alone and the directory of
  # chalkos-dhcp6-pd, which records delegated prefixes, as that of hook scripts; one each for the
  # default VRF, and kea-dhcp4@<vrf> and kea-dhcp6@<vrf> bound to the VRF's device, whose index
  # they record, requiring the device without stopping with it, all reloaded by SIGHUP and started
  # again when they stop. chalkos-dhcp starts, reloads and stops them once the VRFs and networkd
  # are up, whenever the output kea or networkd changes, without capabilities, in the group kea,
  # which the diagnostic dhcp-leases runs in. chalkos-dhcp6-routes routes the delegated prefixes
  # with CAP_NET_ADMIN alone, at boot and whenever a record in /var/lib/chalkos-dhcp6-pd changes,
  # which kea writes, readable by every user, in a directory of its own in /var/lib. The
  # firewall reads the subnets for DHCPv6's port; Kea's raw sockets of DHCPv4 come before it.
  # Other images run no Kea.
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
        check = lib.hasInfix "/bin/kea-$family -t" edge.chalkos.outputs.kea.check;
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
            builtins.match "/nix/store/[^ ]*-chalkos-vrf-device kea-dhcp4 %I" vrf.serviceConfig.ExecStartPre
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
              builtins.match "/nix/store/[^ ]*-chalkos-vrf-device kea-dhcp6 %I" vrf6.serviceConfig.ExecStartPre
              != null;
            inherit (vrf6.unitConfig) ConditionPathExists;
          };
        };
        routes = {
          inherit (routes) after wantedBy;
          inherit (routes.unitConfig) StartLimitIntervalSec;
          inherit (routes.serviceConfig) Type RestrictAddressFamilies;
          ip = hasPackage "iproute2" routes.path;
          path = {
            inherit (edge.systemd.paths.chalkos-dhcp6-routes) wantedBy;
            inherit (edge.systemd.paths.chalkos-dhcp6-routes.pathConfig) PathChanged TriggerLimitIntervalSec;
          };
          timer = {
            inherit (edge.systemd.timers.chalkos-dhcp6-routes) wantedBy;
            inherit (edge.systemd.timers.chalkos-dhcp6-routes.timerConfig) OnBootSec OnUnitActiveSec;
          };
          records = lib.all (r: lib.elem r edge.systemd.tmpfiles.rules) [
            "d /var/lib/chalkos-dhcp6-pd 0755 kea kea - -"
            "d /var/lib/chalkos-dhcp6-pd.new 0700 kea kea - -"
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
        ReadWritePaths = [
          "-/var/lib/chalkos-dhcp6-pd"
          "-/var/lib/chalkos-dhcp6-pd.new"
        ];
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
          PathChanged = "/var/lib/chalkos-dhcp6-pd";
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
  # Router images carry Jool's module, loaded at boot, and run chalkos-nat64, which the output jool
  # renders NAT64 instances for and whose reload the effect asks for, after the firewall and
  # networkd and before FRR, with CAP_NET_ADMIN and CAP_SYS_ADMIN and no mount namespace of its
  # own, so the host sees the instances' namespaces. They run Unbound of the config the output
  # unbound renders, once one is rendered, after chalkos-nat64, with CAP_NET_BIND_SERVICE alone and
  # its control socket's directory closed to other users; a changed resolver reloads it with its
  # cache, and one added or removed starts or stops it. The firewall opens the resolver's port and
  # reads the NAT64s, as networkd does, chalkd asks jool in each instance's namespace and
  # unbound-control for their state, the image adds the diagnostic nat64-sessions in the instance's
  # namespace, and the node's own names resolve as before. Each VRF's resolver runs as
  # unbound@<vrf> with the same hardening, bound to the VRF's device with BindNetworkInterface=,
  # whose index it records, requiring the device without stopping with it. chalkos-resolvers
  # starts, reloads and stops them once chalkos-vrf, networkd and the NAT64s are up, whenever the
  # output unbound or networkd changes, without capabilities, devices or write access to the
  # cgroups, in the group unbound; the output reads the VRFs and Networks and checks every config
  # it renders. Other images run neither.
  testRouterNAT64AndResolver = {
    expr =
      let
        nat64 = edge.systemd.services.chalkos-nat64;
        unbound = edge.systemd.services.unbound;
      in
      {
        checks = {
          jool = lib.hasInfix "/bin/jool file check" edge.chalkos.outputs.jool.check;
          unbound = lib.hasInfix "/bin/unbound-checkconf" edge.chalkos.outputs.unbound.check;
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
              builtins.match "/nix/store/[^ ]*-chalkos-vrf-device unbound %I" u.serviceConfig.ExecStartPre
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
        unboundCheck = lib.hasInfix ''for f in "$OUT"/*.conf'' edge.chalkos.outputs.unbound.check;
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
          (mkImages {
            platforms = [ "metal" ];
            variants.plain = {
              kubernetes = false;
              modules = [ { chalkos.forwarding.enable = true; } ];
            };
          }).nixos.plain-metal.config;
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
        # metal's modules of chalkos stay when platforms.metal gives more: they set the consoles.
        console = lib.any (
          d: lib.hasSuffix "modules/platforms/metal.nix" d.file
        ) i.nixos.node-metal.options.chalkos.consoles.definitionsWithLocations;
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
  # mkImages evaluates the images with the nixpkgs and the module arguments given, adds
  # chalkosLib to those arguments and leaves nixpkgs's lib as it is.
  testMkImagesUsesItsNixpkgs = {
    expr =
      let
        n =
          (mkImages {
            nixpkgs.lib = lib // {
              nixosSystem = args: {
                marker = "custom";
                inherit (args) specialArgs;
                passesLib = args ? lib;
              };
            };
            specialArgs.answer = 42;
          }).nixos.node-metal;
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
  # An image's modules get chalkosLib, without mkImages, and nixpkgs's lib, without a chalkos
  # attribute.
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
                mkImages = chalkosLib ? mkImages;
              };
            }
          )
        ]).environment.etc."chalkos-test/lib.json".text;
    expected = {
      libChalkos = false;
      mkOption = true;
      mkImages = false;
    };
  };
  # mkImages refuses a module argument of the user's named chalkosLib.
  testMkImagesRefusesChalkosLibArgument = {
    expr =
      fails
        (mkImages { specialArgs.chalkosLib = removeAttrs flakeLib [ "mkImages" ]; })
        .nixos.node-metal.config.chalkos.variant.name;
    expected = true;
  };
  # The flake's lib holds chalkosLib's helpers besides mkImages, usable outside an image.
  testFlakeLibOutsideAnImage = {
    expr = {
      mkImages = flakeLib ? mkImages;
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
      mkImages = true;
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
      in
      {
        withoutCA = w.environment.etc ? "chalkos/os-ca.crt";
        setting = withCA.nixos.worker-metal.config.chalkos ? osCA;
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
        node = has i.nixos.worker-metal.config;
        installer = has i.nixos.installer.config;
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
          dhcpReadsConfig = lib.hasInfix "chalkcfg get TimeSync" c.systemd.services.chalkos-chrony-dhcp.script;
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
          # chrony's effect, on its output, runs a script of chalkd's PATH, which leaves a chronyd
          # that does not run.
          reloadSources =
            (etcJSON "effects.json" c).chrony == {
              outputs = [ "chrony" ];
              kinds = [ ];
              run = "chalkos-chrony-reload-sources";
              reload = [ ];
              restart = [ ];
              start = false;
              when = "";
              chalkd = "";
              hint = null;
            }
            && hasPackage "chalkos-chrony-reload-sources" c.systemd.services.chalkd.path;
          # chronyd checks the output chrony, whose examples are tests of the image.
          output = {
            check = lib.hasInfix "/bin/chronyd -p -f" c.chalkos.outputs.chrony.check;
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
        image = i.installer.drvPath == config.system.build.chalkosInstaller.drvPath;
      };
    expected = {
      uplink = "52:54:00:12:34:56";
      wireless = true;
      image = true;
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
        metal = summary i.nixos.worker-metal.config;
        kvm = summary i.nixos.worker-kvm.config;
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
        i = mkImages {
          variants.worker = { };
          platforms = {
            metal = [ ];
            kvm = [ ];
            esxi = [ ];
          };
        };
        serialLast = [
          "tty0"
          "ttyS0,115200"
        ];
        consoles = c: lib.filter (lib.hasPrefix "console=") c.boot.kernelParams;
        refused = refuses "boot.kernelParams holds console= entries of its own";
        # The worker of a platform r650 whose own module sets the consoles plainly, with the
        # variant's module given.
        onR650 =
          module:
          (mkImages {
            platforms.r650 = [
              {
                chalkos.consoles = [
                  "ttyS1,115200"
                  "tty0"
                ];
              }
            ];
            variants.worker.modules = [ module ];
          }).nixos.worker-r650.config;
      in
      {
        metal = consoles i.nixos.worker-metal.config;
        kvm = consoles i.nixos.worker-kvm.config;
        esxi = consoles i.nixos.worker-esxi.config;
        installer = consoles i.nixos.installer.config;
        serialLast = consoles (worker [ { chalkos.consoles = serialLast; } ]);
        installerSerialLast = consoles (installer [ { chalkos.consoles = serialLast; } ]);
        stray = refused (worker [ { boot.kernelParams = [ "console=ttyS1,115200" ]; } ]);
        plain = refused w;
        empty = fails (worker [ { chalkos.consoles = [ ]; } ]).chalkos.consoles;
        notADevice = fails (worker [ { chalkos.consoles = [ "ttyS0 quiet" ]; } ]).chalkos.consoles;
        # A platform's and a variant's plain lists conflict; lib.mkForce overrides the platform's.
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
        metal = output i.nixos.worker-metal.config;
        kvm = output i.nixos.worker-kvm.config;
        installer = output i.nixos.installer.config;
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
        unit = i.nixos.worker-metal.config.systemd.services."chalkos-serial-log@";
      in
      {
        metal = copies i.nixos.worker-metal.config;
        kvm = copies i.nixos.worker-kvm.config;
        installer = copies i.nixos.installer.config;
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
          typeZero = lib.hasInfix "grep -qsx 0 /sys/class/tty/%i/type" unit.serviceConfig.ExecCondition;
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
  # from /sbin.
  testGuestAgentRestricted =
    let
      config = workerImages.nixos.worker-kvm.config;
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
        i = workerImages;
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
  # chalkos.kubernetes.version picks the kubelet by its minor, nixpkgs's by default; image.json
  # names the release's version. Compared by derivation, so no kubelet builds.
  testKubeletRelease =
    let
      releases = import ../../modules/node/kubernetes/releases.nix { inherit lib pkgs; };
      image =
        modules:
        let
          c = worker modules;
        in
        {
          inherit (c.chalkos.kubernetes) version;
          package = c.chalkos.kubernetes.package.drvPath;
          imageVersion =
            (builtins.fromJSON (builtins.readFile c.chalkos.imageDirectory.facts.kubernetes)).version;
          kubelet = lib.hasInfix "-kubelet-${c.chalkos.kubernetes.package.version}/bin/kubelet " c.systemd.services.kubelet.serviceConfig.ExecStart;
        };
    in
    {
      expr = {
        default = image [ ];
        older = image [ { chalkos.kubernetes.version = "1.36"; } ];
        releases = lib.attrNames releases;
        unknown = fails (worker [ { chalkos.kubernetes.version = "1.30"; } ]).chalkos.kubernetes.version;
      };
      expected = {
        default = {
          version = lib.versions.majorMinor pkgs.kubernetes.version;
          package = pkgs.kubernetes.drvPath;
          imageVersion = pkgs.kubernetes.version;
          kubelet = true;
        };
        older = {
          version = "1.36";
          package = releases."1.36".drvPath;
          imageVersion = "1.36.6";
          kubelet = true;
        };
        releases = [
          "1.35"
          "1.36"
          (lib.versions.majorMinor pkgs.kubernetes.version)
        ];
        unknown = true;
      };
    };
  # A package of its own stands for a release the table lacks; an image that sets both the
  # version and the package names one minor in both.
  testKubeletPackageAndVersionAgree =
    let
      releases = import ../../modules/node/kubernetes/releases.nix { inherit lib pkgs; };
      refused = modules: refuses "and chalkos.kubernetes.version is 1.36" (worker modules);
    in
    {
      expr = {
        disagree = refused [
          {
            chalkos.kubernetes.version = "1.36";
            chalkos.kubernetes.package = pkgs.kubernetes;
          }
        ];
        packageAlone =
          lib.all (a: a.assertion)
            (worker [ { chalkos.kubernetes.package = releases."1.36"; } ]).assertions;
        agree =
          lib.all (a: a.assertion)
            (worker [
              {
                chalkos.kubernetes.version = lib.versions.majorMinor pkgs.kubernetes.version;
                chalkos.kubernetes.package = pkgs.kubernetes;
              }
            ]).assertions;
      };
      expected = {
        disagree = true;
        packageAlone = true;
        agree = true;
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
        # directory.
        kubeletEtcdMount = config.systemd.services.kubelet.unitConfig.RequiresMountsFor;
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
        c = w;
        gpu = worker [
          {
            chalkos.kernel.moduleGroups = [ "gpu" ];
            chalkos.kernel.extraModules = [ "kvm_amd" ];
          }
        ];
        all = worker [ { chalkos.kernel.allModules = true; } ];
        tree = c: c.system.build.chalkosKernelModules;
        kernelModules = c: "${lib.getOutput "modules" c.boot.kernelPackages.kernel}";
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
        # The image's module tree is made of the filtered tree alone, not of the kernel's.
        filtered = (tree c) ? directories && map toString c.system.modulesTree.paths == [ "${tree c}" ];
        all = "${tree all}" == kernelModules all;
        loadsChecked = lib.all (m: lib.elem m loaded) (
          c.boot.kernelModules ++ c.boot.initrd.kernelModules ++ c.boot.initrd.availableKernelModules
        );
      };
    expected = {
      groups = [
        "filesystems"
        "firewall"
        "kubernetes"
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
      filtered = true;
      all = true;
      loadsChecked = true;
    };
  };
  # The groups of kernel modules are declared by the modules that need them: every image carries
  # the base firewall's, a router too, whose rulesets use its modules with the nftables service
  # off, and the Kubernetes layer adds its own. Every image declares the Kubernetes group, so a
  # variant may add it; a group no module declares is refused.
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
        node = lib.sort lib.lessThan (base ++ [ "kubernetes" ]);
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
  # Mirrors come from the Mirror and Registry documents, written to /run before containerd starts.
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
          version = pkgs.kubernetes.version;
          cni = "flannel";
        };
        noFlannel = {
          version = pkgs.kubernetes.version;
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
        "umask=0077"
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
        "chalkos-chrony-reload-sources"
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

  # A diagnostic chalkd could not run fails the image's build, with a message that names its
  # option: a name not of [a-z][a-z0-9-]*, a program outside the store, a placeholder without its
  # parameter or a parameter without its placeholder, a parameter both optional and with a
  # default, a group not of [a-z_][a-z0-9_-]* or one the image lacks, a name longer than 64
  # characters, and a timeout longer than 10m.
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
        names = lib.attrNames node;
        installer = lib.attrNames inInstaller;
        inStore = lib.all (d: lib.hasPrefix "/nix/store/" (lib.head d.command)) (lib.attrValues node);
        ip = lib.hasSuffix "-iproute2-ip-${pkgs.iproute2.version}/bin/ip" (lib.head node.route-get.command);
        tcpConnect = {
          program = lib.head node.tcp-connect.command;
          arguments = lib.drop 3 node.tcp-connect.command;
          # bash runs timeout itself, so it can say when the connect timed out.
          timeout = lib.hasInfix (builtins.unsafeDiscardStringContext "${pkgs.coreutils}/bin/timeout 5 ") (
            lib.elemAt node.tcp-connect.command 2
          );
        };
      };
      expected = {
        inherit names;
        installer = names;
        inStore = true;
        ip = true;
        # After bash, -c and its script: $0 and the arguments.
        tcpConnect = {
          program = "${pkgs.bash}/bin/bash";
          arguments = [
            "tcp-connect"
            "{address}"
            "{port}"
          ];
          timeout = true;
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
  # A serial port's getty runs only where the port exists and is a UART of a known type, which
  # ends its restarts on a machine without one: the 8250 driver registers ttyS0 on x86 with type 0
  # when no UART answers. The conditions are in the template's drop-in, next to agetty's
  # ExecStart, and no instance has a drop-in of its own, whose file of the same name would hide
  # the template's and leave the upstream unit's ExecStart, a path that does not exist.
  testSerialGettyNeedsThePort = {
    expr =
      let
        c = w;
        unit = c.systemd.services."serial-getty@";
      in
      {
        condition = unit.unitConfig.ConditionPathExists;
        knownType =
          builtins.match ".*/bin/[a-z]+ -c '! /nix/store/[^ ]*/bin/grep -qsx 0 /sys/class/tty/%I/type'" unit.serviceConfig.ExecCondition
          != null;
        agetty = lib.hasInfix "/bin/agetty " c.systemd.units."serial-getty@.service".text;
        instances = lib.filter (lib.hasPrefix "serial-getty@tty") (lib.attrNames c.systemd.units);
      };
    expected = {
      condition = "/sys/class/tty/%I";
      knownType = true;
      agetty = true;
      instances = [ ];
    };
  };
  # An image's own overlay can override chalkos's packages with overrideScope: mkImages adds the
  # chalkos scope before the image's overlays, so a variant's chalkd replaces the one on the
  # system path, which is otherwise the scope's.
  testImageOverlayOverridesTheChalkosScope = {
    expr =
      let
        onPath = name: c: lib.any (p: lib.getName p == name) c.environment.systemPackages;
        images =
          (mkImages {
            platforms = [ "metal" ];
            variants = {
              node = { };
              custom.modules = [
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
            };
          }).nixos;
      in
      {
        scope = lib.elem images.node-metal.pkgs.chalkos.chalkd images.node-metal.config.environment.systemPackages;
        custom = onPath "chalkd-custom" images.custom-metal.config;
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
                    hardened = routerLib.hardened.NoNewPrivileges;
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
        hardened = true;
        types = true;
        feature = true;
        declared = [ "NAT64" ];
      };
      worker = true;
    };
  };
}
