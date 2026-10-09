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
          the role. Read node-specific values at runtime through chalkos.node.file or
          chalkos.node.consumers.
        '';
        defaultText = lib.literalMD "unavailable in role images";
        description = "Node definitions; available only in the cluster definition.";
      };
      options.role.name = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          The role's name in the cluster definition, set by the role builder; null on the
          installer, which belongs to no role. A node refuses to upgrade to an image of another
          role.
        '';
      };
      options.role.kubernetes.kind = lib.mkOption {
        type = lib.types.nullOr (
          lib.types.enum [
            "controlplane"
            "worker"
          ]
        );
        default = null;
        description = ''
          The role's `kubernetes.kind` from the cluster definition, set by the role builder;
          null builds an image without Kubernetes, as the installer is.
        '';
      };
    };
    default = { };
    description = ''
      chalkos settings. Cluster-wide values (`chalkos.cluster`, `chalkos.secureBoot`, feature
      namespaces) are set read-only from the cluster definition; image options (`chalkos.node`,
      `chalkos.disk`, `chalkos.role`) are declared by the node modules. `node`, `disk`, `role`
      and `nodes` are therefore reserved and cannot be used as feature namespaces.
    '';
  };
}
