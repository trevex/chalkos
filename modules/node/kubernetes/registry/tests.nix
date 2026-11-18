# The tests of the cluster registry on Kubernetes images: the refusals of a ClusterRegistry's
# fields and of a control plane that serves no endpoint, and endpoints spelled otherwise.
let
  # The schema's pattern of a mirrored host.
  host = "^(?:[A-Za-z0-9]+(([._]|__|-+)[A-Za-z0-9]+)*)$";
in
{
  # A worker of no endpoint is accepted.
  kubernetes-registry-worker.config = ./tests/worker.yaml;
  # A control plane serves an endpoint spelled otherwise than its address.
  kubernetes-registry-spelled.config = ./tests/spelled.yaml;
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
