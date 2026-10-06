{
  pkgs,
  self,
}:
let
  inherit (pkgs) lib;

  goSrc = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../cmd
      ../pkg
      ../test/e2e
      ../test/fixtures
    ];
  };
in
{
  test-secureboot = import ./testing/secureboot.nix { inherit pkgs; };

  chalkctl = pkgs.buildGoModule {
    pname = "chalkctl";
    version = "0.1.0";
    src = goSrc;
    vendorHash = null;
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

  test-image = (import ./testing/cluster.nix { inherit self; }).roles.test.image;

  chalklab-e2e = pkgs.buildGoModule {
    pname = "chalklab-e2e";
    version = "0.1.0";
    src = goSrc;
    vendorHash = null;
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
