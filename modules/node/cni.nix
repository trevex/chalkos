# The pod network the image ships. flannel connects pods across nodes with VXLAN; none leaves the
# choice to the cluster's Addon documents. chalkd renders flannel's objects from the node's config.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.chalkos.cni;
  # The reference plugins nixpkgs builds, such as bridge and portmap.
  referencePlugins = map baseNameOf pkgs.cni-plugins.subPackages;
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
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.cni = {
        provider = lib.mkOption {
          type = lib.types.enum [
            "flannel"
            "none"
          ];
          default = "flannel";
          description = ''
            Pod network of an image with Kubernetes. `flannel` connects pods across nodes with
            VXLAN, configured by the node's Flannel document; `none` installs no network, for
            clusters whose Addon documents bring their own, and ships every CNI reference plugin
            (see `chalkos.cni.plugins`).
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
            CNI reference plugins the image ships, in containerd's plugin directory. With
            `flannel` they are bridge, host-local, loopback and portmap, which its network runs,
            besides flannel's own plugin; with `none` every reference plugin nixpkgs builds, since
            the cluster's own network may use any. Definitions add to these; `lib.mkForce`
            replaces them.
          '';
        };
        flannel.image = lib.mkOption {
          type = lib.types.str;
          default = "ghcr.io/flannel-io/flannel:v0.28.9";
          description = "Image of flannel.";
        };
      };
    };
  };

  # A definition rather than the option's default, so a variant's definitions add to it.
  config.chalkos.cni.plugins = if cfg.provider == "flannel" then flannelPlugins else referencePlugins;
}
