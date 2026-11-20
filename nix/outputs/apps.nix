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

  # The vendorHash of the Go programs, set again after a change of go.mod or go.sum; see
  # update-vendor-hash.sh. It runs nix of the caller's PATH.
  updateVendorHash = pkgs.writeShellApplication {
    name = "update-vendor-hash";
    runtimeInputs = [
      pkgs.coreutils
      pkgs.gnugrep
      pkgs.gnused
    ];
    text = builtins.readFile ./update-vendor-hash.sh;
  };

  # govulncheck of the Go code, built with the Go release go.mod names, which also types the
  # standard library it checks. It reads the Go vulnerability database at vuln.go.dev and the
  # modules from the module proxy, so it runs on the host rather than as a check, and fails only
  # on a vulnerability that chalkos's code calls. Arguments replace ./..., the packages it checks.
  govulncheck = pkgs.writeShellApplication {
    name = "chalkos-govulncheck";
    runtimeInputs = [
      pkgs.chalkos.go
      pkgs.chalkos.go.tools.govulncheck.latest
    ];
    text = ''
      if [ ! -f go.mod ]; then
        echo "chalkos-govulncheck: run it in the repository's root, where go.mod is" >&2
        exit 1
      fi
      if [ "$#" -eq 0 ]; then
        set -- ./...
      fi
      exec govulncheck "$@"
    '';
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
  govulncheck = {
    type = "app";
    program = lib.getExe govulncheck;
    meta.description = "Check the Go code for known vulnerabilities that it calls";
  };
  update-pins = {
    type = "app";
    program = lib.getExe updatePins;
    meta.description = "Set the hashes, image digests and references that follow the pinned Kubernetes releases";
  };
  update-vendor-hash = {
    type = "app";
    program = lib.getExe updateVendorHash;
    meta.description = "Set the vendorHash of the Go programs after a change of go.mod or go.sum";
  };
}
