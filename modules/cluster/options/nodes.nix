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
        platform = lib.mkOption {
          type = lib.types.enum (lib.attrNames config.chalkos.platforms);
          default = "metal";
          description = ''
            Platform the node runs on, which must name an entry of `chalkos.platforms`: the node
            runs its role's image for it, and refuses images of another platform. Changing it is a
            reinstall.
          '';
        };
        hostname = lib.mkOption {
          type = lib.types.str;
          default = name;
          description = "Hostname set at boot from the node's config.";
        };
        network = lib.mkOption {
          type = lib.types.lazyAttrsOf lib.types.anything;
          default = { };
          description = ''
            systemd-networkd configuration in the shape of NixOS's `systemd.network` options
            (`networks`, `netdevs`, `links`), delivered to the node as networkd documents of its
            config.
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
        time.servers = lib.mkOption {
          type = lib.types.nullOr (lib.types.listOf (import ../time.nix { inherit lib; }));
          default = null;
          description = "Time servers of this node, replacing `chalkos.time.servers`; null uses those.";
        };
        documents = lib.mkOption {
          type = lib.types.listOf (lib.types.attrsOf lib.types.anything);
          default = [ ];
          example = lib.literalExpression ''
            [
              {
                apiVersion = "rack.example.org/v1alpha1";
                kind = "Rack";
                metadata.name = "rack";
                spec.location = "rack-a/u12";
              }
            ]
          '';
          description = ''
            Config documents of the node besides those chalkctl generates from its other options,
            each an attribute set of `apiVersion`, `kind`, `metadata` and `spec`. Extensions add
            the documents of the kinds their role modules declare under `chalkos.kinds`; chalkctl
            and chalkd check them against the schemas of the node's image. Values are serialised to
            JSON, so a path becomes the path of its copy in the Nix store, not the text of the file.
          '';
        };
      };
    };
in
{
  options.chalkos.nodes = lib.mkOption {
    type = lib.types.attrsOf (lib.types.submodule nodeModule);
    default = { };
    description = "Nodes of the cluster. Extensions add their own per-node options to this submodule, and the documents of their kinds to `documents`.";
  };
}
