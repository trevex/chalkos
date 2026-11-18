# The tests of the cluster registry on Kubernetes images: the refusal of a control plane that
# serves no endpoint.
{
  # A worker of no endpoint is accepted.
  kubernetes-registry-worker.config = ./tests/worker.yaml;
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
