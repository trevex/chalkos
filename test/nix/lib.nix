# Helpers of the evaluation tests.
{ mkImages }:
{
  # Whether evaluating the value in full fails.
  fails = value: !(builtins.tryEval (builtins.deepSeq value true)).success;
  # The metal image of a variant worker, which runs Kubernetes, with the NixOS modules given.
  worker = modules: (mkImages { variants.worker.modules = modules; }).nixos.worker-metal.config;
}
