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
    osCA = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      example = lib.literalExpression "./secrets.pub.json";
      description = ''
        Public part of the cluster's secrets (`secrets.pub.json` from `chalkctl gen secrets`). Role
        images and the cluster's installer carry its OS CA, so chalkd in maintenance mode accepts
        only clients with a certificate from it. null builds images that accept any client until
        they are installed.
      '';
    };
    system = lib.mkOption {
      type = lib.types.str;
      default = "x86_64-linux";
      description = "Platform the role images are built for.";
    };
  };
}
