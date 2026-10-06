{ lib, ... }:
{
  options.chalkos.cluster = {
    name = lib.mkOption {
      type = lib.types.strMatching "[a-z0-9][a-z0-9-]*";
      description = "Cluster name. The cluster is expected under the flake output `chalkos.<name>`.";
    };
    endpoint = lib.mkOption {
      type = lib.types.str;
      example = "https://10.0.0.10:6443";
      description = "URL of the Kubernetes API server used by nodes and clients.";
    };
    system = lib.mkOption {
      type = lib.types.str;
      default = "x86_64-linux";
      description = "Platform the role images are built for.";
    };
  };
}
