{ config, lib, ... }:
let
  taint = lib.types.submodule {
    options = {
      key = lib.mkOption {
        type = lib.types.str;
        example = "dedicated";
        description = "Taint key.";
      };
      value = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Taint value; null for a taint without a value.";
      };
      effect = lib.mkOption {
        type = lib.types.enum [
          "NoSchedule"
          "PreferNoSchedule"
          "NoExecute"
        ];
        description = "What happens to pods that do not tolerate the taint.";
      };
    };
  };

  nodeModule =
    { name, ... }:
    {
      options = {
        role = lib.mkOption {
          type = lib.types.enum (lib.attrNames config.chalkos.roles);
          description = "Role whose image this node runs; must name an entry of `chalkos.roles`.";
        };
        hostname = lib.mkOption {
          type = lib.types.str;
          default = name;
          description = "Hostname set at boot from the node identity.";
        };
        network = lib.mkOption {
          type = lib.types.lazyAttrsOf lib.types.anything;
          default = { };
          description = ''
            systemd-networkd configuration in the shape of NixOS's `systemd.network` options
            (`networks`, `netdevs`, `links`), delivered to the node through its identity.
          '';
        };
        labels = lib.mkOption {
          type = lib.types.attrsOf lib.types.str;
          default = { };
          description = "Kubernetes labels applied to the node.";
        };
        taints = lib.mkOption {
          type = lib.types.listOf taint;
          default = [ ];
          description = "Kubernetes taints applied to the node.";
        };
      };
    };
in
{
  options.chalkos.nodes = lib.mkOption {
    type = lib.types.attrsOf (lib.types.submodule nodeModule);
    default = { };
    description = "Nodes of the cluster. Extensions add their own per-node options to this submodule.";
  };
}
