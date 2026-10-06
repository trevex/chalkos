# The chalkos namespace inside a role image: cluster-wide settings from the cluster definition
# arrive as freeform values under their layer 1 names; node-specific values do not exist here.
{ lib, ... }:
{
  options.chalkos = lib.mkOption {
    type = lib.types.submodule {
      freeformType = lib.types.lazyAttrsOf lib.types.anything;
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
      namespaces) are set from the cluster definition; node options (`chalkos.node`,
      `chalkos.disk`) are declared by the node modules.
    '';
  };
}
