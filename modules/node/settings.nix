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
    };
    default = { };
    description = ''
      chalkos settings. Cluster-wide values (`chalkos.cluster`, `chalkos.secureBoot`, feature
      namespaces) are set read-only from the cluster definition; node options (`chalkos.node`,
      `chalkos.disk`) are declared by the node modules. `node`, `disk` and `nodes` are therefore
      reserved and cannot be used as feature namespaces.
    '';
  };
}
