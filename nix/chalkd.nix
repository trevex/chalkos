# chalkd, the node agent. It takes only the packages it imports, so other Go changes do not
# rebuild the images. withTests keeps the tests in its source, for the image-checker check, which
# builds those of pkg/imagecheck.
{
  callPackage,
  withTests ? false,
}:
callPackage ./go-module.nix { } {
  pname = "chalkd";
  inherit withTests;
  paths = [
    ../cmd/chalkd
    ../pkg/api
    ../pkg/atomicfile
    ../pkg/chalkd
    ../pkg/check
    ../pkg/client
    ../pkg/clustersecrets
    ../pkg/command
    ../pkg/config
    ../pkg/diagnostics
    ../pkg/dns
    ../pkg/effects
    ../pkg/esp
    ../pkg/facts
    ../pkg/identity
    ../pkg/image
    ../pkg/imagecheck
    ../pkg/install
    ../pkg/internal/strs
    ../pkg/jsonfile
    ../pkg/kubernetes
    ../pkg/layers
    ../pkg/nodeconfig
    ../pkg/ociimage
    ../pkg/outputs
    ../pkg/pki
    ../pkg/refusals
    ../pkg/router
    ../pkg/rules
    ../pkg/schema
    ../pkg/imageref
    ../pkg/secureboot
    ../pkg/storage
    ../pkg/storeslot
    ../pkg/tool
    ../pkg/uki
    ../pkg/upgrade
    ../pkg/validators
    ../pkg/verity
  ];
  ldflags = [
    "-s"
    "-w"
  ];
}
