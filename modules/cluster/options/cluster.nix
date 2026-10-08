{ lib, ... }:
let
  # The version of secrets.pub.json chalkos reads; chalkctl gen secrets writes no other.
  publicVersion = 3;
  # A file that exists at evaluation, such as a path in the flake, is read and checked then. One a
  # derivation builds would need import from derivation; the image build checks it instead.
  readable =
    p:
    builtins.isPath p
    || !lib.any (c: c ? outputs || c.allOutputs or false) (
      lib.attrValues (builtins.getContext (toString p))
    );
  checkPublic =
    p:
    let
      version = (builtins.fromJSON (builtins.readFile p)).version or null;
    in
    if p == null || !readable p || version == publicVersion then
      p
    else
      throw "chalkos.cluster.osCA: ${toString p} is version ${toString version}; chalkos reads version ${toString publicVersion} only, so generate new secrets with chalkctl gen secrets";
in
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
      apply = checkPublic;
      description = ''
        Public part of the cluster's secrets (`secrets.pub.json` from `chalkctl gen secrets`, version
        ${toString publicVersion}). Role images and the cluster's installer carry its OS CA, so chalkd
        in maintenance mode accepts only clients with a certificate from it. null builds images
        that accept any client until they are installed.
      '';
    };
    system = lib.mkOption {
      type = lib.types.str;
      default = "x86_64-linux";
      description = "Platform the role images are built for.";
    };
  };
}
