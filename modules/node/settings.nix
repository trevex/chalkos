# The chalkos namespace inside a role image. Cluster-wide settings from the cluster definition
# arrive as read-only options under their layer 1 names (declared by the role builder); node
# options are declared by the node modules; node-specific values do not exist here.
# Cluster settings flow into every role image, so they must not be derived from `chalkos.roles`.
{ lib, ... }:
{
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      options.nodes = lib.mkOption {
        readOnly = true;
        visible = false;
        default = throw ''
          config.chalkos.nodes is not available in a role image: one image serves every node of
          the role. Read node-specific values at runtime from config documents: declare a kind
          under chalkos.kinds, and the unit that reads it under chalkos.consumers.
        '';
        defaultText = lib.literalMD "unavailable in role images";
        description = "Node definitions; available only in the cluster definition.";
      };
      options.variant.name = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          The image's variant, which its os-release names as `CHALKOS_VARIANT` and its image
          directory's `metadata.json` as `variant`. The role builder sets it to the role's name.
          null on the installer, which is no variant. No check compares it: a node may change its
          variant through an upgrade, and the image's schemas decide which config it takes.
        '';
      };
      options.platform.name = lib.mkOption {
        type = lib.types.nullOr (lib.types.strMatching "[a-z0-9][a-z0-9-]*");
        default = null;
        description = ''
          The platform the image is built for, a name of `chalkos.platforms`, set by the role
          builder; null on the installer, which installs images of every platform. A node refuses
          an image of another platform, and translates its config for its own.
        '';
      };
      options.role.kubernetes.enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Whether the image runs Kubernetes, from the role's `kubernetes.enable`, set by the role
          builder; the installer runs none. A node's kind comes with its config.
        '';
      };
    };
    default = { };
    description = ''
      chalkos settings. Cluster-wide values (`chalkos.cluster`, `chalkos.secureBoot`,
      `chalkos.cni`, `chalkos.time` and feature namespaces) are set read-only from the cluster
      definition; image options (`chalkos.consumers`, `chalkos.debug`, `chalkos.disk`,
      `chalkos.kernel`, `chalkos.kinds`, `chalkos.maintenance`, `chalkos.platform`, `chalkos.role`,
      `chalkos.upgrade`, `chalkos.variant`) are declared by the node modules. `consumers`, `debug`,
      `disk`, `kernel`, `kinds`, `maintenance`, `nodes`, `platform`, `role`, `upgrade` and
      `variant` are therefore reserved and cannot be used as feature namespaces.
    '';
  };
}
