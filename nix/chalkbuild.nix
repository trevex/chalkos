# chalkbuild, with the tools that read and sign images. The flake's package and mkImages's oci
# outputs build it alike; mkImages builds it from the images' own nixpkgs, as they build chalkd.
# It takes only the packages it imports, so other Go changes do not rebuild the oci outputs.
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
    ../pkg/chalkbuild
    ../pkg/image
    ../pkg/imagesign
    ../pkg/internal/cli
    ../pkg/internal/filecopy
    ../pkg/internal/strs
    ../pkg/ociimage
    ../pkg/pki
    ../pkg/schema
    ../pkg/imageref
    ../pkg/secureboot
    ../pkg/uki
    ../pkg/verity
  ];
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
