# The layers, the flake's nixosModules: an image lists at most one in its modules, which enables
# the layer's modules in modules/node. An image without one is a plain image.
{
  kubernetes = {
    _file = "chalkos.nixosModules.kubernetes";
    chalkos.kubernetes.enable = true;
  };
  router = {
    _file = "chalkos.nixosModules.router";
    chalkos.router.enable = true;
  };
}
