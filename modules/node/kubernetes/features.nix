# kubernetesLib, which the Kubernetes layer's features get as a module argument, as the router's
# parts get routerLib: the features an image may leave out or add, and their option.
{ chalkosLib, ... }:
let
  kubernetesLib = {
    # The features of the layer, each with its option chalkos.kubernetes.<feature>.enable.
    features = [
      "bgp"
      "ceph"
      "kubevirt"
      "longhorn"
      "nvidia"
      "registry"
    ];
    # The option chalkos.kubernetes.<name>.enable of a feature, and its other options; name must
    # be one of features.
    feature =
      chalkosLib.internal.layerFeature "Kubernetes" "kubernetesLib.features"
        kubernetesLib.features;
  };
in
{
  # Every image declares the features' options, which read kubernetesLib, so it is set whether the
  # image runs Kubernetes or not.
  _module.args.kubernetesLib = kubernetesLib;
}
