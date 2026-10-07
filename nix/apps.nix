{ pkgs, self }:
let
  inherit (pkgs) lib;
  chalkPkgs = self.packages.${pkgs.stdenv.hostPlatform.system};
  testEnv = import ./testing/env.nix { inherit pkgs self; };

  # TestKubernetesCluster with the nodes pulling their images from the upstream registries. It
  # needs network access and /dev/kvm, so it runs on the host rather than as a check; arguments
  # go to the test binary.
  kubernetesOnline = pkgs.writeShellApplication {
    name = "chalkos-e2e-kubernetes-online";
    runtimeInputs = testEnv.tools ++ [ chalkPkgs.chalklab-e2e ];
    text = ''
      ${lib.concatStrings (
        lib.mapAttrsToList (name: value: "export ${name}=${lib.escapeShellArg value}\n") (
          testEnv.vars
          // {
            CHALKLAB_CHALKCTL = lib.getExe chalkPkgs.chalkctl;
            CHALKLAB_SECRETS = "${chalkPkgs.test-secrets}/secrets.json";
            CHALKLAB_MANIFESTS = "${chalkPkgs.test-manifests}";
            CHALKLAB_K8S_CONTROLPLANE_IMAGE_DIR = "${chalkPkgs.test-kubernetes-online-controlplane-image}";
            CHALKLAB_K8S_WORKER_IMAGE_DIR = "${chalkPkgs.test-kubernetes-online-worker-image}";
            CHALKLAB_K8S_ONLINE = "1";
          }
        )
      )}
      exec chalklab-e2e -test.v -test.run '^TestKubernetesCluster$' -test.timeout 60m "$@"
    '';
  };
in
{
  e2e-kubernetes-online = {
    type = "app";
    program = lib.getExe kubernetesOnline;
    meta.description = "Run the Kubernetes e2e test with images from the upstream registries";
  };
}
