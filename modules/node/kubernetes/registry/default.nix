# The cluster registry on images with Kubernetes whose chalkos.kubernetes.registry.enable is true:
# the ClusterRegistry kind, one per cluster, whose endpoints name the nodes that serve it.
{
  chalkosLib,
  config,
  lib,
  ...
}:
let
  inherit (lib) mkOption types;
  inherit (chalkosLib.internal) builtin cel;
  inherit (chalkosLib.internal.api) groups apiVersions;
  inherit (chalkosLib.types) address;
  k = config.chalkos.kubernetes;
  # The Node documents of the delivered config, by name.
  nodes = cel.documents "config" apiVersions.kubernetes "Node";
in
{
  options.chalkos.kubernetes = chalkosLib.internal.feature "Kubernetes" "registry" {
    carries = "chalkos-registry, the cluster registry,";
    kinds = [ "ClusterRegistry" ];
  };

  config = lib.mkIf (k.enable && k.registry.enable) {
    chalkos.tests = import ./tests.nix;
    chalkos.kinds.ClusterRegistry = builtin groups.kubernetes // {
      description = "The cluster registry: chalkos-registry on the nodes whose static addresses its endpoints name, every control plane among them. containerd on every node pulls the images of the mirrored hosts from it first, before their Mirror endpoints and the hosts themselves.";
      spec = {
        mirrors = chalkosLib.mkOption {
          # A host is the first component of the repository paths below which the cluster registry
          # holds its images, as the distribution spec takes it in any case (Go lower-cases it):
          # neither a port nor an IPv6 address, and no two separators in a row.
          type = types.nonEmptyListOf (types.strMatching "[A-Za-z0-9]+(([._]|__|-+)[A-Za-z0-9]+)*");
          unique = {
            across = "list";
            as = "host";
            hint = "name each host once";
          };
          example = [
            "docker.io"
            "ghcr.io"
            "registry.k8s.io"
          ];
          description = "Registry hosts whose images containerd pulls from the cluster registry first, as image references name them, without a port; docker.io is Docker Hub. The cluster registry holds an image below its host's name, in lower case: docker.io/library/nginx is <endpoint>/docker.io/library/nginx.";
        };
        endpoints = chalkosLib.mkOption {
          type = types.nonEmptyListOf address;
          unique = {
            across = "list";
            as = "ip";
            hint = "name each address once";
          };
          example = [
            "fdc8:a1b2:c3d4:ff::11"
            "fdc8:a1b2:c3d4:ff::12"
          ];
          description = "IP addresses of the nodes that serve the cluster registry, in the order nodes pull from them; a node that serves it pulls from itself first. A node serves it when one of these is one of its static addresses, the Address= values of its Network documents, and every control plane serves it. chalkctl registry push pushes to each, and nothing copies images between them.";
          # node.addresses are the node's static addresses, as its node certificate names them;
          # ip() compares every spelling of an address alike.
          rules = [
            {
              description = "Every control plane serves the cluster registry: one of its static addresses is an endpoint.";
              rule = ''cel.bind(nodes, ${nodes}, !nodes.exists(n, nodes[n].spec.type == "controlplane")) || node.addresses.exists(a, self.exists(e, ip(e) == ip(a)))'';
              messageExpression = ''"the control plane " + node.name + " serves the cluster registry, but the ClusterRegistry's endpoints name none of its static addresses (" + (size(node.addresses) == 0 ? "none" : node.addresses.join(", ")) + "); add one of them to the endpoints and apply the config to every node"'';
            }
          ];
        };
        port = mkOption {
          type = types.ints.between 1024 65535;
          default = 5000;
          description = "TCP port the cluster registry serves on, over HTTPS with the node certificate: 1024 or above, as chalkos-registry runs without privileges.";
        };
      };
    };
  };
}
