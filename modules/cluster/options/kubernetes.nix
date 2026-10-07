# Kubernetes options of the cluster, its roles and its nodes.
{
  config,
  lib,
  nixpkgs,
  ...
}:
let
  inherit (lib) mkOption types;
  pkgs = nixpkgs.legacyPackages.${config.chalkos.cluster.system};
  flags = types.attrsOf types.str;
  flagsOf =
    component:
    mkOption {
      type = flags;
      default = { };
      description = "Extra flags of ${component}, without the leading dashes; they override chalkos's own.";
    };

  # The node's static addresses in network name order, without their prefix lengths.
  staticAddresses =
    network:
    let
      networks = network.networks or { };
    in
    map (a: builtins.head (lib.splitString "/" a)) (
      lib.concatMap (name: networks.${name}.address or [ ]) (lib.attrNames networks)
    );

  # The endpoint's host when it is an IP literal, without brackets; null for a hostname.
  endpointIP =
    let
      authority = builtins.head (
        lib.splitString "/" (lib.removePrefix "https://" config.chalkos.cluster.endpoint)
      );
      ipv6 = builtins.match "\\[([^]]+)].*" authority;
      ipv4 = builtins.match "([0-9]{1,3}(\\.[0-9]{1,3}){3})(:.*)?" authority;
    in
    if ipv6 != null then
      builtins.head ipv6
    else if ipv4 != null then
      builtins.head ipv4
    else
      null;
  controlPlaneNodes = lib.attrValues (
    lib.filterAttrs (
      _: node: config.chalkos.roles.${node.role}.kubernetes.kind == "controlplane"
    ) config.chalkos.nodes
  );

  # The four octets of an IPv4 address, or null. Octets have no leading zeros, as in Go's netip.
  parseIPv4 =
    s:
    let
      octet = "(0|[1-9][0-9]{0,2})";
      octets = builtins.match "${octet}\\.${octet}\\.${octet}\\.${octet}" s;
      values = map lib.toInt octets;
    in
    if octets == null || lib.any (o: o > 255) values then null else values;

  # The eight 16-bit groups of an IPv6 address, or null. "::" stands for at least one group of
  # zeros, and the last two groups may be written as an IPv4 address.
  parseIPv6 =
    s:
    let
      halves = lib.splitString "::" s;
      # The groups of the colon-separated fields of part, or null.
      groupsOf =
        last: part:
        let
          fields = if part == "" then [ ] else lib.splitString ":" part;
          field =
            i: f:
            let
              v4 = parseIPv4 f;
            in
            if builtins.match "[0-9A-Fa-f]{1,4}" f != null then
              [ (lib.fromHexString f) ]
            else if last && i == builtins.length fields - 1 && v4 != null then
              [
                (builtins.elemAt v4 0 * 256 + builtins.elemAt v4 1)
                (builtins.elemAt v4 2 * 256 + builtins.elemAt v4 3)
              ]
            else
              null;
          groups = lib.imap0 field fields;
        in
        if lib.elem null groups then null else lib.concatLists groups;
      head = groupsOf (builtins.length halves == 1) (builtins.head halves);
      tail = groupsOf true (lib.last halves);
      zeros = 8 - builtins.length head - builtins.length tail;
    in
    if builtins.length halves == 1 then
      if head != null && builtins.length head == 8 then head else null
    else if builtins.length halves == 2 && head != null && tail != null && zeros >= 1 then
      head ++ lib.replicate zeros 0 ++ tail
    else
      null;

  # A subnet filter as { exclude, ipv4, groups, prefix }, groups being the address's octets or
  # 16-bit groups, or else a string saying what is wrong with it. It accepts what chalkd's
  # nodeip.ParseFilter accepts.
  parseSubnet =
    s:
    let
      parts = builtins.match "(!?)([^/]+)/(0|[1-9][0-9]{0,2})" s;
      address = builtins.elemAt parts 1;
      prefix = lib.toInt (builtins.elemAt parts 2);
      v4 = parseIPv4 address;
      v6 = parseIPv6 address;
      parsed = ipv4: groups: {
        exclude = builtins.elemAt parts 0 == "!";
        inherit ipv4 groups prefix;
      };
    in
    if parts == null then
      "is not a subnet in CIDR notation"
    else if v4 != null then
      if prefix > 32 then "has a prefix longer than 32 bits" else parsed true v4
    else if v6 == null then
      "is not a subnet in CIDR notation: ${address} is not an IPv4 or IPv6 address"
    else if lib.take 6 v6 == lib.replicate 5 0 ++ [ 65535 ] then
      # The node compares its addresses in their IPv4 form, which such a subnet never holds.
      "is an IPv4-mapped IPv6 subnet; write the IPv4 form, such as 10.0.0.0/8"
    else if prefix > 128 then
      "has a prefix longer than 128 bits"
    else
      parsed false v6;

  # A subnet in CIDR notation, excluded with a leading "!".
  subnet = lib.mkOptionType {
    name = "subnet";
    description = ''subnet in CIDR notation, excluded with a leading "!"'';
    check = builtins.isString;
    merge =
      loc: defs:
      let
        value = lib.mergeEqualOption loc defs;
        parsed = parseSubnet value;
      in
      if builtins.isString parsed then
        throw "${
          lib.showOption (lib.filter (p: !lib.hasPrefix "[definition " p) loc)
        }: \"${value}\" ${parsed}"
      else
        value;
  };
  clusterSubnets = config.chalkos.cluster.kubernetes.nodeIP.validSubnets;

  # An address as { ipv4, groups }, like parseSubnet's, or null.
  parseAddress =
    s:
    let
      v4 = parseIPv4 s;
      v6 = parseIPv6 s;
    in
    if v4 != null then
      {
        ipv4 = true;
        groups = v4;
      }
    else if v6 != null then
      {
        ipv4 = false;
        groups = v6;
      }
    else
      null;

  # Whether the parsed subnet holds the parsed address: each group agrees in the bits the prefix
  # covers of it.
  holds =
    subnet: address:
    let
      width = if subnet.ipv4 then 8 else 16;
      pow2 = n: builtins.foldl' (x: _: x * 2) 1 (lib.range 1 n);
      agrees =
        i: group:
        let
          unit = pow2 (width - lib.max 0 (lib.min width (subnet.prefix - i * width)));
        in
        group / unit == builtins.elemAt address.groups i / unit;
    in
    subnet.ipv4 == address.ipv4 && lib.all lib.id (lib.imap0 agrees subnet.groups);

  # Whether a node may pick the parsed address from these subnets, as chalkd's nodeip.Filter
  # decides it.
  subnetsMatch =
    subnets: address:
    let
      parsed = map parseSubnet subnets;
      included = lib.filter (s: !s.exclude) parsed;
      excluded = lib.filter (s: s.exclude) parsed;
    in
    (included == [ ] || lib.any (s: holds s address) included)
    && !lib.any (s: holds s address) excluded;

  familyOf = parsed: if parsed.ipv4 then "ipv4" else "ipv6";
  inherit (config.chalkos.cluster.kubernetes) ipFamilies;

  # Whether a control-plane node may hold the endpoint's address: as its fixed address of that
  # family, or as one it picks at boot from the subnets that apply to it.
  holdsEndpoint =
    node:
    let
      endpoint = parseAddress endpointIP;
      subnets =
        if node.kubernetes.validSubnets != null then node.kubernetes.validSubnets else clusterSubnets;
      fixed = lib.filter (a: a != null && familyOf a == familyOf endpoint) (
        map parseAddress node.kubernetes.nodeIPs
      );
    in
    endpoint != null
    && (if fixed != [ ] then lib.elem endpoint fixed else subnetsMatch subnets endpoint);

  # What is wrong with the addresses of the named node, as messages.
  addressErrors =
    name: node: nodeIPs:
    let
      inherit (node.kubernetes) nodeIP;
      valid = lib.filter (a: a != null) (map parseAddress nodeIPs);
      families = map familyOf valid;
      subnets =
        if node.kubernetes.validSubnets != null then node.kubernetes.validSubnets else clusterSubnets;
      included = lib.filter (s: !s.exclude) (map parseSubnet subnets);
      # A family is picked from the subnets when they include one of it or include none at all.
      picks = family: included == [ ] || lib.any (s: familyOf s == family) included;
      option = "chalkos.nodes.${name}.kubernetes";
    in
    lib.optional (nodeIP != null && nodeIPs != [ nodeIP ]) "${option}: set nodeIP or nodeIPs, not both"
    ++ map (a: "${option}.nodeIPs: \"${a}\" is not an address") (
      lib.filter (a: parseAddress a == null) nodeIPs
    )
    ++ map (f: "${option}.nodeIPs: more than one ${f} address; give at most one per family") (
      lib.filter (f: lib.count (x: x == f) families > 1) (lib.unique families)
    )
    ++ map (
      f:
      "${option}.nodeIPs: an ${f} address, but chalkos.cluster.kubernetes.ipFamilies is [ ${toString ipFamilies} ]"
    ) (lib.filter (f: !lib.elem f ipFamilies) (lib.unique families))
    ++ map (
      f:
      "${option}: no ${f} address in nodeIPs, and no ${f} subnet in the validSubnets that apply, so the node cannot pick its ${f} address"
    ) (lib.filter (f: !lib.elem f families && !picks f) ipFamilies);

  # The virtual addresses, checked: one per family of the cluster, and the endpoint one of them
  # unless it is a host name.
  vipAddresses =
    addresses:
    let
      parsed = map parseAddress addresses;
      families = map familyOf (lib.filter (a: a != null) parsed);
      endpoint = parseAddress endpointIP;
      errors =
        map (a: "\"${a}\" is not an address") (lib.filter (a: parseAddress a == null) addresses)
        ++ map (f: "more than one ${f} address; give at most one per family") (
          lib.filter (f: lib.count (x: x == f) families > 1) (lib.unique families)
        )
        ++ map (
          f: "an ${f} address, but chalkos.cluster.kubernetes.ipFamilies is [ ${toString ipFamilies} ]"
        ) (lib.filter (f: !lib.elem f ipFamilies) (lib.unique families))
        ++
          lib.optional (addresses != [ ] && endpointIP != null && !lib.elem endpoint parsed)
            "the endpoint ${config.chalkos.cluster.endpoint} is none of them; with a VIP the endpoint is a VIP or a host name";
    in
    if errors == [ ] then
      addresses
    else
      throw "chalkos.cluster.kubernetes.vip.addresses: ${lib.concatStringsSep "; " errors}";
