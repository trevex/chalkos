# The kinds every image accepts: the node's host name, secrets, OCI registries with their
# credentials and mirrors, storage, time and, on images with Kubernetes, the cluster's Kubernetes
# settings with its API server's endpoint and manifests and the node's type and its kubelet's
# settings. chalkd translates them into the node's identity and its Kubernetes cluster. Router
# images accept the router's kinds as well, which the router layer's outputs render.
{
  imports = [
    ./core.nix
    ./oci.nix
    ./storage.nix
    ./time.nix
    ./kubernetes.nix
    ./router.nix
  ];
}
