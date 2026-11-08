# Evaluation tests of chalkos.lib.mkImages and the images it builds; returns lib.runTests failures
# (empty on success).
{
  lib,
  pkgs,
  mkImages,
}:
let
  inherit (import ./lib.nix { inherit mkImages; }) fails worker;
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
  # A router image on metal without the router features named, whose modules set their
  # chalkos.router.<feature>.enable to false.
  routerWithout =
    features:
    (mkImages {
      platforms = [ "metal" ];
      variants.edge = {
        kubernetes = false;
        router = true;
        modules = [
          {
            chalkos.router = lib.genAttrs features (_: {
              enable = false;
            });
          }
        ];
      };
    }).nixos.edge-metal.config;
  # The example files a router image's outputs render at its build.
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
    expr = map (c: lib.any (p: lib.getName p == "chalkd") c.environment.systemPackages) [
      (worker [ ])
      layered.plain-metal.config
      layered.edge-metal.config
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
        edge = markers layered.edge-metal.config;
        installer = markers (mkImages { }).nixos.installer.config;
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
        message = "the variant both enables the kubernetes and router layers; an image carries one layer at most, so set kubernetes = false or router = false";
        refused = c: lib.any (a: !a.assertion && a.message == message) c.assertions;
        both = (mkImages { variants.both.router = true; }).nixos.both-metal.config;
      in
      {
        both = refused both;
        node = refused layered.node-metal.config;
        plain = refused layered.plain-metal.config;
        edge = refused layered.edge-metal.config;
      };
    expected = {
      both = true;
      node = false;
      plain = false;
      edge = false;
    };
  };
  # Router images take DHCPSubnet documents and run Kea unless their modules set
  # chalkos.router.dhcp.enable to false, whose router outputs then render no example of DHCP at the
  # build, and which carry no output kea or effect dhcp, no Kea, its units, the routes' path unit,
  # user or package, and no diagnostic dhcp-leases; other images never take them.
  testRouterDHCPOptional = {
    expr =
      let
        kinds = c: c.chalkos.kinds ? DHCPSubnet;
        example = c: builtins.pathExists "${c.chalkos.router.examples}/dhcp.yaml";
        # What of Kea the image c carries.
        kea = c: {
          output = c.chalkos.outputs ? kea;
          effect = c.chalkos.effects ? dhcp;
          units = lib.filter (
            n: lib.hasPrefix "kea" n || n == "chalkos-dhcp" || n == "chalkos-dhcp6-routes"
          ) (lib.attrNames c.systemd.services);
          path = c.systemd.paths ? chalkos-dhcp6-routes;
          user = c.users.users ? kea;
          # A pinned ID, so that the user moves no other user's ID.
          ids =
            if c.users.users ? kea then
              [
                c.users.users.kea.uid
                c.users.groups.kea.gid
              ]
            else
              null;
          package = lib.any (p: lib.hasPrefix "kea" (lib.getName p)) c.environment.systemPackages;
          # A unit that runs one of Kea's programs.
          referenced = lib.any (u: lib.hasInfix "-kea-dhcp-" (u.text or "")) (lib.attrValues c.systemd.units);
          diagnostic = c.chalkos.diagnostics ? dhcp-leases;
        };
        without =
          (mkImages {
            platforms = [ "metal" ];
            variants.edge = {
              kubernetes = false;
              router = true;
              modules = [ { chalkos.router.dhcp.enable = false; } ];
            };
          }).nixos.edge-metal.config;
      in
      {
        edge = kinds layered.edge-metal.config;
        without = kinds without;
        node = kinds layered.node-metal.config;
        plain = kinds layered.plain-metal.config;
        examples = {
          edge = example layered.edge-metal.config;
          without = example without;
        };
        kea = {
          edge = kea layered.edge-metal.config;
          without = kea without;
        };
      };
    expected = {
      edge = true;
      without = false;
      node = false;
      plain = false;
      examples = {
        edge = true;
        without = false;
      };
      kea = {
        edge = {
          output = true;
          effect = true;
          units = [
            "chalkos-dhcp"
            "chalkos-dhcp6-routes"
            "kea-dhcp4"
            "kea-dhcp4@"
            "kea-dhcp6"
            "kea-dhcp6@"
          ];
          path = true;
          user = true;
          ids = [
            328
            328
          ];
          package = false;
          referenced = true;
          diagnostic = true;
        };
        without = {
          output = false;
          effect = false;
          units = [ ];
          path = false;
          user = false;
          ids = null;
          package = false;
          referenced = false;
          diagnostic = false;
        };
      };
    };
  };
  # Router images take DHCPRelay documents and run dnsmasq's relays unless their modules set
  # chalkos.router.dhcpRelay.enable to false, whose router outputs then render no example of a
  # relay at the build, and which carry no output dhcp-relay or effect dhcp-relay, no dnsmasq, its
  # units or user; other images never take them.
  testRouterDHCPRelayOptional = {
    expr =
      let
        relay = c: {
          kind = c.chalkos.kinds ? DHCPRelay;
          example = builtins.pathExists "${c.chalkos.router.examples}/dhcp-relay.yaml";
          output = c.chalkos.outputs ? dhcp-relay;
          effect = c.chalkos.effects ? dhcp-relay;
          units = lib.filter (n: lib.hasPrefix "dnsmasq" n || n == "chalkos-dhcp-relays") (
            lib.attrNames c.systemd.services
          );
          ids =
            if c.users.users ? dnsmasq then
              [
                c.users.users.dnsmasq.uid
                c.users.groups.dnsmasq.gid
              ]
            else
              null;
          referenced = lib.any (u: lib.hasInfix "-dnsmasq-" (u.text or "")) (lib.attrValues c.systemd.units);
          firewallReads = lib.elem "DHCPRelay" c.chalkos.outputs.router-nftables.inputs;
        };
      in
      {
        edge = relay layered.edge-metal.config;
        without = relay (routerWithout [ "dhcpRelay" ]);
        node = layered.node-metal.config.chalkos.kinds ? DHCPRelay;
        plain = layered.plain-metal.config.chalkos.kinds ? DHCPRelay;
      };
    expected = {
      edge = {
        kind = true;
        example = true;
        output = true;
        effect = true;
        units = [
          "chalkos-dhcp-relays"
          "dnsmasq-relay"
          "dnsmasq-relay@"
        ];
        ids = [
          332
          332
        ];
        referenced = true;
        firewallReads = true;
      };
      without = {
        kind = false;
        example = false;
        output = false;
        effect = false;
        units = [ ];
        ids = null;
        referenced = false;
        firewallReads = false;
      };
      node = false;
      plain = false;
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
        edge = layered.edge-metal.config;
        relay = edge.systemd.services.dnsmasq-relay;
        vrf = edge.systemd.services."dnsmasq-relay@";
        relays = edge.systemd.services.chalkos-dhcp-relays;
      in
      {
        inputs = edge.chalkos.outputs.dhcp-relay.inputs;
        render = lib.hasSuffix "/bin/chalkos-render dhcp-relay" edge.chalkos.outputs.dhcp-relay.render;
        check = lib.hasInfix "/bin/dnsmasq --test" edge.chalkos.outputs.dhcp-relay.check;
        relay = {
          start =
            builtins.match "/nix/store/[^ ]*-dnsmasq-2.93/bin/dnsmasq --keep-in-foreground --log-facility=- --pid-file= --conf-file=/run/chalkos/outputs/dhcp-relay/dnsmasq-relay.conf" relay.serviceConfig.ExecStart
            != null;
          inherit (relay.serviceConfig)
            User
            AmbientCapabilities
            CapabilityBoundingSet
            RestrictAddressFamilies
            ;
          inherit (relay.unitConfig) ConditionPathExists;
          wanted = relay.wantedBy;
        };
        vrfRelay = {
          start = lib.hasSuffix "--conf-file=/run/chalkos/outputs/dhcp-relay/dnsmasq-relay-%I.conf" vrf.serviceConfig.ExecStart;
          inherit (vrf.serviceConfig) BindNetworkInterface;
          inherit (vrf.unitConfig) Requires;
          records = lib.hasInfix "dnsmasq-relay-vrf-device" vrf.serviceConfig.ExecStartPre;
        };
        reconciler = {
          inherit (relays) wantedBy;
          inherit (relays.serviceConfig) Type CapabilityBoundingSet;
          reloads = relays.serviceConfig.ExecReload == relays.serviceConfig.ExecStart;
          stops = lib.hasSuffix " stop" relays.serviceConfig.ExecStop;
          effect = {
            inherit (edge.chalkos.effects.dhcp-relay) outputs reload;
          };
        };
      };
    expected = {
      inputs = [
        "DHCPRelay"
        "VRF"
        "Network"
        "DHCPSubnet"
        "NAT64"
      ];
      render = true;
      check = true;
      relay = {
        start = true;
        User = "dnsmasq";
        AmbientCapabilities = [
          "CAP_NET_BIND_SERVICE"
          "CAP_NET_RAW"
          "CAP_NET_ADMIN"
        ];
        CapabilityBoundingSet = [
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
        start = true;
        BindNetworkInterface = "%I";
        Requires = "sys-subsystem-net-devices-%i.device";
        records = true;
      };
      reconciler = {
        wantedBy = [ "multi-user.target" ];
        Type = "oneshot";
        CapabilityBoundingSet = [ "" ];
        reloads = true;
        stops = true;
        effect = {
          outputs = [
            "dhcp-relay"
            "networkd"
          ];
          reload = [ "chalkos-dhcp-relays.service" ];
        };
      };
    };
  };
  # The router outputs render, at the image's build, the examples all of whose documents are of
  # kinds the image accepts: every one on a router image with every feature, those without
  # DHCPSubnet documents on one without DHCP, and those without DHCPRelay documents on one without
  # the relay.
  testRouterExamples = {
    expr = {
      edge = examplesOf layered.edge-metal.config;
      withoutDHCP = examplesOf (routerWithout [ "dhcp" ]);
      withoutDHCPRelay = examplesOf (routerWithout [ "dhcpRelay" ]);
    };
    expected = {
      edge = [
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
      withoutDHCP = [
        "dhcp-relay.yaml"
        "edge.yaml"
        "lan.yaml"
        "leaf.yaml"
        "nat64.yaml"
        "none.yaml"
        "policy.yaml"
        "vrf.yaml"
      ];
      withoutDHCPRelay = [
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
  };
  # The users and groups of the router's features have pinned IDs, free in NixOS's ids.nix, so an
  # image that adds or drops a feature moves no other user's or group's ID; the build allocates
  # IDs to the router image's other users and groups alone.
  testRouterUserIDs = {
    expr =
      let
        c = layered.edge-metal.config;
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
  # Router images run FRR and take the BGP kinds unless their modules set
  # chalkos.router.bgp.enable to false, whose images carry no FRR: no BGP kinds, output frr,
  # effect frr, frr.service, FRR's users, groups, package or config, no vtysh on chalkd's PATH,
  # no unit that runs FRR's programs and no BGP or BFD diagnostics; their firewall reads no
  # BGPPeer, and their outputs render no example with BGP at the build but the NAT64 one.
  # networkd keeps the routes others install either way, and the build allocates the same IDs.
  testRouterBGPOptional = {
    expr =
      let
        of = c: {
          kinds = lib.filter (k: c.chalkos.kinds ? ${k}) [
            "BGP"
            "BGPPeer"
            "PrefixList"
            "CommunityList"
            "RoutingPolicy"
          ];
          output = c.chalkos.outputs ? frr;
          effect = c.chalkos.effects ? frr;
          unit = c.systemd.services ? frr;
          users = lib.filter (n: c.users.users ? ${n}) [ "frr" ];
          groups = lib.filter (n: c.users.groups ? ${n}) [
            "frr"
            "frrvty"
          ];
          package = lib.any (p: lib.getName p == "frr") c.environment.systemPackages;
          chalkd = lib.any (p: lib.getName p == "frr") c.systemd.services.chalkd.path;
          config = c.environment.etc ? "frr/frr.conf";
          # A unit that runs one of FRR's programs.
          referenced = lib.any (u: lib.hasInfix "-frr-" (u.text or "")) (lib.attrValues c.systemd.units);
          diagnostics = lib.filter (n: lib.hasPrefix "bgp-" n || lib.hasPrefix "bfd-" n) (
            lib.attrNames c.chalkos.diagnostics
          );
          firewallReads = lib.elem "BGPPeer" c.chalkos.outputs.router-nftables.inputs;
          examples = lib.filter (lib.flip lib.elem [
            "edge.yaml"
            "leaf.yaml"
            "vrf.yaml"
          ]) (examplesOf c);
          nat64Example = lib.elem "nat64.yaml" (examplesOf c);
          foreignRoutes = c.systemd.network.config.networkConfig;
          unpinned = unpinned c;
        };
      in
      {
        edge = of layered.edge-metal.config;
        without = of (routerWithout [ "bgp" ]);
      };
    expected =
      let
        foreignRoutes = {
          ManageForeignNextHops = false;
          ManageForeignRoutes = false;
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
      in
      {
        edge = {
          kinds = [
            "BGP"
            "BGPPeer"
            "PrefixList"
            "CommunityList"
            "RoutingPolicy"
          ];
          output = true;
          effect = true;
          unit = true;
          users = [ "frr" ];
          groups = [
            "frr"
            "frrvty"
          ];
          package = true;
          chalkd = true;
          config = true;
          referenced = true;
          diagnostics = [
            "bfd-peers"
            "bgp-neighbor"
            "bgp-route"
            "bgp-summary"
          ];
          firewallReads = true;
          examples = [
            "edge.yaml"
            "leaf.yaml"
            "vrf.yaml"
          ];
          nat64Example = true;
          inherit foreignRoutes unpinned;
        };
        without = {
          kinds = [ ];
          output = false;
          effect = false;
          unit = false;
          users = [ ];
          groups = [ ];
          package = false;
          chalkd = false;
          config = false;
          referenced = false;
          diagnostics = [ ];
          firewallReads = false;
          examples = [ ];
          nat64Example = true;
          inherit foreignRoutes unpinned;
        };
      };
  };
  # Router images run Jool and take NAT64 documents unless their modules set
  # chalkos.router.nat64.enable to false, whose images carry no Jool: no kind NAT64, output jool,
  # effect jool, chalkos-nat64.service, kernel module or jool on chalkd's PATH, no unit that runs
  # jool and no diagnostic nat64-sessions; no output reads NAT64 documents, and their outputs
  # render no example with NAT64 at the build. The build allocates the same IDs.
  testRouterNAT64Optional = {
    expr =
      let
        of = c: {
          kind = c.chalkos.kinds ? NAT64;
          output = c.chalkos.outputs ? jool;
          effect = c.chalkos.effects ? jool;
          unit = c.systemd.services ? chalkos-nat64;
          module = map lib.getName c.boot.extraModulePackages;
          loaded = lib.elem "jool" c.boot.kernelModules;
          chalkd = lib.any (p: lib.getName p == "jool-cli") c.systemd.services.chalkd.path;
          # A unit that runs jool.
          referenced = lib.any (u: lib.hasInfix "-jool-" (u.text or "")) (lib.attrValues c.systemd.units);
          diagnostic = c.chalkos.diagnostics ? nat64-sessions;
          reads = lib.filter (o: lib.elem "NAT64" c.chalkos.outputs.${o}.inputs) (
            lib.attrNames c.chalkos.outputs
          );
          examples = lib.filter (lib.flip lib.elem [
            "edge.yaml"
            "nat64.yaml"
            "vrf.yaml"
          ]) (examplesOf c);
          unpinned = unpinned c;
        };
      in
      {
        edge = of layered.edge-metal.config;
        without = of (routerWithout [ "nat64" ]);
      };
    expected =
      let
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
      in
      {
        edge = {
          kind = true;
          output = true;
          effect = true;
          unit = true;
          module = [ "jool" ];
          loaded = true;
          chalkd = true;
          referenced = true;
          diagnostic = true;
          reads = [
            "dhcp-relay"
            "jool"
            "kea"
            "networkd"
            "router-nftables"
          ];
          examples = [
            "edge.yaml"
            "nat64.yaml"
            "vrf.yaml"
          ];
          inherit unpinned;
        };
        without = {
          kind = false;
          output = false;
          effect = false;
          unit = false;
          module = [ ];
          loaded = false;
          chalkd = false;
          referenced = false;
          diagnostic = false;
          reads = [ ];
          examples = [ ];
          inherit unpinned;
        };
      };
  };
  # Router images run Unbound and take Resolver documents unless their modules set
  # chalkos.router.resolver.enable to false, whose images carry no Unbound: no kind Resolver,
  # output unbound, effects unbound, unbound-start and resolvers, units unbound, unbound@ and
  # chalkos-resolvers, Unbound's user, group, package or config and no unbound-control on
  # chalkd's PATH; their firewall reads no Resolver, and their outputs render no example with a
  # Resolver at the build but the NAT64 one. The build allocates the same IDs.
  testRouterResolverOptional = {
    expr =
      let
        of = c: {
          kind = c.chalkos.kinds ? Resolver;
          output = c.chalkos.outputs ? unbound;
          effects = lib.filter (e: c.chalkos.effects ? ${e}) [
            "resolvers"
            "unbound"
            "unbound-start"
          ];
          units = lib.filter (u: c.systemd.services ? ${u}) [
            "chalkos-resolvers"
            "unbound"
            "unbound@"
          ];
          users = lib.filter (n: c.users.users ? ${n}) [ "unbound" ];
          groups = lib.filter (n: c.users.groups ? ${n}) [ "unbound" ];
          package = lib.any (p: lib.getName p == "unbound") c.environment.systemPackages;
          chalkd = lib.any (p: lib.getName p == "unbound") c.systemd.services.chalkd.path;
          config = c.environment.etc ? "unbound/unbound.conf";
          firewallReads = lib.elem "Resolver" c.chalkos.outputs.router-nftables.inputs;
          examples = lib.filter (lib.flip lib.elem [
            "edge.yaml"
            "lan.yaml"
            "vrf.yaml"
          ]) (examplesOf c);
          nat64Example = lib.elem "nat64.yaml" (examplesOf c);
          unpinned = unpinned c;
        };
      in
      {
        edge = of layered.edge-metal.config;
        without = of (routerWithout [ "resolver" ]);
      };
    expected =
      let
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
      in
      {
        edge = {
          kind = true;
          output = true;
          effects = [
            "resolvers"
            "unbound"
            "unbound-start"
          ];
          units = [
            "chalkos-resolvers"
            "unbound"
            "unbound@"
          ];
          users = [ "unbound" ];
          groups = [ "unbound" ];
          package = true;
          chalkd = true;
          config = true;
          firewallReads = true;
          examples = [
            "edge.yaml"
            "lan.yaml"
            "vrf.yaml"
          ];
          nat64Example = true;
          inherit unpinned;
        };
        without = {
          kind = false;
          output = false;
          effects = [ ];
          units = [ ];
          users = [ ];
          groups = [ ];
          package = false;
          chalkd = false;
          config = false;
          firewallReads = false;
          examples = [ ];
          nat64Example = true;
          inherit unpinned;
        };
      };
  };
  # A router image without BGP, NAT64, the resolver, DHCP and the DHCP relay keeps the router's
  # core: the router kinds of the firewall, VRFs and policy routing, the outputs and units of the
  # firewall, the kernel settings and the VRFs, and the diagnostics of every image and traceroute.
  # Its outputs render the examples of none of the five, and its build allocates the IDs of the
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
        same = unpinned c == unpinned layered.edge-metal.config;
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
        "networkd"
        "router-nftables"
        "router-sysctl"
        "vrf"
      ];
      units = [
        "chalkos-chrony-dhcp"
        "chalkos-chrony-dhcp-links"
        "chalkos-config"
        "chalkos-firewall"
        "chalkos-health"
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
        edge = layered.edge-metal.config;
        unit = edge.systemd.services.chalkos-firewall;
      in
      {
        edge = of edge;
        plain = of layered.plain-metal.config;
        node = of layered.node-metal.config;
        after = lib.elem "chalkos-config.service" unit.after;
        before = lib.elem "network-pre.target" unit.before;
        sysctlDir = edge.chalkos.outputs.router-sysctl.dir;
        sysctlAfter = edge.systemd.services.systemd-sysctl.after;
        effects = {
          inherit (edge.chalkos.effects.router-sysctl) restart;
          inherit (edge.chalkos.effects.router-nftables) reload;
        };
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
      sysctlDir = "/run/sysctl.d";
      sysctlAfter = [ "chalkos-config.service" ];
      effects = {
        restart = [ "systemd-sysctl.service" ];
        reload = [ "chalkos-firewall.service" ];
      };
      missingModules = [ ];
    };
  };
  # A router image ignores NixOS's firewall, so it warns of each of its options a module set
  # besides chalkd's port, which every image opens.
  testRouterWarnsOfNixosFirewall = {
    expr =
      let
        warned =
          modules:
          lib.filter (lib.hasInfix "networking.firewall") (
            (mkImages {
              platforms = [ "metal" ];
              variants.edge = {
                kubernetes = false;
                router = true;
                inherit modules;
              };
            }).nixos.edge-metal.config.warnings
          );
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
  # Router images add traceroute to every image's diagnostics; it sends traffic, so it takes an
  # operator.
  testRouterDiagnostics = {
    expr = {
      edge = layered.edge-metal.config.chalkos.diagnostics ? traceroute;
      plain = layered.plain-metal.config.chalkos.diagnostics ? traceroute;
      role = layered.edge-metal.config.chalkos.diagnostics.traceroute.role or null;
      arguments = lib.tail (layered.edge-metal.config.chalkos.diagnostics.traceroute.command or [ null ]);
    };
    expected = {
      edge = true;
      plain = false;
      role = "operator";
      arguments = [
        "-n"
        "-m"
        "30"
        "-q"
        "1"
        "-w"
        "2"
        "{address}"
      ];
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
        edge = layered.edge-metal.config;
        frr = edge.systemd.services.frr;
      in
      {
        daemons = {
          inherit (edge.services.frr.bgpd) enable;
          bfdd = edge.services.frr.bfdd.enable;
        };
        inputs = edge.chalkos.outputs.frr.inputs;
        firewallReads = lib.elem "BGPPeer" edge.chalkos.outputs.router-nftables.inputs;
        check = lib.hasInfix "vtysh --dryrun" edge.chalkos.outputs.frr.check;
        effect = edge.chalkos.effects.frr.reload;
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
        vtysh = lib.any (p: lib.getName p == "frr") edge.systemd.services.chalkd.path;
        networkd = edge.systemd.network.config.networkConfig;
        diagnostics = lib.filter (n: lib.hasPrefix "bgp-" n || lib.hasPrefix "bfd-" n) (
          lib.attrNames edge.chalkos.diagnostics
        );
        groups = edge.chalkos.diagnostics.bgp-summary.groups;
        plain = layered.plain-metal.config.services.frr.bgpd.enable;
        node = layered.node-metal.config.chalkos.outputs ? frr;
      };
    expected = {
      daemons = {
        enable = true;
        bfdd = true;
      };
      inputs = [
        "BGP"
        "BGPPeer"
        "CommunityList"
        "PrefixList"
        "RoutingPolicy"
        "Network"
        "VRF"
      ];
      firewallReads = true;
      check = true;
      effect = [ "frr.service" ];
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
      vtysh = true;
      networkd = {
        ManageForeignNextHops = false;
        ManageForeignRoutes = false;
      };
      diagnostics = [
        "bfd-peers"
        "bgp-neighbor"
        "bgp-route"
        "bgp-summary"
      ];
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
        edge = layered.edge-metal.config;
        vrf = edge.systemd.services.chalkos-vrf;
      in
      {
        kind = edge.chalkos.kinds ? VRF;
        inputs = edge.chalkos.outputs.networkd.inputs;
        render = lib.hasInfix " router-networkd " edge.chalkos.outputs.networkd.render;
        plainRender = lib.hasInfix " networkd " layered.plain-metal.config.chalkos.outputs.networkd.render;
        plainKind = layered.plain-metal.config.chalkos.kinds ? VRF;
        effect = {
          inherit (edge.chalkos.effects.vrf) outputs reload;
        };
        after = vrf.after;
        before = vrf.before;
        defaults = vrf.unitConfig.DefaultDependencies;
        wantedBy = vrf.wantedBy;
        reloads = vrf.serviceConfig.ExecReload == vrf.serviceConfig.ExecStart;
        capabilities = vrf.serviceConfig.CapabilityBoundingSet;
        frrAfter = lib.elem "systemd-networkd.service" edge.systemd.services.frr.after;
        firewallInputs = lib.filter (
          k: k == "VRF" || k == "Network"
        ) edge.chalkos.outputs.router-nftables.inputs;
        commands = map (d: lib.last edge.chalkos.diagnostics.${d}.command) [
          "bgp-summary"
          "bgp-neighbor"
          "bgp-route"
          "bfd-peers"
        ];
        parameter = {
          inherit (edge.chalkos.diagnostics.bgp-summary.parameters.vrf) type default;
        };
      };
    expected = {
      kind = true;
      inputs = [
        "Link"
        "NetDev"
        "Network"
        "NAT64"
        "VRF"
        "NextHop"
        "PolicyRoute"
      ];
      render = true;
      plainRender = true;
      plainKind = false;
      effect = {
        outputs = [ "networkd" ];
        reload = [ "chalkos-vrf.service" ];
      };
      after = [ "chalkos-config.service" ];
      before = [ "systemd-networkd.service" ];
      defaults = false;
      wantedBy = [ "multi-user.target" ];
      reloads = true;
      capabilities = [ "CAP_NET_ADMIN" ];
      frrAfter = true;
      firewallInputs = [
        "VRF"
        "Network"
      ];
      commands = [
        "show bgp vrf {vrf} summary"
        "show bgp vrf {vrf} neighbors {peer}"
        "show bgp vrf {vrf} {family} unicast {prefix}"
        "show bfd vrf {vrf} peers"
      ];
      parameter = {
        type = "pattern";
        default = "default";
      };
    };
  };
  # Router images accept PolicyRoute and NextHop documents, which both the firewall, which marks
  # the packets they match, and networkd, which keeps the kernel's rules and the next hops' tables,
  # read; other images have no such kinds.
  testRouterPolicyRouting = {
    expr =
      let
        edge = layered.edge-metal.config;
        kinds = [
          "PolicyRoute"
          "NextHop"
        ];
      in
      {
        kinds = map (k: edge.chalkos.kinds ? ${k}) kinds;
        plainKinds = map (k: layered.plain-metal.config.chalkos.kinds ? ${k}) kinds;
        firewall = map (k: lib.elem k edge.chalkos.outputs.router-nftables.inputs) kinds;
        networkd = map (k: lib.elem k edge.chalkos.outputs.networkd.inputs) kinds;
      };
    expected = {
      kinds = [
        true
        true
      ];
      plainKinds = [
        false
        false
      ];
      firewall = [
        true
        true
      ];
      networkd = [
        true
        true
      ];
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
        edge = layered.edge-metal.config;
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
        inputs = edge.chalkos.outputs.kea.inputs;
        render =
          builtins.match ".*/bin/chalkos-render kea /nix/store/[^ ]*-chalkos-dhcp6-pd/bin/chalkos-dhcp6-pd" edge.chalkos.outputs.kea.render
          != null;
        check = lib.hasInfix "/bin/kea-$family -t" edge.chalkos.outputs.kea.check;
        package = lib.hasSuffix "-kea-dhcp-3.2.0" kea;
        hooks = builtins.attrNames (builtins.readDir "${kea}/lib/kea/hooks");
        firewallReads = lib.elem "DHCPSubnet" edge.chalkos.outputs.router-nftables.inputs;
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
            lib.hasInfix "kea-vrf-device" vrf.serviceConfig.ExecStartPre
            && lib.hasSuffix " dhcp4 %I" vrf.serviceConfig.ExecStartPre;
        };
        server6 = {
          start = lib.hasSuffix "/bin/kea-dhcp6 -c /run/chalkos/outputs/kea/kea-dhcp6.json" server6.serviceConfig.ExecStart;
          inherit (server6.serviceConfig)
            User
            AmbientCapabilities
            CapabilityBoundingSet
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
            records = lib.hasSuffix " dhcp6 %I" vrf6.serviceConfig.ExecStartPre;
            inherit (vrf6.unitConfig) ConditionPathExists;
          };
        };
        routes = {
          start = lib.hasSuffix "/bin/chalkd router dhcp6-routes" routes.serviceConfig.ExecStart;
          inherit (routes) after wantedBy;
          inherit (routes.unitConfig) StartLimitIntervalSec;
          inherit (routes.serviceConfig)
            Type
            CapabilityBoundingSet
            RestrictAddressFamilies
            TimeoutStartSec
            ;
          ip = lib.any (p: lib.getName p == "iproute2") routes.path;
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
          reloads = dhcp.serviceConfig.ExecReload == dhcp.serviceConfig.ExecStart;
          stops = lib.hasSuffix " stop" dhcp.serviceConfig.ExecStop;
          effect = {
            inherit (edge.chalkos.effects.dhcp) outputs reload;
          };
        };
        diagnostic = {
          inherit (edge.chalkos.diagnostics.dhcp-leases) role groups;
        };
        plain = layered.plain-metal.config.chalkos.outputs ? kea;
      };
    expected = {
      kind = true;
      plainKind = false;
      inputs = [
        "DHCPSubnet"
        "VRF"
        "Network"
        "NAT64"
      ];
      render = true;
      check = true;
      package = true;
      hooks = [
        "libdhcp_lease_cmds.so"
        "libdhcp_run_script.so"
      ];
      firewallReads = true;
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
        CapabilityBoundingSet = [ "CAP_NET_BIND_SERVICE" ];
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
        start = true;
        StartLimitIntervalSec = 0;
        after = [ "chalkos-dhcp.service" ];
        wantedBy = [ "multi-user.target" ];
        Type = "oneshot";
        CapabilityBoundingSet = [ "CAP_NET_ADMIN" ];
        RestrictAddressFamilies = [
          "AF_UNIX"
          "AF_NETLINK"
        ];
        TimeoutStartSec = "30s";
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
        reloads = true;
        stops = true;
        effect = {
          outputs = [
            "kea"
            "networkd"
          ];
          reload = [ "chalkos-dhcp.service" ];
        };
      };
      diagnostic = {
        role = "operator";
        groups = [ "kea" ];
      };
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
        edge = layered.edge-metal.config;
        nat64 = edge.systemd.services.chalkos-nat64;
        unbound = edge.systemd.services.unbound;
      in
      {
        module = map lib.getName edge.boot.extraModulePackages;
        loaded = lib.elem "jool" edge.boot.kernelModules;
        inputs = {
          jool = edge.chalkos.outputs.jool.inputs;
          unbound = edge.chalkos.outputs.unbound.inputs;
        };
        firewallReads = lib.all (k: lib.elem k edge.chalkos.outputs.router-nftables.inputs) [
          "Resolver"
          "NAT64"
        ];
        networkdReads = lib.elem "NAT64" edge.chalkos.outputs.networkd.inputs;
        checks = {
          jool = lib.hasInfix "/bin/jool file check" edge.chalkos.outputs.jool.check;
          unbound = lib.hasInfix "/bin/unbound-checkconf" edge.chalkos.outputs.unbound.check;
        };
        effects = {
          jool = edge.chalkos.effects.jool.reload;
          unbound = edge.chalkos.effects.unbound.reload;
          start = {
            inherit (edge.chalkos.effects.unbound-start) restart start;
          };
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
        diagnostic = {
          inherit (edge.chalkos.diagnostics.nat64-sessions) role capabilities networkNamespace;
        };
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
              lib.hasInfix "unbound-vrf-device" u.serviceConfig.ExecStartPre
              && lib.hasSuffix " %I" u.serviceConfig.ExecStartPre;
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
            reloads = r.serviceConfig.ExecReload == r.serviceConfig.ExecStart;
            stops = lib.hasSuffix " stop" r.serviceConfig.ExecStop;
            effect = {
              inherit (edge.chalkos.effects.resolvers) outputs reload;
            };
          };
        unboundCheck = lib.hasInfix ''for f in "$OUT"/*.conf'' edge.chalkos.outputs.unbound.check;
        plain = {
          unbound = layered.plain-metal.config.services.unbound.enable;
          jool = layered.plain-metal.config.chalkos.outputs ? jool;
        };
      };
    expected = {
      module = [ "jool" ];
      loaded = true;
      inputs = {
        jool = [
          "NAT64"
          "VRF"
          "Network"
        ];
        unbound = [
          "Resolver"
          "VRF"
          "Network"
        ];
      };
      firewallReads = true;
      networkdReads = true;
      checks = {
        jool = true;
        unbound = true;
      };
      effects = {
        jool = [ "chalkos-nat64.service" ];
        unbound = [ "unbound.service" ];
        start = {
          restart = [ "unbound.service" ];
          start = true;
        };
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
      diagnostic = {
        role = "operator";
        capabilities = [ "CAP_NET_ADMIN" ];
        networkNamespace = "nat64-{instance}";
      };
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
        reloads = true;
        stops = true;
        effect = {
          outputs = [
            "unbound"
            "networkd"
          ];
          reload = [ "chalkos-resolvers.service" ];
        };
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
        edge = forwarding layered.edge-metal.config == on;
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
  # mkImages evaluates the images with the nixpkgs and the module arguments given, and gives
  # their modules a lib that holds lib.chalkos.
  testMkImagesUsesItsNixpkgs = {
    expr =
      let
        n =
          (mkImages {
            nixpkgs.lib = lib // {
              nixosSystem = args: {
                marker = "custom";
                inherit (args) specialArgs lib;
              };
            };
            specialArgs.answer = 42;
          }).nixos.node-metal;
      in
      {
        inherit (n) marker;
        inherit (n.specialArgs) answer;
        chalkos = n.lib ? chalkos;
      };
    expected = {
      marker = "custom";
      answer = 42;
      chalkos = true;
    };
  };
  # A variant's modules set the image's pod network, which its image.json names; the image names
  # no images of the cluster, which its config does.
  testVariantSetsKubernetesPodNetwork = {
    expr =
      let
        n =
          (mkImages {
            variants.node.modules = [ { chalkos.cni.provider = "none"; } ];
          }).nixos.node-metal;
        facts = builtins.fromJSON (builtins.readFile n.config.chalkos.imageDirectory.facts.kubernetes);
      in
      {
        inherit (facts) cni;
        images = facts ? images;
      };
    expected = {
      cni = "none";
      images = false;
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
  # chalkd pulls images over HTTPS with Go's crypto/x509, which reads the system trust store at
  # /etc/ssl/certs/ca-certificates.crt, on the nodes for upgrades and on the installer for installs.
  testImageHasTheCABundle = {
    expr =
      let
        i = mkImages { variants.worker = { }; };
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
          dhcpGrant =
            (builtins.fromJSON c.environment.etc."chalkos/services.json".text).chalkos-chrony-dhcp.kinds;
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
            (builtins.fromJSON c.environment.etc."chalkos/effects.json".text).chrony == {
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
            && lib.any (p: lib.getName p == "chalkos-chrony-reload-sources") c.systemd.services.chalkd.path;
          # The TimeSync's servers are an output, which a template renders, chronyd checks, and
          # the examples exercise when the image is built.
          output =
            let
              o = c.chalkos.outputs.chrony;
              # The command chalkd runs, which outputs.json holds.
              render =
                (builtins.fromJSON (
                  builtins.unsafeDiscardStringContext c.environment.etc."chalkos/outputs.json".text
                )).chrony.render;
            in
            {
              inherit (o) inputs dir;
              template =
                lib.hasInfix "chalkos-render template " render && lib.hasSuffix " servers.sources" render;
              check = lib.hasInfix "/bin/chronyd -p -f" o.check;
              examples = lib.attrNames (builtins.readDir o.examples);
            };
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
        "sourcedir /run/chalkos/outputs/chrony"
        "sourcedir /run/chalkos/chrony-dhcp"
      ];
      certTimeCheck = true;
      ntsDump = true;
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
        inputs = [ "TimeSync" ];
        dir = "/run/chalkos/outputs/chrony";
        template = true;
        check = true;
        examples = [
          "empty.yaml"
          "none.yaml"
          "servers.yaml"
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
  # The image fixes its Kubernetes release, whose kubelet it runs, and its pod network, as options
  # of its own; the Cluster document names the other images, which no option sets.
  testImageKubernetesOptions = {
    expr =
      let
        c = worker [ ];
      in
      {
        inherit (c.chalkos.kubernetes) enable;
        version = c.chalkos.kubernetes.package.version;
        inherit (c.chalkos.cni) provider;
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
      version = pkgs.kubernetes.version;
      provider = "flannel";
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
      message = "chalkos.kubernetes.package is Kubernetes ${pkgs.kubernetes.version} and chalkos.kubernetes.version is 1.36; set one of them, or both to one minor";
      releases = import ../../modules/node/kubernetes/releases.nix { inherit lib pkgs; };
      refused = modules: lib.any (a: !a.assertion && a.message == message) (worker modules).assertions;
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
        config = worker [ ];
      in
      {
        # The image opens neither the API server's nor etcd's ports, which etcd authenticates by its
        # CA, nor makes etcd's directory: chalkos-controlplane does both by the Node's type.
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
        grant =
          (builtins.fromJSON config.environment.etc."chalkos/services.json".text).chalkos-controlplane.kinds;
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
      firewall = false;
      apiServerPort = false;
      markedPorts = true;
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
        message = "chalkos.kernel.moduleGroups names chalkos-nothing, which chalkos.kernel.groups does not declare; it declares ${
          lib.concatStringsSep ", " (lib.attrNames (worker [ ]).chalkos.kernel.groups)
        }";
        refused = modules: lib.any (a: !a.assertion && a.message == message) (worker modules).assertions;
      in
      {
        node = groupsOf layered.node-metal.config;
        plain = groupsOf layered.plain-metal.config;
        edge = groupsOf layered.edge-metal.config;
        declared = layered.plain-metal.config.chalkos.kernel.groups ? kubernetes;
        firewall = layered.plain-metal.config.chalkos.kernel.groups.firewall.directories;
        unknown = refused [ { chalkos.kernel.moduleGroups = [ "chalkos-nothing" ]; } ];
        known = refused [ ];
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
        unknown = true;
        known = false;
        unknownTree = false;
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
  # Mirrors come from the Mirror and Registry documents, written to /run before containerd starts.
  testRegistryMirrorsFromConfig =
    let
      config = worker [ ];
      unit = config.systemd.services.chalkos-registry-mirrors;
      services = builtins.fromJSON config.environment.etc."chalkos/services.json".text;
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
      config = worker [ ];
      unit = config.systemd.services.chalkos-containerd-config;
      services = builtins.fromJSON config.environment.etc."chalkos/services.json".text;
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
      image = factsOf (worker [ ]);
      noFlannel = factsOf (worker [ { chalkos.cni.provider = "none"; } ]);
      w = worker [ ];
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
  # The kubelet's credential provider: a unit granted the Registry kind writes its files before the
  # kubelet, which reads its flags, and a Registry change starts both again. The kubelet only wants
  # the unit and its flags file may be missing, so a failure to write them leaves anonymous pulls.
  testRegistryCredentialsUnit =
    let
      config = worker [ ];
      unit = config.systemd.services.chalkos-registry-credentials;
      kubelet = config.systemd.services.kubelet;
      services = builtins.fromJSON config.environment.etc."chalkos/services.json".text;
      effects = builtins.fromJSON config.environment.etc."chalkos/effects.json".text;
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
        none = (environment (worker [ ])) ? CHALKD_HEALTH_IGNORE_UNITS;
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
        default = timeout (worker [ ]);
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
      };
      table = {
        type = "pattern";
        regex = "^[a-z]+$";
        description = "A table.";
      };
    in
    {
      expr = {
        accepted = refusals {
          route = plain // {
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
        };
        name = refusals {
          Route_Get = plain // {
            command = [ ip ];
          };
        };
        program = refusals {
          route = plain // {
            command = [
              "ip"
              "route"
            ];
          };
        };
        placeholder = refusals {
          route = plain // {
            command = [
              ip
              "route"
              "get"
              "{address}"
            ];
          };
        };
        parameter = refusals {
          route = plain // {
            command = [
              ip
              "route"
              "show"
            ];
            parameters.table = table;
          };
        };
        parameterName = refusals {
          route = plain // {
            command = [
              ip
              "route"
              "show"
              "{Table}"
            ];
            parameters.Table = table;
          };
        };
        optionalDefault = refusals {
          route = plain // {
            command = [
              ip
              "route"
              "show"
              "table"
              "{table}"
            ];
            parameters.table = table // {
              optional = true;
              default = "main";
            };
          };
        };
        group = refusals {
          route = plain // {
            command = [ ip ];
            groups = [ "chalkos-no-such-group" ];
          };
        };
        groupForm = refusals {
          route = plain // {
            command = [ ip ];
            groups = [ "Wheel" ];
          };
        };
        longName = refusals {
          ${lib.strings.replicate 65 "a"} = plain // {
            command = [ ip ];
          };
        };
        longTimeout = refusals {
          route = plain // {
            command = [ ip ];
            timeout = "601s";
          };
        };
        longestTimeout = refusals {
          route = plain // {
            command = [ ip ];
            timeout = "10m";
          };
        };
        namespace = refusals {
          in-namespace = plain // {
            command = [ ip ];
            networkNamespace = "ns-{name}";
            parameters.name = table;
          };
        };
        namespaceOptional = refusals {
          in-namespace = plain // {
            command = [ ip ];
            networkNamespace = "ns-{name}";
            parameters.name = table // {
              optional = true;
            };
          };
        };
        namespaceParameter = refusals {
          in-namespace = plain // {
            command = [ ip ];
            networkNamespace = "ns-{name}";
          };
        };
        # A parameter fills a namespace's name with one character at least.
        namespaceLongest = refusals {
          in-namespace = plain // {
            command = [ ip ];
            networkNamespace = "${lib.strings.replicate 254 "a"}{name}";
            parameters.name = table;
          };
        };
        namespaceLong = refusals {
          in-namespace = plain // {
            command = [ ip ];
            networkNamespace = "${lib.strings.replicate 255 "a"}{name}";
            parameters.name = table;
          };
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
      etc = builtins.fromJSON (
        builtins.unsafeDiscardStringContext c.environment.etc."chalkos/diagnostics.json".text
      );
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
  # the store. Readers run those that read the node's state; ping, dns-query and tcp-connect send
  # traffic and nft-ruleset shows the firewall, so they need an operator, and ping and nft-ruleset
  # alone get a capability. ip is the ip-only build, which leaves iproute2's other tools and arpd's
  # Berkeley DB out of the images.
  testBaseDiagnostics =
    let
      read =
        c:
        builtins.fromJSON (
          builtins.unsafeDiscardStringContext c.environment.etc."chalkos/diagnostics.json".text
        );
      node = read (worker [ ]);
      installer = read (mkImages { }).nixos.installer.config;
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
        installer = lib.attrNames installer;
        inStore = lib.all (d: lib.hasPrefix "/nix/store/" (lib.head d.command)) (lib.attrValues node);
        ip = lib.hasSuffix "-iproute2-ip-${pkgs.iproute2.version}/bin/ip" (lib.head node.route-get.command);
        roles = lib.mapAttrs (_: d: d.role) node;
        capabilities = lib.filterAttrs (_: c: c != [ ]) (lib.mapAttrs (_: d: d.capabilities) node);
        routeGet = node.route-get // {
          command = lib.tail node.route-get.command;
        };
        routes = lib.tail node.routes.command;
        ipRules = lib.tail node.ip-rules.command;
        ping = lib.tail node.ping.command;
        dnsQuery = lib.tail node.dns-query.command;
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
        roles = {
          addresses = "reader";
          dns-query = "operator";
          ip-rules = "reader";
          links = "reader";
          neighbours = "reader";
          nft-ruleset = "operator";
          ping = "operator";
          route-get = "reader";
          routes = "reader";
          tcp-connect = "operator";
        };
        capabilities = {
          nft-ruleset = [ "CAP_NET_ADMIN" ];
          ping = [ "CAP_NET_RAW" ];
        };
        routeGet = {
          description = "The route the kernel takes to an address.";
          command = [
            "-json"
            "route"
            "get"
            "{address}"
          ];
          parameters.address = {
            type = "address";
            description = "IPv4 or IPv6 address, without a zone: ip route get takes none, and finds a link-local address's interface by the routes.";
            optional = false;
            default = "";
            min = null;
            max = null;
            values = [ ];
            regex = "";
          };
          role = "reader";
          timeout = "10s";
          capabilities = [ ];
          groups = [ ];
          networkNamespace = "";
        };
        routes = [
          "-json"
          "-family"
          "{family}"
          "route"
          "show"
          "table"
          "{table}"
        ];
        ipRules = [
          "-json"
          "-family"
          "{family}"
          "rule"
          "show"
        ];
        ping = [
          "-n"
          "-w"
          "10"
          "-c"
          "{count}"
          "{address}"
        ];
        dnsQuery = [
          "{name}"
          "{type}"
          "@{server}"
        ];
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
}