in
{
  options.chalkos.cluster.kubernetes = {
    package = mkOption {
      type = types.package;
      default = pkgs.kubernetes;
      defaultText = lib.literalExpression "pkgs.kubernetes";
      description = ''
        Kubernetes release of the cluster. Nodes run its kubelet; the control plane runs the
        upstream images of the same version. A Kubernetes upgrade is an image upgrade.
      '';
    };
    podCIDR = mkOption {
      type = types.str;
      default = "10.244.0.0/16";
      description = "Address range of pods; each node gets a part of it.";
    };
    serviceCIDR = mkOption {
      type = types.str;
      default = "10.96.0.0/12";
      description = "Address range of services.";
    };
    dnsIP = mkOption {
      type = types.str;
      default = "10.96.0.10";
      description = "Service address of the cluster DNS, inside serviceCIDR.";
    };
    domain = mkOption {
      type = types.str;
      default = "cluster.local";
      description = "DNS domain of the cluster.";
    };
    allowSchedulingOnControlPlanes = mkOption {
      type = types.bool;
      default = false;
      description = "Let workloads run on control-plane nodes, which are otherwise tainted.";
    };
    ipFamilies = mkOption {
      type = types.listOf (
        types.enum [
          "ipv4"
          "ipv6"
        ]
      );
      default = [ "ipv4" ];
      example = [
        "ipv4"
        "ipv6"
      ];
      apply =
        families:
        if families != [ ] && lib.allUnique families then
          families
        else
          throw "chalkos.cluster.kubernetes.ipFamilies must list ipv4, ipv6 or both, each once";
      description = ''
        Address families of the nodes, the primary one first. Every node has one address of each:
        the kubelet registers them, the control plane's certificates name them, and etcd and the
        API server advertise the primary one.
      '';
    };
    vip = {
      addresses = mkOption {
        type = types.listOf types.str;
        default = [ ];
        example = [ "10.0.0.10" ];
        apply = vipAddresses;
        description = ''
          Virtual addresses of the API server, at most one per family of ipFamilies. One healthy
          control-plane node at a time holds them, chosen by an election in etcd, so the endpoint
          is one of them or a host name that resolves to them. Empty means no virtual IP.
        '';
      };
      mode = mkOption {
        type = types.enum [ "l2" ];
        default = "l2";
        description = ''
          How the holder announces the addresses: l2 adds them to an interface and announces
          them with gratuitous ARP and unsolicited neighbour advertisements.
        '';
      };
      interface = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = ''
          Interface the addresses are added to; null means the interface holding the node's
          address of the address's family.
        '';
      };
    };
    nodeIP = {
      validSubnets = mkOption {
        type = types.listOf subnet;
        default = [ ];
        example = [
          "10.0.0.0/8"
          "!10.0.0.10/32"
        ];
        description = ''
          Subnets in CIDR notation, IPv4 or IPv6, that nodes without a fixed nodeIP pick their
          address from at every boot; a leading `!` excludes a subnet. A node takes the first
          matching address: IPv4 before IPv6, then by interface name and address, and the
          endpoint's address, which may be a virtual one, only when no other matches. Empty takes
          any global unicast address. Addresses in the pod and service ranges and on the
          interfaces of the pod network and kube-proxy (`flannel.*`, `cni*`, `veth*`, `kube-*`)
          are never taken. chalkos.nodes.<name>.kubernetes.validSubnets overrides this per node.
        '';
      };
      timeout = mkOption {
        type = types.ints.positive;
        default = 300;
        description = ''
          Seconds a node waits at boot for its address, so DHCP leases and addresses a routing
          daemon adds late still count. A node without one runs neither the kubelet nor static
          pods.
        '';
      };
    };
    extraArgs = {
      etcd = flagsOf "etcd";
      kube-apiserver = flagsOf "kube-apiserver" // {
        # These flags decide who may do what; chalkos sets them and chalkd refuses overrides.
        apply =
          flags:
          let
            protected = lib.intersectLists (lib.attrNames flags) [
              "anonymous-auth"
              "authentication-config"
              "authorization-mode"
              "enable-bootstrap-token-auth"
            ];
          in
          if protected == [ ] then
            flags
          else
            throw "chalkos.cluster.kubernetes.extraArgs.kube-apiserver cannot override ${lib.concatStringsSep ", " protected}: chalkos sets them";
      };
      kube-controller-manager = flagsOf "kube-controller-manager";
      kube-scheduler = flagsOf "kube-scheduler";
      kubelet = flagsOf "the kubelet";
    };
    images = {
      etcd = mkOption {
        type = types.str;
        default = "registry.k8s.io/etcd:3.7.0-0";
        description = "Image of etcd; the default is the version kubeadm uses with this Kubernetes release.";
      };
      pause = mkOption {
        type = types.str;
        default = "registry.k8s.io/pause:3.10.2";
        description = "Image of the pod sandbox.";
      };
      coredns = mkOption {
        type = types.str;
        default = "registry.k8s.io/coredns/coredns:v1.14.6";
        description = "Image of the cluster DNS.";
      };
    };
  };

  options.chalkos.cluster.manifests = mkOption {
    type = types.listOf (types.attrsOf types.anything);
    default = [ ];
    example = lib.literalExpression ''
      [ { apiVersion = "v1"; kind = "Namespace"; metadata.name = "apps"; } ]
    '';
    description = ''
      Kubernetes objects the control plane applies with server-side apply after the built-in
      ones, in this order, at bootstrap and at every boot.
    '';
  };

  options.chalkos.cluster.registries = {
    mirrors = mkOption {
      type = types.attrsOf (types.listOf types.str);
      default = { };
      example = {
        "docker.io" = [ "https://mirror.example.com" ];
      };
      # Nothing authenticates a plain HTTP mirror: anyone on the path can replace its images.
      apply =
        mirrors:
        let
          plain = lib.concatLists (
            lib.mapAttrsToList (
              registry: endpoints:
              map (endpoint: "${registry}: ${endpoint}") (
                lib.filter (endpoint: !lib.hasPrefix "https://" endpoint) endpoints
              )
            ) mirrors
          );
        in
        if plain == [ ] || config.chalkos.cluster.registries.allowPlainHTTP then
          mirrors
        else
          throw "chalkos.cluster.registries.mirrors must be https:// URLs unless chalkos.cluster.registries.allowPlainHTTP is set: ${lib.concatStringsSep ", " plain}";
      description = ''
        Mirrors of container registries, by registry host, as https:// URLs. containerd tries
        them in order and falls back to the registry itself.
      '';
    };
    allowPlainHTTP = mkOption {
      type = types.bool;
      default = false;
      description = ''
        Allow mirrors that are not https:// URLs. It exists for test registries: nothing
        authenticates what a plain HTTP mirror serves.
      '';
    };
  };

  options.chalkos.roles = mkOption {
    type = types.attrsOf (
      types.submodule {
        options.kubernetes.kind = mkOption {
          type = types.nullOr (
            types.enum [
              "controlplane"
              "worker"
            ]
          );
          default = "worker";
          description = ''
            What the role's nodes are in Kubernetes. Control-plane nodes run etcd and the control
            plane besides the kubelet; null leaves Kubernetes out of the role's image.
          '';
        };
      }
    );
  };

  # Hostnames are not checked: they may name a load balancer or a DNS record of the nodes. A VIP
  # endpoint is checked with the VIPs.
  config.chalkos.warnings =
    lib.optional
      (
        controlPlaneNodes != [ ]
        && endpointIP != null
        && config.chalkos.cluster.kubernetes.vip.addresses == [ ]
        && !lib.any holdsEndpoint controlPlaneNodes
      )
      ''
        chalkos.cluster.endpoint ${config.chalkos.cluster.endpoint} is neither a nodeIPs entry of a
        control-plane node nor in the validSubnets one picks its address from.
        The endpoint must reach a control-plane node, for example through a node's IP, a load
        balancer or a VIP.
      '';

  options.chalkos.nodes = mkOption {
    type = types.attrsOf (
      types.submodule (
        { config, name, ... }:
        let
          subnets =
            if config.kubernetes.validSubnets != null then config.kubernetes.validSubnets else clusterSubnets;
        in
        {
          options.kubernetes.nodeIP = mkOption {
            type = types.nullOr types.str;
            default = null;
            description = "Shorthand for nodeIPs with one address.";
          };
          options.kubernetes.nodeIPs = mkOption {
            type = types.listOf types.str;
            default =
              let
                # The first static address of each of the cluster's families.
                static = lib.filter (a: parseAddress a != null) (staticAddresses config.network);
                first = f: lib.take 1 (lib.filter (a: familyOf (parseAddress a) == f) static);
              in
              if config.kubernetes.nodeIP != null then
                [ config.kubernetes.nodeIP ]
              else if subnets == [ ] then
                lib.concatMap first ipFamilies
              else
                [ ];
            defaultText = lib.literalMD "nodeIP, or else the node's first static address of each family, unless validSubnets apply to the node";
            apply =
              ips:
              let
                errors = addressErrors name config ips;
              in
              if errors == [ ] then
                ips
              else
                throw "chalkos.nodes.${name} is invalid:\n${lib.concatMapStringsSep "\n" (e: "- ${e}") errors}";
            description = ''
              Fixed addresses of the node, at most one per family of
              chalkos.cluster.kubernetes.ipFamilies: the kubelet registers them and, on
              control-plane nodes, the certificates name them and etcd and the API server
              advertise the primary family's. The node waits at boot until its interfaces hold
              them. The node picks the address of a family without one from the validSubnets that
              apply.
            '';
          };
          options.kubernetes.validSubnets = mkOption {
            type = types.nullOr (types.listOf subnet);
            default = null;
            example = [ "192.168.100.0/24" ];
            description = ''
              Subnets the node picks its address from instead of
              chalkos.cluster.kubernetes.nodeIP.validSubnets; null uses those.
            '';
          };
        }
      )
    );
  };
}
