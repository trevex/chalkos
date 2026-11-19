{
  pkgs,
  self,
  docs,
  ...
}:
let
  inherit (pkgs) lib;
  testEnv = import ../testing/env.nix { inherit pkgs self; };

  # TestKubernetesCluster without the test's registry: the nodes' mirror is unreachable, so
  # containerd falls back to pulling their images from the upstream registries. It needs network
  # access and /dev/kvm, so it runs on the host rather than as a check; arguments go to the test
  # binary.
  kubernetesOnline = pkgs.writeShellApplication {
    name = "chalkos-e2e-kubernetes-online";
    runtimeInputs = testEnv.tools ++ [ pkgs.chalkos.chalklab-e2e ];
    text = ''
      ${lib.concatStrings (
        lib.mapAttrsToList (name: value: "export ${name}=${lib.escapeShellArg value}\n") (
          testEnv.e2eEnv [ "CHALKLAB_K8S_IMAGE_DIR" ] {
            CHALKLAB_CHALKLAB = lib.getExe pkgs.chalkos.chalklab;
            CHALKLAB_K8S_ONLINE = "1";
          }
        )
      )}
      exec chalklab-e2e -test.v -test.run '^TestKubernetesCluster$' -test.timeout 60m "$@"
    '';
  };

  # What follows the Kubernetes releases of modules/node/kubernetes/releases.nix, set again after a
  # change of a version there; see update-pins.sh. It runs nix of the caller's PATH.
  updatePins = pkgs.writeShellApplication {
    name = "update-pins";
    runtimeInputs = [
      (import ../testing/image-layout.nix { inherit pkgs; })
      pkgs.coreutils
      pkgs.crane
      pkgs.findutils
      pkgs.git
      pkgs.gnused
      pkgs.jq
      pkgs.perl
    ];
    # jq and perl programs in single quotes name their own variables.
    excludeShellChecks = [ "SC2016" ];
    text = builtins.readFile ./update-pins.sh;
  };
in
{
  chalklab = {
    type = "app";
    program = lib.getExe pkgs.chalkos.chalklab;
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
  update-pins = {
    type = "app";
    program = lib.getExe updatePins;
    meta.description = "Set the hashes, image digests and references that follow the pinned Kubernetes releases";
  };
}
