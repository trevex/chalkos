{
  pkgs,
  self,
}:
let
  inherit (pkgs) lib;
  testCluster = import ./testing/cluster.nix { inherit self; };
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

  chalkctl = goModule {
    pname = "chalkctl";
    paths = goPaths;
    subPackages = [ "cmd/chalkctl" ];
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

  test-image = testCluster.roles.test.image;
  test-storage-image = testCluster.roles.storage.image;

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
