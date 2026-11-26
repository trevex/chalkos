# chalkbuild, which runs no other program. The flake's package and each image's oci build it
# the same way, from the chalkos scope of the nixpkgs at hand, as they build chalkd. It takes only
# the packages it imports, so other Go changes do not rebuild the oci outputs.
{
  buildGoProgram,
}:
buildGoProgram {
  pname = "chalkbuild";
  paths = [
    ../../cmd/chalkbuild
    ../../pkg/arch
    ../../pkg/atomicfile
    ../../pkg/constants
    ../../pkg/chalkbuild
    ../../pkg/fatfs
    ../../pkg/image
    ../../pkg/imagepart
    ../../pkg/imagesign
    ../../pkg/internal/cli
    ../../pkg/internal/filecopy
    ../../pkg/internal/strs
    ../../pkg/jsonfile
    ../../pkg/modulepolicy
    ../../pkg/ociimage
    ../../pkg/pki
    ../../pkg/networkdschema
    ../../pkg/schema
    ../../pkg/imageref
    ../../pkg/secureboot
    ../../pkg/uki
    ../../pkg/verity
    ../../pkg/version
  ];
}
