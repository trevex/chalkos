# chalkbuild, with the tools that read and sign images. The flake's package and mkImages's oci
# outputs build it alike; mkImages builds it from the images' own nixpkgs, as they build chalkd.
{
  lib,
  callPackage,
  makeWrapper,
  mtools,
  sbsigntool,
  coreutils,
}:
callPackage ./go-module.nix { } {
  pname = "chalkbuild";
  paths = [
    ../cmd/chalkbuild
    ../pkg
  ];
  subPackages = [ "cmd/chalkbuild" ];
  # The go-unit check runs its tests.
  doCheck = false;
  meta.mainProgram = "chalkbuild";
  nativeBuildInputs = [ makeWrapper ];
  postFixup = ''
    wrapProgram $out/bin/chalkbuild --prefix PATH : ${
      lib.makeBinPath [
        mtools
        sbsigntool
        coreutils
      ]
    }
  '';
}
