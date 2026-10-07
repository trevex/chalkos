{
  pkgs,
  self,
}:
let
  inherit (pkgs) lib;
  testing = import ./testing/cluster.nix { inherit self pkgs; };
  goModule = pkgs.callPackage ./go-module.nix { };

  goPaths = [
    ../cmd
    ../pkg
    ../test/e2e
    ../test/fixtures
  ];
in
{
  test-secureboot = import ./testing/secureboot.nix { inherit pkgs; };
  test-secrets = testing.secrets;
  test-manifests = testing.manifests;

  chalkctl = goModule {
    pname = "chalkctl";
    paths = goPaths;
    subPackages = [ "cmd/chalkctl" ];
    meta.mainProgram = "chalkctl";
    nativeBuildInputs = [ pkgs.makeWrapper ];
    postFixup = ''
      wrapProgram $out/bin/chalkctl --prefix PATH : ${
        lib.makeBinPath [
          pkgs.mtools
          pkgs.sbsigntool
        ]
      }
    '';
  };

  chalkos-storage = pkgs.callPackage ./chalkos-storage.nix { };
  chalkd = pkgs.callPackage ./chalkd.nix { };

  test-image = testing.cluster.roles.test.image;
  test-storage-image = testing.cluster.roles.storage.image;
  test-installer = testing.cluster.installer;
  test-kubernetes-controlplane-image = testing.cluster.roles.k8s-controlplane.image;
  test-kubernetes-worker-image = testing.cluster.roles.k8s-worker.image;
  test-kubernetes-images = testing.kubernetesImages;
  # For TestKubernetesCluster with CHALKLAB_K8S_ONLINE=1, run outside the sandbox: the nodes
  # pull from the upstream registries.
  test-kubernetes-online-controlplane-image = testing.online.roles.k8s-controlplane.image;
  test-kubernetes-online-worker-image = testing.online.roles.k8s-worker.image;

  # The installer of a cluster without an OS CA: it accepts any client until it installs a node.
  installer =
    (self.lib.mkCluster {
      modules = [
        {
          chalkos.cluster = {
            name = "generic";
            endpoint = "https://127.0.0.1:6443";
          };
        }
      ];
    }).installer;

  chalklab-e2e = goModule {
    pname = "chalklab-e2e";
    paths = goPaths;
    doCheck = false;
    buildPhase = ''
      runHook preBuild
      go test -c -o chalklab-e2e ./test/e2e
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      install -Dm755 chalklab-e2e $out/bin/chalklab-e2e
      runHook postInstall
    '';
  };
}
