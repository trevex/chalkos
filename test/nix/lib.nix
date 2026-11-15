# Helpers of the evaluation tests.
{ lib, mkImages }:
rec {
  # Whether evaluating the value in full fails.
  fails = value: !(builtins.tryEval (builtins.deepSeq value true)).success;
  # Whether the image c fails an assertion whose message holds part.
  refuses = part: c: lib.any (a: !a.assertion && lib.hasInfix part a.message) c.assertions;
  # The image c's /etc/chalkos/<file>, parsed.
  etcJSON =
    file: c:
    builtins.fromJSON (builtins.unsafeDiscardStringContext c.environment.etc."chalkos/${file}".text);
  # Whether the list of packages holds one of the name.
  hasPackage = name: lib.any (p: lib.getName p == name);
  # The metal image of a variant worker, which runs Kubernetes, with the NixOS modules given.
  worker = modules: (mkImages { variants.worker.modules = modules; }).nixos.worker-metal.config;
  # The installer with the NixOS modules given.
  installer =
    modules:
    (mkImages {
      variants = { };
      installer = { inherit modules; };
    }).nixos.installer.config;
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
  # A router image with the named features disabled.
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
