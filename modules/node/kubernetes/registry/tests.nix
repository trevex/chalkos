# The tests of the cluster registry on Kubernetes images: the refusals of a ClusterRegistry's
# fields and of a control plane that serves no endpoint, the files the output renders on a node
# that serves an endpoint spelled otherwise, and nothing on one that serves none, and the effects
# of a new port and of a moved address.
{ pkgs }:
let
  # The schema's pattern of a mirrored host.
  host = "^(?:[A-Za-z0-9]+(([._]|__|-+)[A-Za-z0-9]+)*)$";
  # The config of file with the node's IPv4 address moved.
  moved =
    file:
    builtins.toFile "moved-${baseNameOf file}" (
      builtins.replaceStrings [ "192.0.2.13/24" ] [ "192.0.2.14/24" ] (builtins.readFile file)
    );
  # The config of file with a Volume mounted at mountPoint added.
  withVolume =
    name: file: mountPoint:
    builtins.toFile "${name}-${baseNameOf file}" (
      builtins.readFile file
      + ''
        ---
        apiVersion: storage.chalkos.dev/v1alpha1
        kind: Volume
        metadata: {name: ${name}}
        spec: {size: 10G, mountPoint: ${mountPoint}}
      ''
    );
in
{
  # A Volume mounted at the registry's storage starts the registry again, so that it stores on
  # the volume; one mounted elsewhere does not.
  kubernetes-registry-volume = {
    before = ./tests/spelled.yaml;
    config = withVolume "registry" ./tests/spelled.yaml "/var/lib/chalkos-registry";
    effects = [ "cluster-registry" ];
  };
  kubernetes-registry-volume-elsewhere = {
    before = ./tests/spelled.yaml;
    config = withVolume "scratch" ./tests/spelled.yaml "/var/lib/scratch";
    effects = [ ];
  };
  # A moved address rewrites containerd's hosts.toml when a ClusterRegistry orders the node's
  # endpoints, and not without one.
  kubernetes-registry-moved = {
    before = ./tests/worker.yaml;
    config = moved ./tests/worker.yaml;
    effects = [
      "kubernetes"
      "networkd"
      "registry-mirrors"
    ];
  };
  kubernetes-registry-moved-unregistered = {
    before = ./tests/unregistered.yaml;
    config = moved ./tests/unregistered.yaml;
    effects = [
      "kubernetes"
      "networkd"
    ];
  };
  # A worker of no endpoint is accepted and runs no registry.
  kubernetes-registry-worker = {
    config = ./tests/worker.yaml;
    outputs.cluster-registry = pkgs.emptyDirectory;
  };
  # A control plane serves an endpoint spelled otherwise than its address: the output renders
  # the registry's port and firewall table.
  kubernetes-registry-spelled = {
    config = ./tests/spelled.yaml;
    outputs.cluster-registry = ./tests/spelled/cluster-registry;
  };
  # A new port starts the registry again and rewrites containerd's hosts.toml.
  kubernetes-registry-port = {
    before = ./tests/spelled.yaml;
    config = ./tests/port.yaml;
    effects = [
      "cluster-registry"
      "registry-mirrors"
    ];
  };
  # Hosts with two separators in a row and a port below 1024 are refused.
  kubernetes-registry-bad-fields = {
    config = ./tests/bad-fields.yaml;
    refusals = [
      {
        message =
          "ClusterRegistry \"cluster\": - at '/spec/mirrors/1': 'a..b' does not match pattern '${host}'\n"
          + "- at '/spec/mirrors/2': 'foo-.bar' does not match pattern '${host}'\n"
          + "- at '/spec/port': minimum: got 443, want 1,024";
      }
    ];
  };
  # A control plane of no endpoint is refused. node.addresses come from the test's Network.
  kubernetes-registry-elsewhere = {
    config = ./tests/elsewhere.yaml;
    refusals = [
      {
        kind = "ClusterRegistry";
        name = "cluster";
        path = "spec.endpoints";
        message = "the control plane example serves the cluster registry, but the ClusterRegistry's endpoints name none of its static addresses (192.0.2.13, fd00::13); add one of them to the endpoints and apply the config to every node";
      }
    ];
  };
}
