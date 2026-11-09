# Helpers of the evaluation tests.
{ lib, mkImages }:
rec {
  # Whether evaluating the value in full fails.
  fails = value: !(builtins.tryEval (builtins.deepSeq value true)).success;
  # The metal image of a variant worker, which runs Kubernetes, with the NixOS modules given.
  worker = modules: (mkImages { variants.worker.modules = modules; }).nixos.worker-metal.config;
  # The metal image of a variant edge, which runs the router layer, with the NixOS modules given.
  router =
    modules:
    (mkImages {
      variants.edge = {
        kubernetes = false;
        router = true;
        inherit modules;
      };
    }).nixos.edge-metal.config;
  # The router image without the router features named, whose modules set their
  # chalkos.router.<feature>.enable to false.
  routerWithout =
    features:
    router [
      {
        chalkos.router = lib.genAttrs features (_: {
          enable = false;
        });
      }
    ];
}
