{
  pkgs,
  nixpkgs,
  self,
}:
let
  inherit (pkgs) lib;

  goSrc = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../cmd
      ../internal
      ../tests
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
}
