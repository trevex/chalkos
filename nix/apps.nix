{ pkgs, self }:
let
  inherit (pkgs) lib;
  chalkPkgs = self.packages.${pkgs.stdenv.hostPlatform.system};
  testEnv = import ./testing/env.nix { inherit pkgs self; };
  docs = import ./docs.nix { inherit pkgs self; };

  # TestKubernetesCluster without the test's registry: the nodes' mirror is unreachable, so
  # containerd falls back to pulling their images from the upstream registries. It needs network
  # access and /dev/kvm, so it runs on the host rather than as a check; arguments go to the test
  # binary.
  kubernetesOnline = pkgs.writeShellApplication {
    name = "chalkos-e2e-kubernetes-online";
    runtimeInputs = testEnv.tools ++ [ chalkPkgs.chalklab-e2e ];
    text = ''
      ${lib.concatStrings (
        lib.mapAttrsToList (name: value: "export ${name}=${lib.escapeShellArg value}\n") (
          testEnv.vars
          // {
            CHALKLAB_CHALKCTL = lib.getExe chalkPkgs.chalkctl;
            CHALKLAB_CHALKLAB = lib.getExe chalkPkgs.chalklab;
            CHALKLAB_SECRETS = "${chalkPkgs.test-secrets}/secrets.json";
            CHALKLAB_MANIFESTS = "${chalkPkgs.test-manifests}";
            CHALKLAB_K8S_CONTROLPLANE_IMAGE_DIR = "${chalkPkgs.test-kubernetes-controlplane-image}";
            CHALKLAB_K8S_WORKER_IMAGE_DIR = "${chalkPkgs.test-kubernetes-worker-image}";
            CHALKLAB_K8S_ONLINE = "1";
          }
        )
      )}
      exec chalklab-e2e -test.v -test.run '^TestKubernetesCluster$' -test.timeout 60m "$@"
    '';
  };
in
{
  chalklab = {
    type = "app";
    program = lib.getExe chalkPkgs.chalklab;
    meta.description = "Run a chalkos cluster's nodes as QEMU virtual machines on this machine";
  };
  docgen = {
    type = "app";
    program = lib.getExe docs.write;
    meta.description = "Write the documentation pages generated from the code into the working tree";
  };
  docs-serve = {
    type = "app";
    program = lib.getExe docs.serve;
    meta.description = "Serve the documentation from the working tree while it is edited";
  };
  e2e-kubernetes-online = {
    type = "app";
    program = lib.getExe kubernetesOnline;
    meta.description = "Run the Kubernetes e2e test with images pulled from the upstream registries";
  };
}
