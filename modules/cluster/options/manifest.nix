# The cluster description chalkctl reads. Unstable while schemaVersion is 0.
{ config, lib, ... }:
let
  cfg = config.chalkos;
  storage = import ../storage.nix { inherit lib; };
  coreNodeOptions = [
    "role"
    "hostname"
    "storage"
    "network"
    "labels"
    "taints"
    "kubernetes"
    "time"
    "_module"
  ];
  strip = attrs: removeAttrs attrs [ "_module" ];

  renderStorage =
    name: n:
    let
      errors = storage.errors n.storage;
    in
    if errors != [ ] then
      throw "chalkos.nodes.${name}.storage is invalid:\n${
        lib.concatMapStringsSep "\n" (e: "- ${e}") errors
      }"
    else
      storage.render {
        cluster = cfg.cluster.name;
        node = name;
        inherit (n) storage;
      };

  # The node's networkd configuration as unit files, rendered with the role image's own networkd
  # options and renderer, so a unit means on the node exactly what it means in NixOS.
  renderNetwork =
    name: n:
    let
      nixos = cfg.roles.${n.role}.nixos;
      inherit (nixos._module.args.utils.systemdUtils.network) units;
      kinds = {
        networks = {
          suffix = "network";
          toUnit = units.networkToUnit;
        };
        netdevs = {
          suffix = "netdev";
          toUnit = units.netdevToUnit;
        };
        links = {
          suffix = "link";
          toUnit = units.linkToUnit;
        };
      };
      unknown = lib.attrNames (removeAttrs n.network (lib.attrNames kinds));
      render =
        kind:
        { suffix, toUnit }:
        let
          defs = n.network.${kind} or { };
          loc = [
            "chalkos"
            "nodes"
            name
            "network"
            kind
          ];
          # Merging through the option's type applies defaults and shorthands such as `name`.
          merged = nixos.options.systemd.network.${kind}.type.merge loc [
            {
              file = "chalkos.nodes.${name}.network";
              value = defs;
            }
          ];
        in
        lib.optionalAttrs (defs != { }) (
          lib.mapAttrs' (unit: def: lib.nameValuePair "${unit}.${suffix}" (toUnit def)) (
            lib.filterAttrs (_: def: def.enable) merged
          )
        );
    in
    if unknown != [ ] then
      throw "chalkos.nodes.${name}.network: ${lib.concatStringsSep ", " unknown} is not one of networks, netdevs and links"
    else
      lib.concatMapAttrs render kinds;

  # The labels a kubelet may set on its own Node (k8s.io/kubelet's IsKubeletLabel in Kubernetes
  # 1.37): it refuses other labels in the kubernetes.io and k8s.io namespaces and does not start.
  kubeletLabels = [
    "kubernetes.io/hostname"
    "topology.kubernetes.io/zone"
    "topology.kubernetes.io/region"
    "failure-domain.beta.kubernetes.io/zone"
    "failure-domain.beta.kubernetes.io/region"
    "beta.kubernetes.io/instance-type"
    "node.kubernetes.io/instance-type"
    "kubernetes.io/os"
    "kubernetes.io/arch"
    "beta.kubernetes.io/os"
    "beta.kubernetes.io/arch"
  ];
  kubeletLabelNamespaces = [
    "kubelet.kubernetes.io"
    "node.kubernetes.io"
  ];
  inNamespace = namespace: ns: namespace == ns || lib.hasSuffix ".${ns}" namespace;
  refusedLabel =
    key:
    let
      parts = lib.splitString "/" key;
      namespace = if lib.length parts > 1 then builtins.head parts else "";
    in
    lib.any (inNamespace namespace) [
      "kubernetes.io"
      "k8s.io"
    ]
    && !lib.elem key kubeletLabels
    && !lib.any (inNamespace namespace) kubeletLabelNamespaces;

  # Settings the kubelet of a node refuses to start with.
  kubeletErrors =
    name: n:
    map (
      label:
      "chalkos.nodes.${name}.labels.\"${label}\": the kubelet sets labels in the kubernetes.io and k8s.io namespaces on its own node only under kubelet.kubernetes.io/ and node.kubernetes.io/ or from the list ${lib.concatStringsSep ", " kubeletLabels}"
    ) (lib.filter refusedLabel (lib.attrNames n.labels))
    ++ lib.mapAttrsToList (
      volume: _:
      "chalkos.nodes.${name}.storage.volumes.${volume}: a swap volume on a Kubernetes node; the kubelet refuses to run with swap (failSwapOn)"
    ) (lib.filterAttrs (_: v: v.enable && v.format == "swap") n.storage.volumes);

  node = name: n: {
    inherit (n) role;
    identity = {
      inherit (n) hostname network labels;
      networkUnits = renderNetwork name n;
      storage = renderStorage name n;
      taints = map strip n.taints;
      # A node's name in Kubernetes is its name in the cluster, which its kubelet certificates
      # carry; null on nodes of a role without Kubernetes.
      kubernetes =
        let
          inherit (cfg.roles.${n.role}.kubernetes) kind;
        in
        if kind == null then
          null
        else if kubeletErrors name n != [ ] then
          throw "chalkos.nodes.${name} is invalid:\n${
            lib.concatMapStringsSep "\n" (e: "- ${e}") (kubeletErrors name n)
          }"
        else
          {
            nodeName = name;
            # A node picks the addresses of the families without a fixed one at boot; the
            # cluster's subnets are in the image, a node's own ones here.
            inherit (n.kubernetes) nodeIPs validSubnets;
          };
      # A node's own servers replace the cluster's.
      time.servers = map strip (if n.time.servers != null then n.time.servers else cfg.time.servers);
      extensions = removeAttrs n coreNodeOptions;
    };
  };
in
{
  options.chalkos.manifest = lib.mkOption {
    type = lib.types.raw;
    readOnly = true;
    description = "Generated cluster description read by chalkctl (JSON-serialisable, schemaVersion 0).";
  };

  config.chalkos.manifest = lib.showWarnings cfg.warnings {
    schemaVersion = 0;
    cluster = { inherit (cfg.cluster) name endpoint; };
    # Relative to the cluster's attribute, which only the evaluating CLI knows; the flake may
    # expose the cluster under any name.
    roles = lib.mapAttrs (role: r: {
      image = "roles.${role}.image";
      inherit (r.kubernetes) kind;
    }) cfg.roles;
    nodes = lib.mapAttrs node cfg.nodes;
  };
}
