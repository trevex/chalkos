# chalkbuild, with the tools that read and sign images. The flake's package and mkImages's oci
# outputs build it the same way, from the chalkos scope of the nixpkgs at hand, as they build
# chalkd. It takes only the packages it imports, so other Go changes do not rebuild the oci
# outputs.
{
  buildGoProgram,
  coreutils,
  lib,
  makeWrapper,
  mtools,
  sbsigntool,
}:
buildGoProgram {
  pname = "chalkbuild";
  paths = [
    ../cmd/chalkbuild
    ../pkg/arch
    ../pkg/atomicfile
    ../pkg/chalkbuild
    ../pkg/image
    ../pkg/imagepart
    ../pkg/imagesign
    ../pkg/internal/cli
    ../pkg/internal/filecopy
    ../pkg/internal/strs
    ../pkg/jsonfile
    ../pkg/ociimage
    ../pkg/pki
    ../pkg/networkdschema
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
