# The pod network the images ship. flannel connects pods across nodes with VXLAN; none leaves the
# choice to the cluster's own manifests. chalkd renders flannel's objects from the node's config.
{
  config,
  lib,
  nixpkgs,
  ...
}:
let
  cfg = config.chalkos.cni;
  k = config.chalkos.cluster.kubernetes;
  inUse = family: lib.elem family k.ipFamilies;
  # The reference plugins nixpkgs builds, such as bridge and portmap.
  referencePlugins =
    map baseNameOf
      nixpkgs.legacyPackages.${config.chalkos.cluster.system}.cni-plugins.subPackages;
  # What flannel's network runs: flannel delegates to bridge with host-local addresses, portmap
  # serves host ports, and containerd sets up each pod's loopback with loopback.
  flannelPlugins = [
    "bridge"
    "host-local"
    "loopback"
    "portmap"
  ];
in
{
  options.chalkos.cni = {
    provider = lib.mkOption {
      type = lib.types.enum [
        "flannel"
        "none"
      ];
      default = "flannel";
      description = ''
        Pod network. `flannel` connects pods across nodes with VXLAN; `none` installs no
        network, for clusters whose manifests bring their own, and ships every CNI reference
        plugin (see `chalkos.cni.plugins`).
      '';
    };
    plugins = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      example = [ "bandwidth" ];
      apply =
        plugins:
        let
          unknown = lib.subtractLists referencePlugins plugins;
        in
        if unknown != [ ] then
          throw "chalkos.cni.plugins: no CNI reference plugin is named ${lib.concatStringsSep " or " unknown}; nixpkgs's cni-plugins has ${lib.concatStringsSep ", " referencePlugins}"
        else
          plugins;
      description = ''
        CNI reference plugins the images ship, in containerd's plugin directory. With `flannel`
        they are bridge, host-local, loopback and portmap, which its network runs, besides
        flannel's own plugin; with `none` every reference plugin nixpkgs builds, since the
        cluster's manifests may use any. Definitions add to these; `lib.mkForce` replaces them.
      '';
    };
    flannel.image = lib.mkOption {
      type = lib.types.str;
      default = "ghcr.io/flannel-io/flannel:v0.28.9";
      description = "Image of flannel.";
    };
    flannel.mtu = lib.mkOption {
      type = lib.types.nullOr lib.types.ints.positive;
      default = null;
      example = 1430;
      apply =
        mtu:
        let
          # Pods get 50 less, which must leave them their family's minimum MTU.
          family =
            if inUse "ipv6" then
              {
                name = "IPv6";
                minimum = 1280;
              }
            else
              {
                name = "IPv4";
                minimum = 576;
              };
          least = toString (family.minimum + 50);
        in
        if mtu != null && mtu < family.minimum + 50 then
          throw "chalkos.cni.flannel.mtu: ${toString mtu} is below the minimum of ${least}: pods get 50 less, and ${family.name} needs an MTU of at least ${toString family.minimum}"
        else
          mtu;
      description = ''
        MTU flannel's VXLAN assumes for the network between the nodes; its VXLAN devices and the
        pods get 50 less. null takes the MTU of the interface holding the node's address. Nodes
        whose address is on a loopback interface need it set, and a dummy interface holding a
        node's address needs an MTU of at least this, 20 more for an IPv6 address: the nodes
        refuse to prepare otherwise. flannel subtracts 50 in both families but IPv6 VXLAN needs
        70, so with IPv6 in use set this to the network's MTU minus 20; otherwise IPv6 pod packets
        near the full size rely on path MTU discovery. At least 1330 with IPv6 in use, so pods get
        IPv6's minimum of 1280, and 626 otherwise.
      '';
    };
  };

  config = {
    # A definition rather than the option's default, so a cluster's definitions add to it.
    chalkos.cni.plugins = if cfg.provider == "flannel" then flannelPlugins else referencePlugins;
  };
}
